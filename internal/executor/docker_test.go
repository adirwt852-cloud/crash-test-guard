package executor

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/adirwt852-cloud/crash-test-guard/internal/config"
)

func TestNewDockerExecutorDefaults(t *testing.T) {
	e := NewDockerExecutor()
	if e.binary != defaultDockerBinary {
		t.Errorf("binary = %q, want %q", e.binary, defaultDockerBinary)
	}
	if e.crashType != CrashContainerRestart {
		t.Errorf("crashType = %q, want %q", e.crashType, CrashContainerRestart)
	}
	if e.killSignal != syscall.SIGKILL {
		t.Errorf("killSignal = %v, want SIGKILL", e.killSignal)
	}
	if !strings.HasPrefix(e.ContainerName(), containerNamePrefix) {
		t.Errorf("container name %q missing prefix %q", e.ContainerName(), containerNamePrefix)
	}

	status, err := e.Status(context.Background())
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if status != StatusUnknown {
		t.Errorf("status = %s, want %s", status, StatusUnknown)
	}
}

func TestGenerateContainerNameUnique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		name := generateContainerName()
		if !strings.HasPrefix(name, containerNamePrefix) {
			t.Fatalf("name %q missing prefix", name)
		}
		if seen[name] {
			t.Fatalf("duplicate container name %q", name)
		}
		seen[name] = true
	}
}

func TestSetCrashType(t *testing.T) {
	e := NewDockerExecutor()

	if err := e.SetCrashType(CrashContainerReplace); err != nil {
		t.Fatalf("SetCrashType(container_replace) failed: %v", err)
	}
	if e.crashType != CrashContainerReplace {
		t.Errorf("crashType = %q, want %q", e.crashType, CrashContainerReplace)
	}

	if err := e.SetCrashType("bogus"); err == nil {
		t.Fatal("expected error for unsupported crash type")
	}
}

func TestStartRequiresImage(t *testing.T) {
	e := NewDockerExecutor()
	if err := e.Start(context.Background(), config.AgentConfig{}); err == nil {
		t.Fatal("expected error for empty image")
	}
}

func TestMapContainerState(t *testing.T) {
	tests := []struct {
		state    string
		exitCode int
		want     Status
	}{
		{"running", 0, StatusRunning},
		{"restarting", 0, StatusRunning},
		{"exited", 0, StatusStopped},
		{"exited", 137, StatusCrashed},
		{"dead", 1, StatusCrashed},
		{"created", 0, StatusStopped},
		{"weird", 0, StatusUnknown},
	}
	for _, tt := range tests {
		if got := mapContainerState(tt.state, tt.exitCode); got != tt.want {
			t.Errorf("mapContainerState(%q, %d) = %s, want %s", tt.state, tt.exitCode, got, tt.want)
		}
	}
}

func TestSignalName(t *testing.T) {
	if got := signalName(syscall.SIGKILL); got != "KILL" {
		t.Errorf("signalName(SIGKILL) = %q, want KILL", got)
	}
	if got := signalName(syscall.SIGTERM); got != "TERM" {
		t.Errorf("signalName(SIGTERM) = %q, want TERM", got)
	}
}

func TestIsContainerGone(t *testing.T) {
	cases := []string{
		"Error: No such container: abc",
		"No such object: abc",
		"container abc is not running",
	}
	for _, c := range cases {
		if !isContainerGone(errString(c)) {
			t.Errorf("isContainerGone(%q) = false, want true", c)
		}
	}
	if isContainerGone(errString("permission denied")) {
		t.Error("isContainerGone should be false for unrelated errors")
	}
}

func TestDockerStartBuildsRunArgs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub docker script requires a POSIX shell")
	}

	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.log")
	stub := filepath.Join(dir, "docker")
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> " + shellQuote(argsFile) + "\n" +
		"if [ \"$1\" = \"run\" ]; then echo deadbeefcafe; fi\n" +
		"if [ \"$1\" = \"inspect\" ]; then echo \"running|0\"; fi\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	e := NewDockerExecutor(
		WithDockerBinary(stub),
		WithWorkspace(filepath.Join(dir, "workspace"), ""),
	)

	cfg := config.AgentConfig{
		Image:   "example/agent:latest",
		Network: "testnet",
		Volumes: []string{"data:/data"},
		Env:     []string{"FOO=bar"},
		Command: []string{"python", "agent.py"},
	}
	if err := e.Start(context.Background(), cfg); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if e.ContainerID() != "deadbeefcafe" {
		t.Errorf("container id = %q, want deadbeefcafe", e.ContainerID())
	}

	status, err := e.Status(context.Background())
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if status != StatusRunning {
		t.Errorf("status = %s, want %s", status, StatusRunning)
	}

	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args log: %v", err)
	}
	log := string(data)
	for _, want := range []string{
		"run -d --name " + e.ContainerName(),
		"--label " + labelManaged + "=true",
		"--label " + labelRole + "=agent",
		"--network testnet",
		"--volume " + filepath.Join(dir, "workspace") + ":" + defaultWorkspacePath,
		"--volume data:/data",
		"--env FOO=bar",
		"example/agent:latest python agent.py",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("docker run args missing %q; got:\n%s", want, log)
		}
	}

	if err := e.Cleanup(context.Background()); err != nil {
		t.Fatalf("Cleanup failed: %v", err)
	}
	if e.ContainerID() != "" {
		t.Errorf("container id after Cleanup = %q, want empty", e.ContainerID())
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
