package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/adirwt852-cloud/crash-test-guard/internal/config"
	"github.com/adirwt852-cloud/crash-test-guard/internal/executor"
	"github.com/adirwt852-cloud/crash-test-guard/internal/protocol"
)

type mockExecutor struct {
	mu           sync.Mutex
	startCalls   int
	killCalls    int
	restartCalls int
	err          error
}

func (m *mockExecutor) Start(context.Context, config.AgentConfig) error {
	m.mu.Lock()
	m.startCalls++
	m.mu.Unlock()
	return m.err
}
func (m *mockExecutor) Wait(context.Context) error { return nil }
func (m *mockExecutor) Kill(context.Context) error {
	m.mu.Lock()
	m.killCalls++
	m.mu.Unlock()
	return m.err
}
func (m *mockExecutor) Restart(context.Context) error {
	m.mu.Lock()
	m.restartCalls++
	m.mu.Unlock()
	return m.err
}
func (m *mockExecutor) Status(context.Context) (executor.Status, error) {
	return executor.StatusRunning, nil
}
func (m *mockExecutor) Cleanup(context.Context) error { return nil }
func (m *mockExecutor) counts() (int, int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.startCalls, m.killCalls, m.restartCalls
}

type scenarioMode int

const (
	modeEvent scenarioMode = iota
	modeNoEvent
	modePreCondition
)

type scenarioServer struct {
	*httptest.Server
	mu         sync.Mutex
	mode       scenarioMode
	stepID     string
	stateCalls int
	eventCalls int
}

func newScenarioServer(t *testing.T, mode scenarioMode, stepID string) *scenarioServer {
	t.Helper()
	s := &scenarioServer{mode: mode, stepID: stepID}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

func (s *scenarioServer) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path

	switch {
	case r.Method == http.MethodGet && path == "/health":
		json.NewEncoder(w).Encode(protocol.HealthResponse{
			ProtocolVersion: protocol.ProtocolVersion, Status: protocol.StatusHealthy,
		})
		return
	case r.Method == http.MethodPost && path == "/workflow/start":
		json.NewEncoder(w).Encode(protocol.StartResponse{WorkflowID: "wf-1", Status: protocol.StatusRunning})
		return
	case r.Method == http.MethodPost && len(path) >= len("/workflow//resume") && path[:len("/workflow/")] == "/workflow/" && path[len(path)-len("/resume"):] == "/resume":
		s.mu.Lock()
		_ = s.eventCalls
		s.mu.Unlock()
		json.NewEncoder(w).Encode(protocol.ResumeResponse{Status: protocol.StatusResumed, WorkflowID: "wf-1"})
		return
	}

	// /workflow/{id}/state or /workflow/{id}/events or /workflow/{id}/checkpoint
	if len(path) > len("/workflow/") && path[:len("/workflow/")] == "/workflow/" {
		rest := path[len("/workflow/"):]
		id := rest
		endpoint := ""
		if i := indexSlash(rest); i >= 0 {
			id = rest[:i]
			endpoint = rest[i+1:]
		}
		if id == "" || id == "wf-1" == false {
			id = "wf-1" // accept any id
		}
		switch endpoint {
		case "state":
			s.mu.Lock()
			s.stateCalls++
			s.mu.Unlock()
			curStep := s.stepID
			if s.mode == modePreCondition {
				curStep = s.stepID
			} else {
				curStep = "read_email"
			}
			json.NewEncoder(w).Encode(protocol.StateResponse{
				WorkflowID: id, Status: protocol.StatusCompleted, CurrentStep: curStep,
				CompletedSteps: []string{},
			})
			return
		case "events":
			s.mu.Lock()
			s.eventCalls++
			since := int64(0)
			if v := r.URL.Query().Get("since"); v != "" {
				fmt.Sscanf(v, "%d", &since)
			}
			s.mu.Unlock()
			var events []protocol.Event
			if s.mode == modeEvent {
				events = []protocol.Event{{Sequence: 1, Type: "step_completed", StepID: s.stepID}}
			}
			filtered := events
			if since > 0 {
				filtered = nil
				for _, e := range events {
					if e.Sequence > since {
						filtered = append(filtered, e)
					}
				}
			}
			json.NewEncoder(w).Encode(filtered)
			return
		case "checkpoint":
			json.NewEncoder(w).Encode(protocol.CheckpointEnvelope{
				ProtocolVersion: protocol.ProtocolVersion, WorkflowID: id,
				CheckpointID: "cp-1", Sequence: 1,
				Payload: protocol.CheckpointPayload{Format: "json", Data: "ckpt"},
			})
			return
		}
	}

	http.NotFound(w, r)
}

