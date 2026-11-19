package executor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/adirwt852-cloud/crash-test-guard/internal/config"
)

const (
	defaultDockerBinary  = "docker"
	containerNamePrefix  = "crash-test-guard-"
	defaultWorkspacePath = "/workspace"

	labelManaged = "crash-test-guard.managed"
	labelSession = "crash-test-guard.session"
	labelRole    = "crash-test-guard.role"
)

// CrashType selects how the Docker executor injects and recovers from a crash.
type CrashType string

const (
	// CrashContainerRestart kills the container process and starts it again
	// with "docker kill" + "docker start".
	CrashContainerRestart CrashType = "container_restart"

	// CrashContainerReplace destroys the container and launches a new one from
	// the same image with "docker rm -f" + "docker run".
	CrashContainerReplace CrashType = "container_replace"
)

// WorkspaceMount describes a host directory bind-mounted into the container so
// checkpoints survive container replacement.
type WorkspaceMount struct {
	Host      string
	Container string
}

// DockerOption configures a DockerExecutor.
type DockerOption func(*DockerExecutor)

// WithDockerBinary overrides the docker CLI binary path.
func WithDockerBinary(path string) DockerOption {
	return func(e *DockerExecutor) {
		if strings.TrimSpace(path) != "" {
			e.binary = path
		}
	}
}

// WithCrashType sets the crash injection strategy used by Restart.
func WithCrashType(t CrashType) DockerOption {
	return func(e *DockerExecutor) {
		e.crashType = t
	}
}

// WithKillSignal sets the signal used by Kill. SIGKILL uses "docker kill",
// SIGTERM uses "docker stop".
func WithKillSignal(sig syscall.Signal) DockerOption {
	return func(e *DockerExecutor) {
		e.killSignal = sig
	}
}

// WithWorkspace bind-mounts host into the container at container. An empty
// container path defaults to /workspace.
func WithWorkspace(host, container string) DockerOption {
	return func(e *DockerExecutor) {
		e.workspace = &WorkspaceMount{Host: host, Container: container}
	}
}

// WithContainerName overrides the generated container name.
func WithContainerName(name string) DockerOption {
	return func(e *DockerExecutor) {
		if strings.TrimSpace(name) != "" {
			e.name = name
		}
	}
}

// DockerExecutor manages an agent running inside a Docker container.
type DockerExecutor struct {
	mu sync.Mutex

	binary     string
	crashType  CrashType
	killSignal syscall.Signal
	workspace  *WorkspaceMount

	cfg      config.AgentConfig
	name     string
	id       string
	session  string
	network  string
	netOwned bool
	status   Status
	exitCode int
	started  bool
}

var _ Executor = (*DockerExecutor)(nil)

// NewDockerExecutor creates an executor that drives the docker CLI.
func NewDockerExecutor(opts ...DockerOption) *DockerExecutor {
	e := &DockerExecutor{
		binary:     defaultDockerBinary,
		crashType:  CrashContainerRestart,
		killSignal: syscall.SIGKILL,
		session:    randomHex(8),
		name:       generateContainerName(),
		status:     StatusUnknown,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}
	return e
}

// SetCrashType updates the crash strategy. Valid values are
// container_restart and container_replace.
func (e *DockerExecutor) SetCrashType(t CrashType) error {
	switch t {
	case CrashContainerRestart, CrashContainerReplace:
	default:
		return fmt.Errorf("docker: unsupported crash type %q", t)
	}
	e.mu.Lock()
	e.crashType = t
	e.mu.Unlock()
	return nil
}

// ContainerName returns the name of the tracked container.
func (e *DockerExecutor) ContainerName() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.name
}

// ContainerID returns the ID of the tracked container, if started.
func (e *DockerExecutor) ContainerID() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.id
}

