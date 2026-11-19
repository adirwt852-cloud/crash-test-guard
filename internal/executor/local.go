//go:build !windows

package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/adirwt852-cloud/crash-test-guard/internal/config"
)

// LocalExecutor manages an agent process on the local machine.
type LocalExecutor struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	cfg     config.AgentConfig
	status  Status
	done    chan struct{}
	waitErr error
	stdout  *os.File
	stderr  *os.File
}

// NewLocalExecutor creates an executor that launches the agent as a local
// process. Agent stdout/stderr are forwarded to the current process.
func NewLocalExecutor() *LocalExecutor {
	return &LocalExecutor{
		status: StatusUnknown,
		stdout: os.Stdout,
		stderr: os.Stderr,
	}
}

// Start launches the agent process.
func (e *LocalExecutor) Start(ctx context.Context, cfg config.AgentConfig) error {
	if len(cfg.Command) == 0 {
		return fmt.Errorf("agent command is empty")
	}

	e.mu.Lock()
	if e.status == StatusRunning {
		e.mu.Unlock()
		return fmt.Errorf("agent is already running")
	}

	cmd := exec.Command(cfg.Command[0], cfg.Command[1:]...)
	cmd.Stdout = e.stdout
	cmd.Stderr = e.stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		e.mu.Unlock()
		return fmt.Errorf("start agent %q: %w", cfg.Command[0], err)
	}

	done := make(chan struct{})
	e.cmd = cmd
	e.cfg = cfg
	e.status = StatusRunning
	e.done = done
	e.waitErr = nil
	e.mu.Unlock()

	go e.wait(cmd, done)
	return nil
}

func (e *LocalExecutor) wait(cmd *exec.Cmd, done chan struct{}) {
	err := cmd.Wait()

	e.mu.Lock()
	if e.cmd == cmd {
		e.waitErr = err
		if e.status == StatusRunning {
			e.status = StatusStopped
		}
	}
	e.mu.Unlock()

	close(done)
}

// Wait blocks until the process exits or ctx is cancelled.
func (e *LocalExecutor) Wait(ctx context.Context) error {
	e.mu.Lock()
	done := e.done
	e.mu.Unlock()

	if done == nil {
		return fmt.Errorf("agent has not been started")
	}

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Kill sends SIGKILL to the agent process group and waits for it to exit.
func (e *LocalExecutor) Kill(ctx context.Context) error {
	e.mu.Lock()
	cmd := e.cmd
	done := e.done
	if cmd == nil || cmd.Process == nil {
		e.mu.Unlock()
		return nil
	}
	e.status = StatusCrashed
	pid := cmd.Process.Pid
	timeout := e.cfg.ShutdownTimeout.Duration
	e.mu.Unlock()

	if err := signalGroup(pid, syscall.SIGKILL); err != nil {
		return fmt.Errorf("kill agent (pid %d): %w", pid, err)
	}

	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		_ = cmd.Process.Kill()
		return fmt.Errorf("agent (pid %d) did not exit within %s", pid, timeout)
	}
}

// Restart kills the running process and starts a new one.
func (e *LocalExecutor) Restart(ctx context.Context) error {
	e.mu.Lock()
	cfg := e.cfg
	e.mu.Unlock()

	if err := e.Kill(ctx); err != nil {
		return err
	}
	return e.Start(ctx, cfg)
}

// Status returns the current process status.
func (e *LocalExecutor) Status(ctx context.Context) (Status, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.status, nil
}

// Cleanup stops the process (if running) and releases its resources.
func (e *LocalExecutor) Cleanup(ctx context.Context) error {
	e.mu.Lock()
	running := e.status == StatusRunning
	e.mu.Unlock()

	if running {
		_ = e.Kill(ctx)
	}

	e.mu.Lock()
	e.cmd = nil
	e.done = nil
	e.status = StatusUnknown
	e.waitErr = nil
	e.mu.Unlock()
	return nil
}

func signalGroup(pid int, sig syscall.Signal) error {
	err := syscall.Kill(-pid, sig)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}
