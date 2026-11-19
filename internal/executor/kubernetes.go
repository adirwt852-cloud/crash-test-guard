package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/adirwt852-cloud/crash-test-guard/internal/config"
)

const (
	defaultKubernetesNamespace = "default"
	kubernetesPodNamePrefix    = "crash-test-guard-"
	agentContainerName         = "agent"

	k8sLabelManaged = "crash-test-guard.managed"
	k8sLabelSession = "crash-test-guard.session"
	k8sLabelRole    = "crash-test-guard.role"

	defaultPollInterval = 250 * time.Millisecond
)

// KubernetesCrashType selects how the Kubernetes executor injects a crash.
type KubernetesCrashType string

const (
	// CrashPodDelete deletes the agent pod and lets its controller recreate it.
	CrashPodDelete KubernetesCrashType = "pod_delete"

	// CrashPodReplace deletes the agent pod and creates a fresh replacement.
	CrashPodReplace KubernetesCrashType = "pod_replace"
)

var errAgentPodNotFound = errors.New("kubernetes: agent pod not found")

// KubernetesOption configures a KubernetesExecutor.
type KubernetesOption func(*KubernetesExecutor)

// WithKubeconfig overrides the kubeconfig path used to reach the cluster.
func WithKubeconfig(path string) KubernetesOption {
	return func(e *KubernetesExecutor) {
		if strings.TrimSpace(path) != "" {
			e.kubeconfig = path
		}
	}
}

// WithKubeContext overrides the kubeconfig context.
func WithKubeContext(name string) KubernetesOption {
	return func(e *KubernetesExecutor) {
		e.kubeContext = name
	}
}

// WithKubernetesNamespace sets the default namespace for agent pods.
func WithKubernetesNamespace(ns string) KubernetesOption {
	return func(e *KubernetesExecutor) {
		if strings.TrimSpace(ns) != "" {
			e.namespace = ns
		}
	}
}

// WithPodName sets the default pod name to manage.
func WithPodName(name string) KubernetesOption {
	return func(e *KubernetesExecutor) {
		if strings.TrimSpace(name) != "" {
			e.podName = name
		}
	}
}

// WithLabelSelector sets the default label selector used to discover agent pods.
func WithLabelSelector(selector string) KubernetesOption {
	return func(e *KubernetesExecutor) {
		if strings.TrimSpace(selector) != "" {
			e.labelSelector = selector
		}
	}
}

// WithKubernetesCrashType sets the crash injection strategy used by Restart.
func WithKubernetesCrashType(t KubernetesCrashType) KubernetesOption {
	return func(e *KubernetesExecutor) {
		e.crashType = t
	}
}

// WithKubernetesKillSignal sets the signal semantics used by Kill. SIGKILL
// deletes the pod without a grace period; SIGTERM honours the shutdown timeout.
func WithKubernetesKillSignal(sig syscall.Signal) KubernetesOption {
	return func(e *KubernetesExecutor) {
		e.killSignal = sig
	}
}

// KubernetesExecutor manages an agent running as a Kubernetes pod.
type KubernetesExecutor struct {
	mu sync.Mutex

	client      kubernetes.Interface
	kubeconfig  string
	kubeContext string

	namespace     string
	podName       string
	labelSelector string
	crashType     KubernetesCrashType
	killSignal    syscall.Signal

	cfg     config.AgentConfig
	session string
	pod     string
	owned   bool
	status  Status
	started bool
	exit    int
}

var _ Executor = (*KubernetesExecutor)(nil)

// NewKubernetesExecutor creates an executor that drives the Kubernetes API.
func NewKubernetesExecutor(opts ...KubernetesOption) *KubernetesExecutor {
	e := &KubernetesExecutor{
		crashType:  CrashPodDelete,
		killSignal: syscall.SIGKILL,
		session:    randomHex(8),
		status:     StatusUnknown,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}
	return e
}

// SetCrashType updates the crash strategy. Valid values are pod_delete and
// pod_replace.
func (e *KubernetesExecutor) SetCrashType(t KubernetesCrashType) error {
	switch t {
	case CrashPodDelete, CrashPodReplace:
	default:
		return fmt.Errorf("kubernetes: unsupported crash type %q", t)
	}
	e.mu.Lock()
	e.crashType = t
	e.mu.Unlock()
	return nil
}