// Start launches a new container from cfg.Image.
func (e *DockerExecutor) Start(ctx context.Context, cfg config.AgentConfig) error {
	image := strings.TrimSpace(cfg.Image)
	if image == "" {
		return fmt.Errorf("docker: image is required")
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.status == StatusRunning {
		return fmt.Errorf("docker: agent container is already running")
	}

	e.cfg = cfg
	if e.name == "" {
		e.name = generateContainerName()
	}
	if e.session == "" {
		e.session = randomHex(8)
	}

	// Remove any stale container left behind by a previous run so that the
	// generated name can be reused deterministically.
	_, _ = e.docker(ctx, "rm", "-f", e.name)

	if network := strings.TrimSpace(cfg.Network); network != "" {
		e.network = network
		created, err := e.ensureNetwork(ctx)
		if err != nil {
			return err
		}
		e.netOwned = created
	} else {
		e.network = ""
		e.netOwned = false
	}

	if e.workspace != nil && strings.TrimSpace(e.workspace.Host) != "" {
		if err := os.MkdirAll(e.workspace.Host, 0o755); err != nil {
			return fmt.Errorf("docker: create workspace %q: %w", e.workspace.Host, err)
		}
	}

	args := []string{"run", "-d", "--name", e.name}
	args = append(args,
		"--label", labelManaged+"=true",
		"--label", labelSession+"="+e.session,
		"--label", labelRole+"=agent",
	)
	if e.network != "" {
		args = append(args, "--network", e.network)
	}
	if e.workspace != nil && strings.TrimSpace(e.workspace.Host) != "" {
		target := strings.TrimSpace(e.workspace.Container)
		if target == "" {
			target = defaultWorkspacePath
		}
		args = append(args, "--volume", e.workspace.Host+":"+target)
	}
	for _, v := range cfg.Volumes {
		if v = strings.TrimSpace(v); v != "" {
			args = append(args, "--volume", v)
		}
	}
	for _, kv := range cfg.Env {
		if kv = strings.TrimSpace(kv); kv != "" {
			args = append(args, "--env", kv)
		}
	}
	args = append(args, image)
	args = append(args, cfg.Command...)

	out, err := e.docker(ctx, args...)
	if err != nil {
		e.status = StatusUnknown
		e.id = ""
		e.started = false
		return fmt.Errorf("docker: start container: %w", err)
	}

	id := lastLine(out)
	if id == "" {
		e.status = StatusUnknown
		e.started = false
		return fmt.Errorf("docker: start container: empty container id")
	}

	e.id = id
	e.status = StatusRunning
	e.exitCode = 0
	e.started = true
	return nil
}

// Wait blocks until the container exits or ctx is cancelled.
func (e *DockerExecutor) Wait(ctx context.Context) error {
	e.mu.Lock()
	target := e.id
	started := e.started
	e.mu.Unlock()

	if !started || target == "" {
		return fmt.Errorf("docker: agent container has not been started")
	}

	out, err := dockerCommand(ctx, e.binary, "wait", target)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("docker: wait for container: %w", err)
	}

	code := 0
	if s := strings.TrimSpace(out); s != "" {
		if n, perr := strconv.Atoi(s); perr == nil {
			code = n
		}
	}

	e.mu.Lock()
	if e.id == target {
		e.exitCode = code
		if code == 0 {
			e.status = StatusStopped
		} else {
			e.status = StatusCrashed
		}
	}
	e.mu.Unlock()
	return nil
}

// Kill terminates the container. SIGKILL maps to "docker kill"; SIGTERM maps to
// "docker stop" using the configured shutdown timeout.
func (e *DockerExecutor) Kill(ctx context.Context) error {
	e.mu.Lock()
	target := e.id
	started := e.started
	sig := e.killSignal
	timeout := e.cfg.ShutdownTimeout.Duration
	e.mu.Unlock()

	if !started || target == "" {
		return nil
	}
	if sig == 0 {
		sig = syscall.SIGKILL
	}

	current, _, err := e.inspect(ctx, target)
	if err == nil && (current == StatusStopped || current == StatusCrashed) {
		e.mu.Lock()
		e.status = current
		e.mu.Unlock()
		return nil
	}

	final := StatusCrashed
	if sig == syscall.SIGTERM {
		seconds := int(timeout.Seconds())
		if seconds <= 0 {
			seconds = 10
		}
		_, err = dockerCommand(ctx, e.binary, "stop", "--time", strconv.Itoa(seconds), target)
		final = StatusStopped
	} else {
		_, err = dockerCommand(ctx, e.binary, "kill", "--signal", signalName(sig), target)
	}

	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !isContainerGone(err) {
			return fmt.Errorf("docker: kill container: %w", err)
		}
	}
	e.setKilled(final)
	return nil
}

func (e *DockerExecutor) setKilled(status Status) {
	e.mu.Lock()
	e.status = status
	e.mu.Unlock()
}

// Restart starts the existing container again, or replaces it when the
// executor is configured for container_replace.
func (e *DockerExecutor) Restart(ctx context.Context) error {
	e.mu.Lock()
	crash := e.crashType
	e.mu.Unlock()

	if crash == CrashContainerReplace {
		return e.Replace(ctx)
	}
	return e.startExisting(ctx)
}

// Replace removes the container and launches a fresh one from the same image.
func (e *DockerExecutor) Replace(ctx context.Context) error {
	e.mu.Lock()
	target := e.id
	if target == "" {
		target = e.name
	}
	cfg := e.cfg
	e.mu.Unlock()

	if target != "" {
		if _, err := dockerCommand(ctx, e.binary, "rm", "-f", target); err != nil && !isContainerGone(err) {
			return fmt.Errorf("docker: remove container: %w", err)
		}
	}

	e.mu.Lock()
	e.id = ""
	e.status = StatusUnknown
	e.started = false
	e.mu.Unlock()

	return e.Start(ctx, cfg)
}

