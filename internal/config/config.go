package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const Version = "1.0"
const DefaultProtocol = "http"

// Config is the top-level configuration loaded from crash-test.yaml.
type Config struct {
	Version        string             `yaml:"version"`
	Agent          AgentConfig        `yaml:"agent"`
	CrashScenarios []CrashScenario    `yaml:"crash_scenarios"`
	Verification   VerificationConfig `yaml:"verification"`
	Reporting      ReportingConfig    `yaml:"reporting"`
	Deployment     DeploymentConfig   `yaml:"deployment"`

	sourcePath string
}

// AgentConfig describes the agent under test.
type AgentConfig struct {
	Command         []string `yaml:"command"`
	Protocol        string   `yaml:"protocol"`
	Endpoint        string   `yaml:"endpoint"`
	StartupTimeout  Duration `yaml:"startup_timeout"`
	ShutdownTimeout Duration `yaml:"shutdown_timeout"`

	// Container settings used by the Docker executor.
	Image   string   `yaml:"image"`
	Network string   `yaml:"network"`
	Volumes []string `yaml:"volumes"`
	Env     []string `yaml:"env"`

	// Kubernetes settings used by the Kubernetes executor.
	Namespace      string `yaml:"namespace"`
	LabelSelector  string `yaml:"label_selector"`
	PodName        string `yaml:"pod_name"`
	ServiceAccount string `yaml:"service_account"`
	Kubeconfig     string `yaml:"kubeconfig"`
}

// KubernetesEnabled reports whether the agent is configured to run on
// Kubernetes rather than locally or in Docker.
func (a *AgentConfig) KubernetesEnabled() bool {
	return strings.TrimSpace(a.Namespace) != "" ||
		strings.TrimSpace(a.LabelSelector) != "" ||
		strings.TrimSpace(a.PodName) != "" ||
		strings.TrimSpace(a.ServiceAccount) != "" ||
		strings.TrimSpace(a.Kubeconfig) != ""
}

// CrashScenario defines one crash test scenario.
type CrashScenario struct {
	Type     string   `yaml:"type"`
	Trigger  Trigger  `yaml:"trigger"`
	Fallback Fallback `yaml:"fallback"`
}

// Trigger defines when and how to inject the crash.
type Trigger struct {
	Event  string `yaml:"event"`
	StepID string `yaml:"step_id"`
	Timing string `yaml:"timing"` // "before_event" | "after_event"
}

// Fallback defines behavior if the trigger event is missed.
type Fallback struct {
	Timeout  Duration `yaml:"timeout"`
	OnMissed string   `yaml:"on_missed"` // "fail" | "skip" | "continue"
}

// VerificationConfig controls what checks are performed after recovery.
type VerificationConfig struct {
	CheckpointConsistency bool `yaml:"checkpoint_consistency"`
	DuplicateSideEffects  bool `yaml:"duplicate_side_effects"`
	WorkflowCompletion    bool `yaml:"workflow_completion"`
	BaselineComparison    bool `yaml:"baseline_comparison"`
}

// ReportingConfig controls output format and destination.
type ReportingConfig struct {
	Format  string `yaml:"format"` // junit | json | human
	Output  string `yaml:"output"`
	Verbose bool   `yaml:"verbose"`
}

// DeploymentConfig controls CI/CD gating behavior.
type DeploymentConfig struct {
	FailOnError bool `yaml:"fail_on_error"`
}

// Duration wraps time.Duration for YAML unmarshaling from strings like "30s".
type Duration struct {
	time.Duration
}

// UnmarshalYAML implements yaml.Unmarshaler for Duration.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = dur
	return nil
}

// Load reads a YAML config file, applies defaults, and validates it.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	cfg := DefaultConfig()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing YAML: %w", err)
	}

	cfg.sourcePath = path

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	return cfg, nil
}

// Validate checks all required fields and cross-field constraints.
func (c *Config) Validate() error {
	if strings.TrimSpace(c.Version) == "" {
		return fmt.Errorf("version is required")
	}
	if c.Version != Version {
		return fmt.Errorf("unsupported config version %q, expected %q", c.Version, Version)
	}

	if err := c.Agent.Validate(); err != nil {
		return fmt.Errorf("agent: %w", err)
	}

	if len(c.CrashScenarios) == 0 {
		return fmt.Errorf("at least one crash_scenario is required")
	}
	for i, sc := range c.CrashScenarios {
		if err := sc.Validate(i); err != nil {
			return err
		}
	}

	if err := c.Reporting.Validate(); err != nil {
		return fmt.Errorf("reporting: %w", err)
	}

	return nil
}

// Validate checks the agent configuration.
func (a *AgentConfig) Validate() error {
	if len(a.Command) == 0 && strings.TrimSpace(a.Image) == "" && !a.KubernetesEnabled() {
		return fmt.Errorf("command is required")
	}
	if a.KubernetesEnabled() && strings.TrimSpace(a.PodName) == "" && strings.TrimSpace(a.LabelSelector) == "" && strings.TrimSpace(a.Image) == "" {
		return fmt.Errorf("kubernetes agent requires pod_name, label_selector, or image")
	}
	if a.Protocol == "" {
		a.Protocol = DefaultProtocol
	}
	if a.Protocol != DefaultProtocol {
		return fmt.Errorf("unsupported protocol %q, only %q is supported in v1", a.Protocol, DefaultProtocol)
	}
	if a.Endpoint == "" {
		return fmt.Errorf("endpoint is required")
	}
	return nil
}

// Validate checks a single crash scenario.
func (s *CrashScenario) Validate(index int) error {
	if s.Type == "" {
		return fmt.Errorf("crash_scenarios[%d]: type is required", index)
	}
	switch s.Type {
	case "process_kill", "process_term", "container_restart", "container_replace", "pod_delete", "pod_replace":
		// valid
	default:
		return fmt.Errorf("crash_scenarios[%d]: unsupported type %q", index, s.Type)
	}

	if err := s.Trigger.Validate(index); err != nil {
		return err
	}
	if err := s.Fallback.Validate(index); err != nil {
		return err
	}

	return nil
}

// Validate checks the trigger configuration.
func (t *Trigger) Validate(scenarioIndex int) error {
	if t.Event == "" {
		return fmt.Errorf("crash_scenarios[%d]: trigger.event is required", scenarioIndex)
	}
	if t.StepID == "" {
		return fmt.Errorf("crash_scenarios[%d]: trigger.step_id is required", scenarioIndex)
	}
	if t.Timing == "" {
		t.Timing = "after_event"
	}
	if t.Timing != "before_event" && t.Timing != "after_event" {
		return fmt.Errorf("crash_scenarios[%d]: trigger.timing must be before_event or after_event, got %q", scenarioIndex, t.Timing)
	}
	return nil
}

// Validate checks the fallback configuration.
func (f *Fallback) Validate(scenarioIndex int) error {
	if f.OnMissed == "" {
		f.OnMissed = "fail"
	}
	if f.OnMissed != "fail" && f.OnMissed != "skip" && f.OnMissed != "continue" {
		return fmt.Errorf("crash_scenarios[%d]: fallback.on_missed must be fail, skip, or continue", scenarioIndex)
	}
	return nil
}

// Validate checks the reporting configuration.
func (r *ReportingConfig) Validate() error {
	if r.Format == "" {
		r.Format = "human"
	}
	if r.Format != "junit" && r.Format != "json" && r.Format != "human" {
		return fmt.Errorf("unsupported reporting format %q", r.Format)
	}
	return nil
}

// SourcePath returns the path the config was loaded from.
func (c *Config) SourcePath() string {
	return c.sourcePath
}
