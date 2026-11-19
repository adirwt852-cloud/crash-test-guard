package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/adirwt852-cloud/crash-test-guard/internal/config"
	"github.com/adirwt852-cloud/crash-test-guard/internal/executor"
	"github.com/adirwt852-cloud/crash-test-guard/internal/orchestrator"
	"github.com/adirwt852-cloud/crash-test-guard/internal/protocol"
)

// mockSteps is the deterministic 5-step workflow the mock agent executes.
var mockSteps = []string{"read_email", "generate_invoice", "process_payment", "send_confirmation", "complete_task"}

// stepDuration is how long the mock agent spends on each step.
const stepDuration = 150 * time.Millisecond

// mockAgent is an in-process agent that implements the full Crash-Test Guard
// agent protocol over httptest.Server. Unlike the standalone mock-agent binary,
// it keeps workflow state inside the test process and resumes partially
// executed workflows to completion, which is what makes crash recovery
// testable against the Engine.
type mockAgent struct {
	mu   sync.Mutex
	wfs  map[string]*mockWorkflow
	next int
}

// mockWorkflow is the in-memory state of one workflow execution.
type mockWorkflow struct {
	mu           sync.Mutex
	id           string
	status       string
	currentStep  string
	stepIndex    int
	completed    []string
	stepOutputs  map[string]any
	sequence     int64
	events       []mockEvent
	checkpointID string
	executing    bool
}

type mockEvent struct {
	Sequence  int64  `json:"sequence"`
	Type      string `json:"type"`
	StepID    string `json:"step_id"`
	Timestamp string `json:"timestamp"`
	Data      any    `json:"data"`
}

func newMockAgent() *mockAgent {
	return &mockAgent{wfs: map[string]*mockWorkflow{}, next: 1}
}

func (a *mockAgent) handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && path == "/health":
		json.NewEncoder(w).Encode(map[string]any{
			"protocol_version": protocol.ProtocolVersion,
			"agent_version":    "test-1.0",
			"status":           protocol.StatusHealthy,
		})
		return
	case r.Method == http.MethodPost && path == "/workflow/start":
		a.handleStart(w, r)
		return
	}
	if len(path) > len("/workflow/") && path[:len("/workflow/")] == "/workflow/" {
		rest := path[len("/workflow/"):]
		id, endpoint := rest, ""
		if i := indexSlash(rest); i >= 0 {
			id = rest[:i]
			endpoint = rest[i+1:]
		}
		switch endpoint {
		case "state":
			a.handleState(w, id)
			return
		case "checkpoint":
			a.handleCheckpoint(w, id)
			return
		case "events":
			a.handleEvents(w, id, r)
			return
		case "resume":
			if r.Method == http.MethodPost {
				a.handleResume(w, id)
				return
			}
		}
	}
	http.NotFound(w, r)
}

func (a *mockAgent) handleStart(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	wf := &mockWorkflow{
		id:          mockWorkflowID(a),
		status:      protocol.StatusRunning,
		currentStep: mockSteps[0],
		stepIndex:   0,
		completed:   []string{},
		stepOutputs: map[string]any{},
		events:      []mockEvent{},
	}
	a.wfs[wf.id] = wf
	a.next++
	a.mu.Unlock()

	go a.runWorkflow(wf)
	json.NewEncoder(w).Encode(map[string]any{
		"workflow_id": wf.id,
		"status":      protocol.StatusRunning,
	})
}

func mockWorkflowID(a *mockAgent) string {
	id := mockWorkflowIDWithPrefix(a, "wf")
	return id
}

func mockWorkflowIDWithPrefix(a *mockAgent, prefix string) string {
	id := prefix
	if a.next > 1 {
		id = prefix + "-" + itoa(a.next)
	}
	return id
}

func itoa(n int) string {
	buf := make([]byte, 0, 16)
	for n >= 10 {
		buf = append(buf, byte('0'+n%10))
		n /= 10
	}
	buf = append(buf, byte('0'+n))
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}

func (a *mockAgent) getWorkflow(id string) *mockWorkflow {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.wfs[id]
}

func (a *mockAgent) handleState(w http.ResponseWriter, id string) {
	wf := a.getWorkflow(id)
	if wf == nil {
		http.NotFound(w, &http.Request{})
		return
	}
	wf.mu.Lock()
	defer wf.mu.Unlock()
	json.NewEncoder(w).Encode(map[string]any{
		"workflow_id":     wf.id,
		"status":          wf.status,
		"current_step":    wf.currentStep,
		"completed_steps": wf.completed,
		"step_outputs":    wf.stepOutputs,
	})
}

func (a *mockAgent) handleCheckpoint(w http.ResponseWriter, id string) {
	wf := a.getWorkflow(id)
	if wf == nil {
		http.NotFound(w, &http.Request{})
		return
	}
	wf.mu.Lock()
	defer wf.mu.Unlock()
	wf.sequence++
	payload := marshalCheckpoint(wf)
	wf.checkpointID = mockWorkflowIDWithPrefix(a, "cp") + "-" + itoa(int(wf.sequence))
	json.NewEncoder(w).Encode(map[string]any{
		"protocol_version": protocol.ProtocolVersion,
		"workflow_id":      wf.id,
		"checkpoint_id":    wf.checkpointID,
		"sequence":         wf.sequence,
		"created_at":       time.Now().UTC().Format(time.RFC3339),
		"payload": map[string]any{
			"format": "json",
			"data":   payload,
		},
	})
}

func marshalCheckpoint(wf *mockWorkflow) string {
	b, _ := json.Marshal(map[string]any{
		"workflow_id":  wf.id,
		"step_index":   wf.stepIndex,
		"completed":    wf.completed,
		"step_outputs": wf.stepOutputs,
		"sequence":     wf.sequence,
	})
	return string(b)
}

