package config

import (
	"os"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp("", "config-*.yaml")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close temp file: %v", err)
	}
	t.Cleanup(func() { os.Remove(f.Name()) })
	return f.Name()
}

func validConfigYAML() string {
	return `version: "1.0"
agent:
  command: ["/bin/echo", "hello"]
  protocol: "http"
  endpoint: "http://localhost:8080"
  startup_timeout: "30s"
  shutdown_timeout: "10s"
crash_scenarios:
  - type: "process_kill"
    trigger:
      event: "step_complete"
      step_id: "step-1"
      timing: "after_event"
    fallback:
      timeout: "5s"
      on_missed: "fail"
verification:
  checkpoint_consistency: true
  duplicate_side_effects: true
  workflow_completion: true
  baseline_comparison: true
reporting:
  format: "human"
  output: ""
  verbose: false
deployment:
  fail_on_error: true
`
}

func validTestConfig() *Config {
	return &Config{
		Version: Version,
		Agent: AgentConfig{
			Command:         []string{"/bin/echo", "test"},
			Protocol:        DefaultProtocol,
			Endpoint:        "http://localhost:8080",
			StartupTimeout:  Duration{Duration: 30 * time.Second},
			ShutdownTimeout: Duration{Duration: 10 * time.Second},
		},
		CrashScenarios: []CrashScenario{
			{
				Type: "process_kill",
				Trigger: Trigger{
					Event:  "step_complete",
					StepID: "step-1",
					Timing: "after_event",
				},
				Fallback: Fallback{
					Timeout:  Duration{Duration: 5 * time.Second},
					OnMissed: "fail",
				},
			},
		},
		Verification: VerificationConfig{
			CheckpointConsistency: true,
			DuplicateSideEffects:  true,
			WorkflowCompletion:    true,
			BaselineComparison:    true,
		},
		Reporting: ReportingConfig{
			Format:  "human",
			Output:  "",
			Verbose: false,
		},
		Deployment: DeploymentConfig{
			FailOnError: true,
		},
	}
}

func TestLoad_ValidConfigFile(t *testing.T) {
	path := writeTempConfig(t, validConfigYAML())
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg == nil {
		t.Fatal("Load() returned nil config")
	}
	if cfg.Version != Version {
		t.Errorf("Version = %q, want %q", cfg.Version, Version)
	}
	if len(cfg.Agent.Command) == 0 {
		t.Error("Agent.Command is empty")
	}
	if cfg.Agent.Endpoint != "http://localhost:8080" {
		t.Errorf("Agent.Endpoint = %q, want %q", cfg.Agent.Endpoint, "http://localhost:8080")
	}
	if len(cfg.CrashScenarios) != 1 {
		t.Errorf("CrashScenarios len = %d, want 1", len(cfg.CrashScenarios))
	}
	if cfg.SourcePath() != path {
		t.Errorf("SourcePath() = %q, want %q", cfg.SourcePath(), path)
	}
}

func TestLoad_MissingFileReturnsError(t *testing.T) {
	_, err := Load("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("Load() expected error for missing file, got nil")
	}
	if err.Error() == "" {
		t.Error("Load() returned empty error")
	}
}

func TestLoad_InvalidYAMLReturnsError(t *testing.T) {
	invalidYAML := "version: 1.0\nagent:\n  command: [\ninvalid: yaml: ["
	path := writeTempConfig(t, invalidYAML)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() expected error for invalid YAML, got nil")
	}
}

func TestValidate_VersionRequired(t *testing.T) {
	cfg := validTestConfig()
	cfg.Version = ""
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() expected error for empty version, got nil")
	}
	if err.Error() != "version is required" {
		t.Errorf("Validate() error = %q, want %q", err.Error(), "version is required")
	}
}

func TestValidate_VersionMustBe1_0(t *testing.T) {
	cfg := validTestConfig()
	cfg.Version = "2.0"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() expected error for unsupported version, got nil")
	}
	expected := "unsupported config version \"2.0\", expected \"1.0\""
	if err.Error() != expected {
		t.Errorf("Validate() error = %q, want %q", err.Error(), expected)
	}
}