// PodName returns the name of the tracked agent pod.
func (e *KubernetesExecutor) PodName() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.pod
}

// Namespace returns the namespace of the tracked agent pod.
func (e *KubernetesExecutor) Namespace() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.namespace
}

// Start resolves an existing agent pod or creates one from cfg, then waits for
// the pod to become ready.
func (e *KubernetesExecutor) Start(ctx context.Context, cfg config.AgentConfig) error {
	e.mu.Lock()
	if e.status == StatusRunning {
		e.mu.Unlock()
		return fmt.Errorf("kubernetes: agent pod is already running")
	}
	e.cfg = cfg
	if ns := strings.TrimSpace(cfg.Namespace); ns != "" {
		e.namespace = ns
	}
	if e.namespace == "" {
		e.namespace = defaultKubernetesNamespace
	}
	if name := strings.TrimSpace(cfg.PodName); name != "" {
		e.podName = name
	}
	if sel := strings.TrimSpace(cfg.LabelSelector); sel != "" {
		e.labelSelector = sel
	}
	namespace := e.namespace
	session := e.session
	e.mu.Unlock()

	client, err := e.ensureClient(cfg)
	if err != nil {
		return err
	}

	pod, err := resolveAgentPod(ctx, client, namespace, e.podName, e.labelSelector)
	if err == nil {
		if err := e.waitForReady(ctx, client, pod.Name, cfg.StartupTimeout.Duration); err != nil {
			return err
		}
		e.markStarted(pod.Name, false)
		return nil
	}
	if !errors.Is(err, errAgentPodNotFound) && !apierrors.IsNotFound(err) {
		return err
	}

	if strings.TrimSpace(cfg.Image) == "" {
		return fmt.Errorf("kubernetes: image is required to create an agent pod")
	}

	created, err := e.createPod(ctx, client, namespace, session, cfg)
	if err != nil {
		return err
	}
	if err := e.waitForReady(ctx, client, created.Name, cfg.StartupTimeout.Duration); err != nil {
		return err
	}
	e.markStarted(created.Name, true)
	return nil
}

