package executor

import (
	"context"
	"fmt"
	"time"

	"github.com/adirwt852-cloud/crash-test-guard/internal/config"
)

// Status describes the current state of the managed process.
type Status string

const (
	StatusUnknown Status = "unknown"
	StatusRunning Status = "running"
	StatusStopped Status = "stopped"
	StatusCrashed Status = "crashed"
)

// Executor manages the lifecycle of the agent under test.
type Executor interface {
	// Start launches the agent process (or container).
	Start(ctx context.Context, cfg config.AgentConfig) error

	// Wait blocks until the process exits or ctx is cancelled.
	Wait(ctx context.Context) error

	// Kill sends a termination signal to the process.
	Kill(ctx context.Context) error

	// Restart kills and starts the process again.
	Restart(ctx context.Context) error

	// Status returns the current process status.
	Status(ctx context.Context) (Status, error)

	// Cleanup releases all resources (temp files, containers, etc.).
	Cleanup(ctx context.Context) error
}

// ProcessInfo holds details about a running process.
type ProcessInfo struct {
	PID      int
	Started  time.Time
	Command  string
	ExitCode int
}

// ValidateExecutor checks that an Executor implementation is non-nil.
func ValidateExecutor(e Executor) error {
	if e == nil {
		return fmt.Errorf("executor is nil")
	}
	return nil
}