func TestValidate_AgentCommandRequired(t *testing.T) {
	cfg := validTestConfig()
	cfg.Agent.Command = nil
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() expected error for missing agent.command, got nil")
	}
	if err.Error() != "agent: command is required" {
		t.Errorf("Validate() error = %q, want %q", err.Error(), "agent: command is required")
	}
}

func TestValidate_AgentEndpointRequired(t *testing.T) {
	cfg := validTestConfig()
	cfg.Agent.Endpoint = ""
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() expected error for missing agent.endpoint, got nil")
	}
	if err.Error() != "agent: endpoint is required" {
		t.Errorf("Validate() error = %q, want %q", err.Error(), "agent: endpoint is required")
	}
}

func TestValidate_AtLeastOneCrashScenarioRequired(t *testing.T) {
	cfg := validTestConfig()
	cfg.CrashScenarios = nil
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() expected error for empty crash_scenarios, got nil")
	}
	if err.Error() != "at least one crash_scenario is required" {
		t.Errorf("Validate() error = %q, want %q", err.Error(), "at least one crash_scenario is required")
	}
}

func TestValidate_CrashScenarioTypeValidation(t *testing.T) {
	validTypes := []string{"process_kill", "process_term", "container_restart", "container_replace"}
	for _, vt := range validTypes {
		t.Run(vt, func(t *testing.T) {
			cfg := validTestConfig()
			cfg.CrashScenarios = []CrashScenario{
				{Type: vt, Trigger: Trigger{Event: "e", StepID: "s", Timing: "after_event"}, Fallback: Fallback{Timeout: Duration{Duration: 5 * time.Second}, OnMissed: "fail"}},
			}
			if err := cfg.Validate(); err != nil {
				t.Errorf("Validate() returned error for valid type %q: %v", vt, err)
			}
		})
	}

	invalidTypes := []string{"invalid_type", "crash", "", "process_crash"}
	for _, it := range invalidTypes {
		t.Run(it, func(t *testing.T) {
			cfg := validTestConfig()
			cfg.CrashScenarios = []CrashScenario{
				{Type: it, Trigger: Trigger{Event: "e", StepID: "s", Timing: "after_event"}, Fallback: Fallback{Timeout: Duration{Duration: 5 * time.Second}, OnMissed: "fail"}},
			}
			err := cfg.Validate()
			if err == nil {
				t.Errorf("Validate() expected error for invalid type %q, got nil", it)
			}
		})
	}
}

func TestValidate_TriggerEventAndStepIDRequired(t *testing.T) {
	tests := []struct {
		name       string
		trigger    Trigger
		wantError  string
	}{
		{
			name:      "missing event",
			trigger:   Trigger{StepID: "s1", Timing: "after_event"},
			wantError: "crash_scenarios[0]: trigger.event is required",
		},
		{
			name:      "missing step_id",
			trigger:   Trigger{Event: "e1", Timing: "after_event"},
			wantError: "crash_scenarios[0]: trigger.step_id is required",
		},
		{
			name:      "both missing",
			trigger:   Trigger{Timing: "after_event"},
			wantError: "crash_scenarios[0]: trigger.event is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validTestConfig()
			cfg.CrashScenarios = []CrashScenario{
				{Type: "process_kill", Trigger: tt.trigger, Fallback: Fallback{Timeout: Duration{Duration: 5 * time.Second}, OnMissed: "fail"}},
			}
			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate() expected error, got nil")
			}
			if err.Error() != tt.wantError {
				t.Errorf("Validate() error = %q, want %q", err.Error(), tt.wantError)
			}
		})
	}
}