// Wait blocks until the agent pod reaches a terminal phase or ctx is cancelled.
func (e *KubernetesExecutor) Wait(ctx context.Context) error {
	e.mu.Lock()
	name := e.pod
	started := e.started
	namespace := e.namespace
	e.mu.Unlock()

	if !started || name == "" {
		return fmt.Errorf("kubernetes: agent pod has not been started")
	}

	client, err := e.ensureClient(e.cfg)
	if err != nil {
		return err
	}

	err = wait.PollUntilContextCancel(ctx, defaultPollInterval, true, func(ctx context.Context) (bool, error) {
		pod, err := client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			return false, err
		}
		switch pod.Status.Phase {
		case corev1.PodSucceeded, corev1.PodFailed:
			code := podExitCode(pod)
			e.mu.Lock()
			e.exit = code
			if pod.Status.Phase == corev1.PodFailed || code != 0 {
				e.status = StatusCrashed
			} else {
				e.status = StatusStopped
			}
			e.mu.Unlock()
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("kubernetes: wait for pod: %w", err)
	}
	return nil
}

// Kill terminates the agent pod using the configured signal semantics.
func (e *KubernetesExecutor) Kill(ctx context.Context) error {
	e.mu.Lock()
	name := e.pod
	started := e.started
	namespace := e.namespace
	selector := e.labelSelector
	sig := e.killSignal
	timeout := e.cfg.ShutdownTimeout.Duration
	e.mu.Unlock()

	if !started {
		return nil
	}

	client, err := e.ensureClient(e.cfg)
	if err != nil {
		return err
	}

	if name == "" {
		pod, err := resolveAgentPod(ctx, client, namespace, e.podName, selector)
		if errors.Is(err, errAgentPodNotFound) || apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		name = pod.Name
	}

	grace := int64(0)
	final := StatusCrashed
	if sig == syscall.SIGTERM {
		seconds := int64(timeout.Seconds())
		if seconds <= 0 {
			seconds = 10
		}
		grace = seconds
		final = StatusStopped
	}

	if err := e.deletePod(ctx, client, namespace, name, grace); err != nil {
		return err
	}

	e.mu.Lock()
	e.status = final
	e.mu.Unlock()
	return nil
}

// Restart deletes the agent pod. With pod_delete the replacement is expected to
// be recreated by a controller; with pod_replace the executor creates it.
func (e *KubernetesExecutor) Restart(ctx context.Context) error {
	e.mu.Lock()
	crash := e.crashType
	e.mu.Unlock()

	if crash == CrashPodReplace {
		return e.Replace(ctx)
	}
	return e.restartAfterDelete(ctx)
}

// Replace deletes the current agent pod and launches a fresh one from the
// stored configuration.
func (e *KubernetesExecutor) Replace(ctx context.Context) error {
	e.mu.Lock()
	name := e.pod
	namespace := e.namespace
	cfg := e.cfg
	e.mu.Unlock()

	client, err := e.ensureClient(cfg)
	if err != nil {
		return err
	}

	if name != "" {
		if err := e.deletePod(ctx, client, namespace, name, 0); err != nil {
			return err
		}
	}

	e.mu.Lock()
	e.pod = ""
	e.owned = false
	e.started = false
	e.status = StatusUnknown
	e.mu.Unlock()

	if strings.TrimSpace(cfg.Image) == "" {
		return fmt.Errorf("kubernetes: image is required to replace the agent pod")
	}

	created, err := e.createPod(ctx, client, namespace, e.session, cfg)
	if err != nil {
		return err
	}
	if err := e.waitForReady(ctx, client, created.Name, cfg.StartupTimeout.Duration); err != nil {
		return err
	}
	e.markStarted(created.Name, true)
	return nil
}

func (e *KubernetesExecutor) restartAfterDelete(ctx context.Context) error {
	e.mu.Lock()
	name := e.pod
	namespace := e.namespace
	selector := e.labelSelector
	cfg := e.cfg
	e.mu.Unlock()

	client, err := e.ensureClient(cfg)
	if err != nil {
		return err
	}

	var oldUID string
	if name != "" {
		if pod, err := client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{}); err == nil {
			oldUID = string(pod.UID)
		}
		if err := e.deletePod(ctx, client, namespace, name, 0); err != nil {
			return err
		}
	}

	timeout := cfg.StartupTimeout.Duration
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	var replacement *corev1.Pod
	err = wait.PollUntilContextTimeout(ctx, defaultPollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		pod, err := resolveAgentPod(ctx, client, namespace, e.podName, selector)
		if err != nil {
			if errors.Is(err, errAgentPodNotFound) || apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		if oldUID != "" && string(pod.UID) == oldUID {
			return false, nil
		}
		if !podReady(pod) {
			return false, nil
		}
		replacement = pod
		return true, nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if strings.TrimSpace(cfg.Image) != "" && selector == "" && e.podName == "" {
			created, cerr := e.createPod(ctx, client, namespace, e.session, cfg)
			if cerr != nil {
				return cerr
			}
			if werr := e.waitForReady(ctx, client, created.Name, cfg.StartupTimeout.Duration); werr != nil {
				return werr
			}
			e.markStarted(created.Name, true)
			return nil
		}
		return fmt.Errorf("kubernetes: pod was not recreated: %w", err)
	}

	e.markStarted(replacement.Name, false)
	return nil
}

// Status reports the pod phase via the Kubernetes API.
func (e *KubernetesExecutor) Status(ctx context.Context) (Status, error) {
	e.mu.Lock()
	started := e.started
	name := e.pod
	namespace := e.namespace
	selector := e.labelSelector
	stored := e.status
	cfg := e.cfg
	e.mu.Unlock()

	if !started {
		return StatusUnknown, nil
	}

	client, err := e.ensureClient(cfg)
	if err != nil {
		return stored, err
	}

	var pod *corev1.Pod
	if name != "" {
		pod, err = client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	} else {
		pod, err = resolveAgentPod(ctx, client, namespace, e.podName, selector)
	}
	if err != nil {
		if apierrors.IsNotFound(err) || errors.Is(err, errAgentPodNotFound) {
			e.mu.Lock()
			e.status = StatusUnknown
			e.mu.Unlock()
			return StatusUnknown, nil
		}
		return stored, fmt.Errorf("kubernetes: get pod: %w", err)
	}

	status, code := mapPodPhase(pod)
	e.mu.Lock()
	e.status = status
	e.exit = code
	e.mu.Unlock()
	return status, nil
}

// Cleanup deletes the tracked pod and any pods created by this session.
func (e *KubernetesExecutor) Cleanup(ctx context.Context) error {
	e.mu.Lock()
	name := e.pod
	namespace := e.namespace
	session := e.session
	cfg := e.cfg
	e.mu.Unlock()

	if namespace == "" {
		namespace = defaultKubernetesNamespace
	}

	client, err := e.ensureClient(cfg)
	if err == nil {
		if name != "" {
			_ = e.deletePod(ctx, client, namespace, name, 0)
		}
		if session != "" {
			pods, lerr := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: k8sLabelManaged + "=true," + k8sLabelSession + "=" + session,
			})
			if lerr == nil {
				for _, pod := range pods.Items {
					_ = e.deletePod(ctx, client, namespace, pod.Name, 0)
				}
			}
		}
	}

	e.mu.Lock()
	e.pod = ""
	e.owned = false
	e.started = false
	e.status = StatusUnknown
	e.exit = 0
	e.mu.Unlock()
	return nil
}