func (a *mockAgent) handleEvents(w http.ResponseWriter, id string, r *http.Request) {
	wf := a.getWorkflow(id)
	if wf == nil {
		http.NotFound(w, &http.Request{})
		return
	}
	wf.mu.Lock()
	defer wf.mu.Unlock()
	since := int64(0)
	if v := r.URL.Query().Get("since"); v != "" {
		for _, c := range v {
			if c < '0' || c > '9' {
				break
			}
			since = since*10 + int64(c-'0')
		}
	}
	events := wf.events
	if since > 0 {
		filtered := make([]mockEvent, 0, len(wf.events))
		for _, ev := range wf.events {
			if ev.Sequence > since {
				filtered = append(filtered, ev)
			}
		}
		events = filtered
	}
	json.NewEncoder(w).Encode(events)
}

func (a *mockAgent) handleResume(w http.ResponseWriter, id string) {
	wf := a.getWorkflow(id)
	if wf == nil {
		http.NotFound(w, &http.Request{})
		return
	}
	wf.mu.Lock()
	if wf.status != protocol.StatusCompleted && !wf.executing {
		wf.status = protocol.StatusResumed
		wf.executing = true
		wf.mu.Unlock()
		go a.runRemaining(wf)
	} else {
		wf.mu.Unlock()
	}
	json.NewEncoder(w).Encode(map[string]any{
		"status":      protocol.StatusResumed,
		"workflow_id": wf.id,
	})
}

func (a *mockAgent) runWorkflow(wf *mockWorkflow) {
	wf.mu.Lock()
	wf.executing = true
	wf.mu.Unlock()
	a.executeSteps(wf, 0)
}

func (a *mockAgent) runRemaining(wf *mockWorkflow) {
	wf.mu.Lock()
	start := wf.stepIndex
	if start >= len(mockSteps) {
		wf.mu.Unlock()
		return
	}
	wf.mu.Unlock()
	a.executeSteps(wf, start)
}

func (a *mockAgent) executeSteps(wf *mockWorkflow, start int) {
	for i := start; i < len(mockSteps); i++ {
		step := mockSteps[i]
		time.Sleep(stepDuration)

		wf.mu.Lock()
		wf.currentStep = step
		wf.status = protocol.StatusRunning
		wf.mu.Unlock()
		time.Sleep(stepDuration)

		wf.mu.Lock()
		wf.stepIndex = i + 1
		wf.completed = append(wf.completed, step)
		wf.stepOutputs[step] = map[string]any{"done": true}
		wf.sequence++
		wf.events = append(wf.events, mockEvent{
			Sequence:  wf.sequence,
			Type:      "step_completed",
			StepID:    step,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Data:      map[string]any{"step": step},
		})
		if i == len(mockSteps)-1 {
			wf.status = protocol.StatusCompleted
			wf.currentStep = ""
		} else {
			wf.status = protocol.StatusRunning
			wf.currentStep = mockSteps[i+1]
		}
		wf.mu.Unlock()
	}
	wf.mu.Lock()
	wf.executing = false
	wf.mu.Unlock()
}

func indexSlash(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}

// mockAgentBinary returns the absolute path to the compiled mock-agent binary,
// which LocalExecutor starts as a subprocess to exercise the executor lifecycle.
func mockAgentBinary(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file) // .../crash-test-guard/test/integration
	bin := filepath.Join(dir, "..", "..", "mock-agent", "mock-agent")
	abs, err := filepath.Abs(bin)
	if err != nil {
		t.Fatalf("resolve mock-agent binary path: %v", err)
	}
	return abs
}

func newProcessKillConfig(endpoint string) *config.Config {
	return &config.Config{
		Version: config.Version,
		Agent: config.AgentConfig{
			Command:         []string{""},
			Protocol:        "http",
			Endpoint:        endpoint,
			StartupTimeout:  config.Duration{Duration: 15 * time.Second},
			ShutdownTimeout: config.Duration{Duration: 5 * time.Second},
		},
		CrashScenarios: []config.CrashScenario{{
			Type: "process_kill",
			Trigger: config.Trigger{
				Event:  "step_completed",
				StepID: "process_payment",
				Timing: "after_event",
			},
			Fallback: config.Fallback{
				Timeout:  config.Duration{Duration: 15 * time.Second},
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
}

// TestCrashRecovery_ProcessKill_LocalExecutor runs the full baseline -> crash
// -> resume -> verify flow against the Engine using LocalExecutor and an
// in-process httptest mock agent, asserting the overall result is PASS.
func TestCrashRecovery_ProcessKill_LocalExecutor(t *testing.T) {
	mockSrv := httptest.NewServer(http.HandlerFunc(newMockAgent().handler))
	defer mockSrv.Close()

	cfg := newProcessKillConfig(mockSrv.URL)
	cfg.Agent.Command = []string{mockAgentBinary(t)}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}

	ex := executor.NewLocalExecutor()
	client := protocol.NewClient(protocol.ClientConfig{
		BaseURL:        mockSrv.URL,
		ConnectTimeout: 2 * time.Second,
		RequestTimeout: 10 * time.Second,
		MaxRetries:     3,
	})
	engine := orchestrator.NewEngine(cfg, ex, client)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	result, exitCode := engine.Run(ctx)
	if exitCode != orchestrator.ExitPass {
		t.Fatalf("engine exited %d, want %d\n%s", exitCode, orchestrator.ExitPass, result.Summary())
	}
	if result.Overall != orchestrator.StatusPass {
		t.Fatalf("overall=%s, want PASS\n%s", result.Overall, result.Summary())
	}
}