func TestValidate_TriggerTimingMustBeBeforeOrAfterEvent(t *testing.T) {
	validTimings := []string{"before_event", "after_event"}
	for _, vt := range validTimings {
		t.Run(vt, func(t *testing.T) {
			cfg := validTestConfig()
			cfg.CrashScenarios = []CrashScenario{
				{Type: "process_kill", Trigger: Trigger{Event: "e", StepID: "s", Timing: vt}, Fallback: Fallback{Timeout: Duration{Duration: 5 * time.Second}, OnMissed: "fail"}},
			}
			if err := cfg.Validate(); err != nil {
				t.Errorf("Validate() returned error for valid timing %q: %v", vt, err)
			}
		})
	}

	// Empty string gets default "after_event" so it's valid
	invalidTimings := []string{"during_event", "before", "after", "invalid"}
	for _, it := range invalidTimings {
		t.Run(it, func(t *testing.T) {
			cfg := validTestConfig()
			cfg.CrashScenarios = []CrashScenario{
				{Type: "process_kill", Trigger: Trigger{Event: "e", StepID: "s", Timing: it}, Fallback: Fallback{Timeout: Duration{Duration: 5 * time.Second}, OnMissed: "fail"}},
			}
			err := cfg.Validate()
			if err == nil {
				t.Errorf("Validate() expected error for invalid timing %q, got nil", it)
			}
			if err != nil {
				expected := "crash_scenarios[0]: trigger.timing must be before_event or after_event"
				if len(err.Error()) < len(expected) || err.Error()[:len(expected)] != expected {
					t.Errorf("Validate() error = %q, want prefix %q", err.Error(), expected)
				}
			}
		})
	}
}

func TestValidate_FallbackOnMissedMustBeFailSkipContinue(t *testing.T) {
	validOnMissed := []string{"fail", "skip", "continue"}
	for _, v := range validOnMissed {
		t.Run(v, func(t *testing.T) {
			cfg := validTestConfig()
			cfg.CrashScenarios = []CrashScenario{
				{Type: "process_kill", Trigger: Trigger{Event: "e", StepID: "s", Timing: "after_event"}, Fallback: Fallback{Timeout: Duration{Duration: 5 * time.Second}, OnMissed: v}},
			}
			if err := cfg.Validate(); err != nil {
				t.Errorf("Validate() returned error for valid on_missed %q: %v", v, err)
			}
		})
	}

	// Empty string gets default "fail" so it's valid
	invalidOnMissed := []string{"ignore", "retry", "abort"}
	for _, iv := range invalidOnMissed {
		t.Run(iv, func(t *testing.T) {
			cfg := validTestConfig()
			cfg.CrashScenarios = []CrashScenario{
				{Type: "process_kill", Trigger: Trigger{Event: "e", StepID: "s", Timing: "after_event"}, Fallback: Fallback{Timeout: Duration{Duration: 5 * time.Second}, OnMissed: iv}},
			}
			err := cfg.Validate()
			if err == nil {
				t.Errorf("Validate() expected error for invalid on_missed %q, got nil", iv)
			}
			if err != nil {
				expected := "crash_scenarios[0]: fallback.on_missed must be fail, skip, or continue"
				if err.Error() != expected {
					t.Errorf("Validate() error = %q, want %q", err.Error(), expected)
				}
			}
		})
	}
}

func TestValidate_ReportingFormatMustBeJunitJsonHuman(t *testing.T) {
	validFormats := []string{"junit", "json", "human"}
	for _, v := range validFormats {
		t.Run(v, func(t *testing.T) {
			cfg := validTestConfig()
			cfg.Reporting.Format = v
			if err := cfg.Validate(); err != nil {
				t.Errorf("Validate() returned error for valid format %q: %v", v, err)
			}
		})
	}

	// Empty string gets default "human" so it's valid
	invalidFormats := []string{"xml", "csv", "yaml", "text"}
	for _, iv := range invalidFormats {
		t.Run(iv, func(t *testing.T) {
			cfg := validTestConfig()
			cfg.Reporting.Format = iv
			err := cfg.Validate()
			if err == nil {
				t.Errorf("Validate() expected error for invalid format %q, got nil", iv)
			}
			if err != nil {
				expected := "reporting: unsupported reporting format"
				if len(err.Error()) < len(expected) || err.Error()[:len(expected)] != expected {
					t.Errorf("Validate() error = %q, want prefix %q", err.Error(), expected)
				}
			}
		})
	}
}

