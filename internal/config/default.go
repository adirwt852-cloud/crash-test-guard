package config

import "time"

// DefaultConfig returns a Config with sensible defaults applied.
func DefaultConfig() *Config {
	return &Config{
		Version: Version,
		Agent: AgentConfig{
			Protocol:        DefaultProtocol,
			StartupTimeout:  Duration{Duration: 30 * time.Second},
			ShutdownTimeout: Duration{Duration: 10 * time.Second},
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