func (e *DockerExecutor) startExisting(ctx context.Context) error {
	e.mu.Lock()
	target := e.id
	if target == "" {
		target = e.name
	}
	started := e.started
	cfg := e.cfg
	e.mu.Unlock()

	if !started || target == "" {
		return fmt.Errorf("docker: agent container has not been started")
	}

	if _, err := dockerCommand(ctx, e.binary, "start", target); err != nil {
		if isContainerGone(err) {
			return e.Start(ctx, cfg)
		}
		return fmt.Errorf("docker: restart container: %w", err)
	}

	e.mu.Lock()
	e.status = StatusRunning
	e.exitCode = 0
	e.mu.Unlock()
	return nil
}

// Status reports the container state via "docker inspect".
func (e *DockerExecutor) Status(ctx context.Context) (Status, error) {
	e.mu.Lock()
	target := e.id
	started := e.started
	stored := e.status
	e.mu.Unlock()

	if !started || target == "" {
		return StatusUnknown, nil
	}

	status, code, err := e.inspect(ctx, target)
	if err != nil {
		if isContainerGone(err) {
			e.mu.Lock()
			e.status = StatusUnknown
			e.mu.Unlock()
			return StatusUnknown, nil
		}
		return stored, fmt.Errorf("docker: inspect container: %w", err)
	}

	e.mu.Lock()
	e.status = status
	e.exitCode = code
	e.mu.Unlock()
	return status, nil
}

// Cleanup removes the container, any leftover containers from this session,
// and the network if the executor created it.
func (e *DockerExecutor) Cleanup(ctx context.Context) error {
	e.mu.Lock()
	target := e.id
	if target == "" {
		target = e.name
	}
	session := e.session
	network := e.network
	netOwned := e.netOwned
	e.mu.Unlock()

	if target != "" {
		_, _ = dockerCommand(ctx, e.binary, "rm", "-f", target)
	}

	if session != "" {
		out, err := dockerCommand(ctx, e.binary, "ps", "-aq",
			"--filter", "label="+labelManaged+"=true",
			"--filter", "label="+labelSession+"="+session,
		)
		if err == nil {
			for _, id := range strings.Fields(out) {
				_, _ = dockerCommand(ctx, e.binary, "rm", "-f", id)
			}
		}
	}

	if netOwned && network != "" {
		_, _ = dockerCommand(ctx, e.binary, "network", "rm", network)
	}

	e.mu.Lock()
	e.id = ""
	e.name = ""
	e.status = StatusUnknown
	e.exitCode = 0
	e.started = false
	e.netOwned = false
	e.mu.Unlock()
	return nil
}

func (e *DockerExecutor) ensureNetwork(ctx context.Context) (bool, error) {
	if _, err := e.docker(ctx, "network", "inspect", "--format", "{{.Id}}", e.network); err == nil {
		return false, nil
	}

	_, err := e.docker(ctx, "network", "create",
		"--label", labelManaged+"=true",
		"--label", labelSession+"="+e.session,
		e.network,
	)
	if err != nil {
		return false, fmt.Errorf("docker: create network %q: %w", e.network, err)
	}
	return true, nil
}

func (e *DockerExecutor) inspect(ctx context.Context, target string) (Status, int, error) {
	out, err := e.docker(ctx, "inspect", "--format", "{{.State.Status}}|{{.State.ExitCode}}", target)
	if err != nil {
		return StatusUnknown, -1, err
	}

	state, codeStr, ok := strings.Cut(strings.TrimSpace(out), "|")
	if !ok {
		return StatusUnknown, -1, fmt.Errorf("unexpected inspect output %q", out)
	}
	code, _ := strconv.Atoi(strings.TrimSpace(codeStr))
	return mapContainerState(strings.TrimSpace(state), code), code, nil
}

func (e *DockerExecutor) docker(ctx context.Context, args ...string) (string, error) {
	return dockerCommand(ctx, e.binary, args...)
}

func dockerCommand(ctx context.Context, binary string, args ...string) (string, error) {
	if strings.TrimSpace(binary) == "" {
		binary = defaultDockerBinary
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return text, ctxErr
		}
		if text == "" {
			return "", fmt.Errorf("docker %s: %w", strings.Join(args, " "), err)
		}
		return text, fmt.Errorf("docker %s: %s: %w", strings.Join(args, " "), text, err)
	}
	return text, nil
}

func mapContainerState(state string, exitCode int) Status {
	switch state {
	case "running", "restarting", "paused":
		return StatusRunning
	case "exited", "dead":
		if exitCode != 0 {
			return StatusCrashed
		}
		return StatusStopped
	case "created", "removing":
		return StatusStopped
	default:
		return StatusUnknown
	}
}

func isContainerGone(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"no such container",
		"no such object",
		"is not running",
		"already stopped",
		"does not exist",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func signalName(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGKILL:
		return "KILL"
	case syscall.SIGTERM:
		return "TERM"
	case syscall.SIGINT:
		return "INT"
	case syscall.SIGHUP:
		return "HUP"
	default:
		return strconv.Itoa(int(sig))
	}
}

func generateContainerName() string {
	return containerNamePrefix + randomHex(6)
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}