func TestDefaultConfig_ReturnsSensibleDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg == nil {
		t.Fatal("DefaultConfig() returned nil")
	}
	if cfg.Version != Version {
		t.Errorf("Version = %q, want %q", cfg.Version, Version)
	}
	if cfg.Agent.Protocol != DefaultProtocol {
		t.Errorf("Agent.Protocol = %q, want %q", cfg.Agent.Protocol, DefaultProtocol)
	}
	if cfg.Agent.StartupTimeout.Duration != 30*time.Second {
		t.Errorf("Agent.StartupTimeout = %v, want 30s", cfg.Agent.StartupTimeout.Duration)
	}
	if cfg.Agent.ShutdownTimeout.Duration != 10*time.Second {
		t.Errorf("Agent.ShutdownTimeout = %v, want 10s", cfg.Agent.ShutdownTimeout.Duration)
	}
	if !cfg.Verification.CheckpointConsistency {
		t.Error("Verification.CheckpointConsistency default false, want true")
	}
	if !cfg.Verification.DuplicateSideEffects {
		t.Error("Verification.DuplicateSideEffects default false, want true")
	}
	if !cfg.Verification.WorkflowCompletion {
		t.Error("Verification.WorkflowCompletion default false, want true")
	}
	if !cfg.Verification.BaselineComparison {
		t.Error("Verification.BaselineComparison default false, want true")
	}
	if cfg.Reporting.Format != "human" {
		t.Errorf("Reporting.Format = %q, want %q", cfg.Reporting.Format, "human")
	}
	if cfg.Reporting.Verbose != false {
		t.Error("Reporting.Verbose default true, want false")
	}
	if cfg.Deployment.FailOnError != true {
		t.Error("Deployment.FailOnError default false, want true")
	}
}

func TestDuration_UnmarshalYAML(t *testing.T) {
	tests := []struct {
		name     string
		yamlStr  string
		wantDur  time.Duration
		wantErr  bool
	}{
		{"30s", "30s", 30 * time.Second, false},
		{"10m", "10m", 10 * time.Minute, false},
		{"1h", "1h", 1 * time.Hour, false},
		{"1h30m", "1h30m", 90 * time.Minute, false},
		{"500ms", "500ms", 500 * time.Millisecond, false},
		{"1.5s", "1.5s", 1500 * time.Millisecond, false},
		{"invalid", "not-a-duration", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d Duration
			node := &yaml.Node{Kind: yaml.ScalarNode, Value: tt.yamlStr}
			err := d.UnmarshalYAML(node)
			if tt.wantErr {
				if err == nil {
					t.Fatal("UnmarshalYAML() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("UnmarshalYAML() returned error: %v", err)
			}
			if d.Duration != tt.wantDur {
				t.Errorf("Duration = %v, want %v", d.Duration, tt.wantDur)
			}
		})
	}
}

func TestLoad_DurationUnmarshalingFromConfig(t *testing.T) {
	yamlContent := `version: "1.0"
agent:
  command: ["/bin/echo", "test"]
  endpoint: "http://localhost:8080"
  startup_timeout: "45s"
  shutdown_timeout: "5m"
crash_scenarios:
  - type: "process_kill"
    trigger:
      event: "step_complete"
      step_id: "step-1"
      timing: "after_event"
    fallback:
      timeout: "10s"
      on_missed: "fail"
verification:
  checkpoint_consistency: true
  duplicate_side_effects: true
  workflow_completion: true
  baseline_comparison: true
reporting:
  format: "human"
  output: ""
  verbose: false
deployment:
  fail_on_error: true
`
	path := writeTempConfig(t, yamlContent)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.Agent.StartupTimeout.Duration != 45*time.Second {
		t.Errorf("Agent.StartupTimeout = %v, want 45s", cfg.Agent.StartupTimeout.Duration)
	}
	if cfg.Agent.ShutdownTimeout.Duration != 5*time.Minute {
		t.Errorf("Agent.ShutdownTimeout = %v, want 5m", cfg.Agent.ShutdownTimeout.Duration)
	}
	if len(cfg.CrashScenarios) != 1 {
		t.Fatalf("expected 1 crash scenario, got %d", len(cfg.CrashScenarios))
	}
	if cfg.CrashScenarios[0].Fallback.Timeout.Duration != 10*time.Second {
		t.Errorf("Fallback.Timeout = %v, want 10s", cfg.CrashScenarios[0].Fallback.Timeout.Duration)
	}
}
