package integration

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/adirwt852-cloud/crash-test-guard/internal/config"
	"github.com/adirwt852-cloud/crash-test-guard/internal/executor"
	"github.com/adirwt852-cloud/crash-test-guard/internal/orchestrator"
	"github.com/adirwt852-cloud/crash-test-guard/internal/protocol"
)

// dockerAvailable reports whether the docker CLI is present on PATH.
func dockerAvailable() bool {
	_, err := exec.LookPath("docker")
	return err == nil
}

// TestCrashRecovery_ContainerReplace_Docker runs the container_replace scenario
// against the Engine using DockerExecutor. It is skipped when docker is not
// available or when no mock-agent container image is configured.
func TestCrashRecovery_ContainerReplace_Docker(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker not available on PATH")
	}
	image := os.Getenv("CRASH_TEST_GUARD_MOCK_IMAGE")
	if image == "" {
		t.Skip("no mock-agent container image configured (set CRASH_TEST_GUARD_MOCK_IMAGE)")
	}
	endpoint := os.Getenv("CRASH_TEST_GUARD_MOCK_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:8080"
	}

	cfg := &config.Config{
		Version: config.Version,
		Agent: config.AgentConfig{
			Image:           image,
			Command:         []string{"/mock-agent"},
			Protocol:        "http",
			Endpoint:        endpoint,
			Network:         "crash-test-guard-net",
			StartupTimeout:  config.Duration{Duration: 30 * time.Second},
			ShutdownTimeout: config.Duration{Duration: 10 * time.Second},
		},
		CrashScenarios: []config.CrashScenario{{
			Type: "container_replace",
			Trigger: config.Trigger{
				Event:  "step_completed",
				StepID: "process_payment",
				Timing: "after_event",
			},
			Fallback: config.Fallback{
				Timeout:  config.Duration{Duration: 30 * time.Second},
				OnMissed: "fail",
			},
		}},
		Verification: config.VerificationConfig{
			CheckpointConsistency: true,
			DuplicateSideEffects:  true,
			WorkflowCompletion:    true,
			BaselineComparison:    true,
		},
		Reporting: config.ReportingConfig{Format: "human"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}

	ex := executor.NewDockerExecutor(
		executor.WithCrashType(executor.CrashContainerReplace),
	)
	client := protocol.NewClient(protocol.ClientConfig{
		BaseURL:        endpoint,
		ConnectTimeout: 5 * time.Second,
		RequestTimeout: 20 * time.Second,
		MaxRetries:     5,
	})
	engine := orchestrator.NewEngine(cfg, ex, client)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	result, exitCode := engine.Run(ctx)
	if exitCode != orchestrator.ExitPass {
		t.Fatalf("engine exited %d, want %d\n%s", exitCode, orchestrator.ExitPass, result.Summary())
	}
	if result.Overall != orchestrator.StatusPass {
		t.Fatalf("overall=%s, want PASS\n%s", result.Overall, result.Summary())
	}
}