func (e *KubernetesExecutor) markStarted(name string, owned bool) {
	e.mu.Lock()
	e.pod = name
	e.owned = owned
	e.started = true
	e.status = StatusRunning
	e.exit = 0
	e.mu.Unlock()
}

func (e *KubernetesExecutor) ensureClient(cfg config.AgentConfig) (kubernetes.Interface, error) {
	e.mu.Lock()
	if e.client != nil {
		client := e.client
		e.mu.Unlock()
		return client, nil
	}
	kubeconfig := e.kubeconfig
	if path := strings.TrimSpace(cfg.Kubeconfig); path != "" {
		kubeconfig = path
	}
	contextName := e.kubeContext
	e.mu.Unlock()

	restCfg, err := buildKubernetesConfig(kubeconfig, contextName)
	if err != nil {
		return nil, err
	}
	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("kubernetes: create client: %w", err)
	}

	e.mu.Lock()
	if e.client == nil {
		e.client = client
	}
	resolved := e.client
	e.mu.Unlock()
	return resolved, nil
}

func buildKubernetesConfig(kubeconfig, contextName string) (*rest.Config, error) {
	if strings.TrimSpace(kubeconfig) != "" {
		rules := clientcmd.NewDefaultClientConfigLoadingRules()
		rules.ExplicitPath = kubeconfig
		overrides := &clientcmd.ConfigOverrides{}
		if contextName != "" {
			overrides.CurrentContext = contextName
		}
		cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("kubernetes: load kubeconfig %q: %w", kubeconfig, err)
		}
		return cfg, nil
	}

	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}

	if contextName == "" && os.Getenv("KUBECONFIG") == "" {
		if _, err := os.Stat(defaultKubeconfigPath()); err != nil {
			return nil, fmt.Errorf("kubernetes: no kubeconfig found; set agent.kubeconfig or run in-cluster")
		}
	}

	overrides := &clientcmd.ConfigOverrides{}
	if contextName != "" {
		overrides.CurrentContext = contextName
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		overrides,
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("kubernetes: load default kubeconfig: %w", err)
	}
	return cfg, nil
}

func (e *KubernetesExecutor) createPod(ctx context.Context, client kubernetes.Interface, namespace, session string, cfg config.AgentConfig) (*corev1.Pod, error) {
	name := strings.TrimSpace(e.podName)
	if name == "" {
		name = kubernetesPodNamePrefix + randomHex(6)
	}

	volumes, mounts := buildVolumes(cfg.Volumes)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				k8sLabelManaged: "true",
				k8sLabelSession: session,
				k8sLabelRole:    "agent",
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy:      corev1.RestartPolicyNever,
			ServiceAccountName: strings.TrimSpace(cfg.ServiceAccount),
			Volumes:            volumes,
			Containers: []corev1.Container{
				{
					Name:         agentContainerName,
					Image:        strings.TrimSpace(cfg.Image),
					Command:      cfg.Command,
					Env:          buildEnv(cfg.Env),
					VolumeMounts: mounts,
				},
			},
		},
	}

	created, err := client.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("kubernetes: create pod %q: %w", name, err)
	}
	return created, nil
}