func indexSlash(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}

func newEngine(t *testing.T, srv *scenarioServer, sc config.CrashScenario) (*Engine, *mockExecutor) {
	t.Helper()
	ex := &mockExecutor{}
	cl := protocol.NewClient(protocol.ClientConfig{
		BaseURL:        srv.URL,
		ConnectTimeout: time.Second,
		RequestTimeout: 2 * time.Second,
		MaxRetries:     0,
	})
	cfg := &config.Config{
		Version: config.Version,
		Agent: config.AgentConfig{
			Endpoint:        srv.URL,
			StartupTimeout:  config.Duration{Duration: 5 * time.Second},
			ShutdownTimeout: config.Duration{Duration: 5 * time.Second},
		},
		CrashScenarios: []config.CrashScenario{sc},
		Verification:   config.VerificationConfig{},
	}
	return NewEngine(cfg, ex, cl), ex
}

func testScenario(t *testing.T, mode scenarioMode, onMissed string, timing string, wantPass bool, wantKill bool) {
	t.Helper()
	stepID := "process_payment"
	timeout := 150 * time.Millisecond
	if timing == "before_event" {
		timeout = 5 * time.Second
	}
	sc := config.CrashScenario{
		Type:     "process_kill",
		Trigger:  config.Trigger{Event: "step_completed", StepID: stepID, Timing: timing},
		Fallback: config.Fallback{Timeout: config.Duration{Duration: timeout}, OnMissed: onMissed},
	}
	srv := newScenarioServer(t, mode, stepID)
	defer srv.Close()
	engine, ex := newEngine(t, srv, sc)
	res := engine.runScenario(context.Background(), sc, nil)
	if wantPass && res.Status != StatusPass {
		t.Fatalf("mode=%v onMissed=%s timing=%s: status=%s, want PASS; error=%v", mode, onMissed, timing, res.Status, res.Error)
	}
	if !wantPass && res.Status == StatusPass {
		t.Fatalf("mode=%v onMissed=%s timing=%s: status=PASS, want non-PASS", mode, onMissed, timing)
	}
	_, _, restartCalls := ex.counts()
	if wantKill && restartCalls == 0 {
		t.Fatalf("mode=%v: expected crash+restart (injection), got restartCalls=0", mode)
	}
	if !wantKill && restartCalls > 0 {
		t.Fatalf("mode=%v: expected no crash injection, got restartCalls=%d", mode, restartCalls)
	}
}

func TestRunScenario_AfterEventFound(t *testing.T) {
	testScenario(t, modeEvent, "fail", "after_event", true, true)
}

func TestRunScenario_AfterEventTimeoutFail(t *testing.T) {
	testScenario(t, modeNoEvent, "fail", "after_event", false, false)
}

func TestRunScenario_AfterEventTimeoutSkip(t *testing.T) {
	testScenario(t, modeNoEvent, "skip", "after_event", true, false)
}

func TestRunScenario_AfterEventTimeoutContinue(t *testing.T) {
	testScenario(t, modeNoEvent, "continue", "after_event", true, true)
}

func TestRunScenario_BeforeEventPreCondition(t *testing.T) {
	testScenario(t, modePreCondition, "fail", "before_event", true, true)
}
