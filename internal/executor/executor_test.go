package executor

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/adirwt852-cloud/crash-test-guard/internal/config"
)

func TestNewLocalExecutor(t *testing.T) {
	e := NewLocalExecutor()
	if e == nil {
		t.Fatal("NewLocalExecutor returned nil")
	}

	status, err := e.Status(context.Background())
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if status != StatusUnknown {
		t.Errorf("expected StatusUnknown, got %s", status)
	}
}

func TestStartEmptyCommand(t *testing.T) {
	e := NewLocalExecutor()
	cfg := config.AgentConfig{
		Command:         []string{},
		Protocol:        "http",
		Endpoint:        "http://localhost:8080",
		ShutdownTimeout: config.Duration{Duration: 5 * time.Second},
	}

	err := e.Start(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error for empty command, got nil")
	}
}

func TestStartSetsStatusRunning(t *testing.T) {
	e := NewLocalExecutor()
	cfg := config.AgentConfig{
		Command:         []string{"sleep", "10"},
		Protocol:        "http",
		Endpoint:        "http://localhost:8080",
		ShutdownTimeout: config.Duration{Duration: 5 * time.Second},
	}

	err := e.Start(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	status, err := e.Status(context.Background())
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if status != StatusRunning {
		t.Errorf("expected StatusRunning, got %s", status)
	}

	e.Cleanup(context.Background())
}

func TestKillNonRunningProcess(t *testing.T) {
	e := NewLocalExecutor()

	err := e.Kill(context.Background())
	if err != nil {
		t.Errorf("expected nil error for non-running process, got %v", err)
	}

	err = e.Kill(context.Background())
	if err != nil {
		t.Errorf("expected nil error for second Kill on non-running process, got %v", err)
	}
}

func TestKillRunningProcess(t *testing.T) {
	e := NewLocalExecutor()
	cfg := config.AgentConfig{
		Command:         []string{"sleep", "10"},
		Protocol:        "http",
		Endpoint:        "http://localhost:8080",
		ShutdownTimeout: config.Duration{Duration: 5 * time.Second},
	}

	err := e.Start(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	err = e.Kill(context.Background())
	if err != nil {
		t.Errorf("Kill failed: %v", err)
	}

	status, err := e.Status(context.Background())
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if status != StatusStopped && status != StatusCrashed {
		t.Errorf("expected StatusStopped or StatusCrashed, got %s", status)
	}
}

func TestRestart(t *testing.T) {
	e := NewLocalExecutor()
	cfg := config.AgentConfig{
		Command:         []string{"sleep", "10"},
		Protocol:        "http",
		Endpoint:        "http://localhost:8080",
		ShutdownTimeout: config.Duration{Duration: 5 * time.Second},
	}

	err := e.Start(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	statusBefore, _ := e.Status(context.Background())
	if statusBefore != StatusRunning {
		t.Errorf("expected StatusRunning before restart, got %s", statusBefore)
	}

	err = e.Restart(context.Background())
	if err != nil {
		t.Errorf("Restart failed: %v", err)
	}

	statusAfter, err := e.Status(context.Background())
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if statusAfter != StatusRunning {
		t.Errorf("expected StatusRunning after restart, got %s", statusAfter)
	}

	e.Cleanup(context.Background())
}

func TestStatusReturnsCorrectStatus(t *testing.T) {
	e := NewLocalExecutor()

	status, err := e.Status(context.Background())
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if status != StatusUnknown {
		t.Errorf("expected StatusUnknown initially, got %s", status)
	}

	cfg := config.AgentConfig{
		Command:         []string{"sleep", "10"},
		Protocol:        "http",
		Endpoint:        "http://localhost:8080",
		ShutdownTimeout: config.Duration{Duration: 5 * time.Second},
	}

	err = e.Start(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	status, err = e.Status(context.Background())
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if status != StatusRunning {
		t.Errorf("expected StatusRunning after Start, got %s", status)
	}

	e.Cleanup(context.Background())

	status, err = e.Status(context.Background())
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if status != StatusUnknown {
		t.Errorf("expected StatusUnknown after Cleanup, got %s", status)
	}
}

func TestCleanupStopsProcessAndReleasesResources(t *testing.T) {
	e := NewLocalExecutor()
	cfg := config.AgentConfig{
		Command:         []string{"sleep", "10"},
		Protocol:        "http",
		Endpoint:        "http://localhost:8080",
		ShutdownTimeout: config.Duration{Duration: 5 * time.Second},
	}

	err := e.Start(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	err = e.Cleanup(context.Background())
	if err != nil {
		t.Errorf("Cleanup failed: %v", err)
	}

	status, err := e.Status(context.Background())
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if status != StatusUnknown {
		t.Errorf("expected StatusUnknown after Cleanup, got %s", status)
	}
}

func TestSignalGroup(t *testing.T) {
	pid := os.Getpid()

	err := signalGroup(pid, syscall.SIGKILL)
	if err != nil {
		if err != syscall.ESRCH {
			t.Errorf("unexpected error from signalGroup: %v", err)
		}
	}
}

func TestStartAlreadyRunning(t *testing.T) {
	e := NewLocalExecutor()
	cfg := config.AgentConfig{
		Command:         []string{"sleep", "10"},
		Protocol:        "http",
		Endpoint:        "http://localhost:8080",
		ShutdownTimeout: config.Duration{Duration: 5 * time.Second},
	}

	err := e.Start(context.Background(), cfg)
	if err != nil {
		t.Fatalf("First Start failed: %v", err)
	}

	err = e.Start(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error for starting already running process")
	}

	e.Cleanup(context.Background())
}