func (e *KubernetesExecutor) waitForReady(ctx context.Context, client kubernetes.Interface, name string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	e.mu.Lock()
	namespace := e.namespace
	e.mu.Unlock()

	err := wait.PollUntilContextTimeout(ctx, defaultPollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		pod, err := client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		if pod.Status.Phase == corev1.PodFailed {
			return false, fmt.Errorf("kubernetes: pod %q failed before becoming ready", name)
		}
		return podReady(pod), nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("kubernetes: wait for pod %q to be ready: %w", name, err)
	}
	return nil
}

func (e *KubernetesExecutor) deletePod(ctx context.Context, client kubernetes.Interface, namespace, name string, grace int64) error {
	policy := metav1.DeletePropagationBackground
	opts := metav1.DeleteOptions{
		GracePeriodSeconds: &grace,
		PropagationPolicy:  &policy,
	}
	err := client.CoreV1().Pods(namespace).Delete(ctx, name, opts)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("kubernetes: delete pod %q: %w", name, err)
	}
	return nil
}

func resolveAgentPod(ctx context.Context, client kubernetes.Interface, namespace, podName, selector string) (*corev1.Pod, error) {
	if strings.TrimSpace(podName) != "" {
		pod, err := client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil, errAgentPodNotFound
			}
			return nil, fmt.Errorf("kubernetes: get pod %q: %w", podName, err)
		}
		return pod, nil
	}

	if strings.TrimSpace(selector) == "" {
		return nil, fmt.Errorf("kubernetes: pod_name or label_selector is required")
	}

	pods, err := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("kubernetes: list pods: %w", err)
	}
	if len(pods.Items) == 0 {
		return nil, errAgentPodNotFound
	}

	var fallback *corev1.Pod
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Status.Phase == corev1.PodRunning && podReady(pod) {
			return pod, nil
		}
		if fallback == nil || pod.Status.Phase == corev1.PodPending {
			fallback = pod
		}
	}
	return fallback, nil
}

func podReady(pod *corev1.Pod) bool {
	if pod.Status.Phase == corev1.PodSucceeded {
		return true
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func mapPodPhase(pod *corev1.Pod) (Status, int) {
	code := podExitCode(pod)
	switch pod.Status.Phase {
	case corev1.PodPending, corev1.PodRunning:
		return StatusRunning, 0
	case corev1.PodSucceeded:
		return StatusStopped, code
	case corev1.PodFailed:
		return StatusCrashed, code
	default:
		return StatusUnknown, code
	}
}

func podExitCode(pod *corev1.Pod) int {
	code := 0
	for _, cs := range pod.Status.ContainerStatuses {
		if term := cs.State.Terminated; term != nil && int(term.ExitCode) != 0 {
			code = int(term.ExitCode)
		}
	}
	return code
}

func buildEnv(pairs []string) []corev1.EnvVar {
	var env []corev1.EnvVar
	for _, kv := range pairs {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		env = append(env, corev1.EnvVar{Name: strings.TrimSpace(key), Value: value})
	}
	return env
}

func buildVolumes(specs []string) ([]corev1.Volume, []corev1.VolumeMount) {
	var volumes []corev1.Volume
	var mounts []corev1.VolumeMount
	for i, spec := range specs {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			continue
		}
		host, target, ok := strings.Cut(spec, ":")
		if !ok || strings.TrimSpace(target) == "" {
			continue
		}
		name := "vol-" + strconv.Itoa(i)
		volumes = append(volumes, corev1.Volume{
			Name: name,
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{Path: strings.TrimSpace(host)},
			},
		})
		mounts = append(mounts, corev1.VolumeMount{
			Name:      name,
			MountPath: strings.TrimSpace(target),
		})
	}
	return volumes, mounts
}

func defaultKubeconfigPath() string {
	if home, err := os.UserHomeDir(); err == nil {
		return home + string(os.PathSeparator) + ".kube" + string(os.PathSeparator) + "config"
	}
	return ""
}
