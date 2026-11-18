package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/adirwt852-cloud/crash-test-guard/internal/config"
	"github.com/adirwt852-cloud/crash-test-guard/internal/executor"
	"github.com/adirwt852-cloud/crash-test-guard/internal/protocol"
)

const (
	ExitPass  = 0
	ExitFail  = 1
	ExitError = 2
)

type Status string

const (
	StatusPass  Status = "PASS"
	StatusFail  Status = "FAIL"
	StatusError Status = "ERROR"
)

type SideEffect struct {
	OperationID    string         `json:"operation_id"`
	OperationType  string         `json:"operation_type"`
	IdempotencyKey string         `json:"idempotency_key"`
	Status         string         `json:"status"`
	Result         map[string]any `json:"result,omitempty"`
}

type RunResult struct {
	WorkflowID  string                       `json:"workflow_id"`
	Status      string                       `json:"status"`
	State       *protocol.StateResponse      `json:"state,omitempty"`
	Checkpoint  *protocol.CheckpointEnvelope `json:"checkpoint,omitempty"`
	Events      []protocol.Event             `json:"events,omitempty"`
	SideEffects []SideEffect                 `json:"side_effects,omitempty"`
}

type ScenarioResult struct {
	Scenario       string                       `json:"scenario"`
	Type           string                       `json:"type"`
	Status         Status                       `json:"status"`
	Duration       time.Duration                `json:"duration"`
	Error          error                        `json:"error,omitempty"`
	Baseline       *RunResult                   `json:"baseline,omitempty"`
	CrashRun       *RunResult                   `json:"crash_run,omitempty"`
	PreCheckpoint  *protocol.CheckpointEnvelope `json:"pre_checkpoint,omitempty"`
	PostCheckpoint *protocol.CheckpointEnvelope `json:"post_checkpoint,omitempty"`
}

type TestResult struct {
	Scenarios []ScenarioResult `json:"scenarios"`
	Overall   Status           `json:"overall"`
	Duration  time.Duration    `json:"duration"`
	Config    string           `json:"config,omitempty"`
}

type Engine struct {
	config   *config.Config
	executor executor.Executor
	client   *protocol.Client
}

func NewEngine(cfg *config.Config, ex executor.Executor, client *protocol.Client) *Engine {
	return &Engine{config: cfg, executor: ex, client: client}
}

func (e *Engine) Run(ctx context.Context) (*TestResult, int) {
	start := time.Now()
	result := &TestResult{Config: e.config.SourcePath()}

	if e.config == nil {
		result.Overall = StatusError
		return result, ExitError
	}

	if err := e.executor.Start(ctx, e.config.Agent); err != nil {
		result.Overall = StatusError
		return result, ExitError
	}
	defer e.executor.Cleanup(ctx)

	if err := e.waitForHealth(ctx); err != nil {
		result.Overall = StatusError
		return result, ExitError
	}

	baseline, err := e.startAndRunToCompletion(ctx, nil)
	if err != nil {
		result.Overall = StatusError
		return result, ExitError
	}

	if err := e.executor.Kill(ctx); err != nil {
		result.Overall = StatusError
		return result, ExitError
	}

	for _, sc := range e.config.CrashScenarios {
		result.Scenarios = append(result.Scenarios, e.runScenario(ctx, sc, baseline))
	}

	result.Overall = e.aggregate(result.Scenarios)
	result.Duration = time.Since(start)

	switch result.Overall {
	case StatusPass:
		return result, ExitPass
	case StatusFail:
		return result, ExitFail
	default:
		return result, ExitError
	}
}

func (e *Engine) runScenario(ctx context.Context, sc config.CrashScenario, baseline *RunResult) (res ScenarioResult) {
	start := time.Now()
	res = ScenarioResult{Scenario: sc.Type, Type: sc.Type}
	defer func() {
		res.Duration = time.Since(start)
		_ = e.executor.Kill(ctx)
	}()

	if err := e.executor.Start(ctx, e.config.Agent); err != nil {
		res.Status = StatusError
		res.Error = fmt.Errorf("start agent: %w", err)
		return res
	}

	if err := e.waitForHealth(ctx); err != nil {
		res.Status = StatusError
		res.Error = fmt.Errorf("wait for health: %w", err)
		return res
	}

	startResp, err := e.client.StartWorkflow(ctx, nil)
	if err != nil {
		res.Status = StatusError
		res.Error = fmt.Errorf("start workflow: %w", err)
		return res
	}
	wfID := startResp.WorkflowID

	if err := e.waitForTrigger(ctx, wfID, sc.Trigger, sc.Fallback); err != nil {
		if sc.Fallback.OnMissed == "skip" {
			res.Status = StatusPass
			return res
		}
		res.Status = StatusError
		res.Error = fmt.Errorf("wait for trigger: %w", err)
		return res
	}

	preCP, err := e.client.GetCheckpoint(ctx, wfID)
	if err != nil {
		res.Status = StatusError
		res.Error = fmt.Errorf("get checkpoint: %w", err)
		return res
	}
	res.PreCheckpoint = preCP

	if err := e.executor.Kill(ctx); err != nil {
		res.Status = StatusError
		res.Error = fmt.Errorf("inject crash: %w", err)
		return res
	}

	if err := e.executor.Restart(ctx); err != nil {
		res.Status = StatusError
		res.Error = fmt.Errorf("restart agent: %w", err)
		return res
	}

	if err := e.waitForHealth(ctx); err != nil {
		res.Status = StatusError
		res.Error = fmt.Errorf("wait for health after restart: %w", err)
		return res
	}

	if _, err := e.client.ResumeWorkflow(ctx, wfID, preCP.Payload.Data); err != nil {
		res.Status = StatusError
		res.Error = fmt.Errorf("resume workflow: %w", err)
		return res
	}

	crashRun, err := e.pollToCompletion(ctx, wfID)
	if err != nil {
		res.Status = StatusError
		res.Error = fmt.Errorf("poll to completion: %w", err)
		return res
	}
	res.CrashRun = crashRun

	var postCP *protocol.CheckpointEnvelope
	if cp, err := e.client.GetCheckpoint(ctx, wfID); err == nil {
		postCP = cp
		res.PostCheckpoint = postCP
	}

	if err := e.verifyScenario(baseline, crashRun, preCP, postCP); err != nil {
		res.Status = StatusFail
		res.Error = err
		return res
	}

	res.Status = StatusPass
	return res
}

func (e *Engine) waitForHealth(ctx context.Context) error {
	timeout := e.config.Agent.StartupTimeout.Duration
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("agent did not become healthy within %s", timeout)
		case <-ticker.C:
			resp, err := e.client.Health(ctx)
			if err == nil && resp.Status == protocol.StatusHealthy {
				return nil
			}
		}
	}
}

func (e *Engine) waitForTrigger(ctx context.Context, wfID string, trigger config.Trigger, fallback config.Fallback) error {
	if trigger.Timing == "before_event" {
		return nil
	}

	var sinceSeq int64
	timeout := fallback.Timeout.Duration
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			switch fallback.OnMissed {
			case "skip", "continue":
				return nil
			default:
				return fmt.Errorf("trigger event %q for step %q not observed within %s", trigger.Event, trigger.StepID, timeout)
			}
		case <-ticker.C:
			events, err := e.client.GetEvents(ctx, wfID, sinceSeq)
			if err != nil {
				continue
			}
			for _, ev := range events {
				if ev.Sequence > sinceSeq {
					sinceSeq = ev.Sequence
				}
				if ev.Type == trigger.Event && ev.StepID == trigger.StepID {
					return nil
				}
			}
		}
	}
}

func (e *Engine) startAndRunToCompletion(ctx context.Context, input map[string]any) (*RunResult, error) {
	startResp, err := e.client.StartWorkflow(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("start workflow: %w", err)
	}
	return e.pollToCompletion(ctx, startResp.WorkflowID)
}

func (e *Engine) pollToCompletion(ctx context.Context, wfID string) (*RunResult, error) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			state, err := e.client.GetState(ctx, wfID)
			if err != nil {
				continue
			}
			if state.Status == protocol.StatusCompleted || state.Status == protocol.StatusFailed {
				cp, _ := e.client.GetCheckpoint(ctx, wfID)
				return &RunResult{
					WorkflowID: wfID,
					Status:     state.Status,
					State:      state,
					Checkpoint: cp,
				}, nil
			}
		}
	}
}

func (e *Engine) verifyScenario(baseline, crash *RunResult, preCP, postCP *protocol.CheckpointEnvelope) error {
	if e.config.Verification.WorkflowCompletion {
		if err := verifyWorkflowCompletion(crash.State); err != nil {
			return fmt.Errorf("workflow completion: %w", err)
		}
	}
	if e.config.Verification.CheckpointConsistency {
		if err := verifyCheckpointConsistency(preCP, postCP); err != nil {
			return fmt.Errorf("checkpoint consistency: %w", err)
		}
	}
	if e.config.Verification.BaselineComparison {
		if err := compareBaseline(baseline, crash); err != nil {
			return fmt.Errorf("baseline comparison: %w", err)
		}
	}
	if e.config.Verification.DuplicateSideEffects {
		if err := verifyDuplicateSideEffects(baseline.SideEffects, crash.SideEffects); err != nil {
			return fmt.Errorf("duplicate side effects: %w", err)
		}
	}
	return nil
}

func verifyWorkflowCompletion(state *protocol.StateResponse) error {
	if state == nil {
		return fmt.Errorf("no final state recorded")
	}
	if state.Status != protocol.StatusCompleted {
		return fmt.Errorf("workflow did not complete, status=%s", state.Status)
	}
	return nil
}

func verifyCheckpointConsistency(pre, post *protocol.CheckpointEnvelope) error {
	if pre == nil || post == nil {
		return nil
	}
	if pre.WorkflowID != post.WorkflowID {
		return fmt.Errorf("checkpoint workflow id mismatch: %s vs %s", pre.WorkflowID, post.WorkflowID)
	}
	if post.Sequence < pre.Sequence {
		return fmt.Errorf("checkpoint sequence regression: %d < %d", post.Sequence, pre.Sequence)
	}
	return nil
}

func compareBaseline(baseline, crash *RunResult) error {
	if baseline == nil || crash == nil {
		return nil
	}
	if baseline.State == nil || crash.State == nil {
		return nil
	}
	if crash.State.Status != baseline.State.Status {
		return fmt.Errorf("status mismatch: baseline=%s crash=%s", baseline.State.Status, crash.State.Status)
	}
	return nil
}

func verifyDuplicateSideEffects(baselineLedger, crashLedger []SideEffect) error {
	seen := make(map[string]bool)
	for _, eff := range crashLedger {
		if seen[eff.OperationID] {
			return fmt.Errorf("duplicate side effect detected: %s", eff.OperationID)
		}
		seen[eff.OperationID] = true
	}
	return nil
}

func (e *Engine) aggregate(scenarios []ScenarioResult) Status {
	overall := StatusPass
	for _, s := range scenarios {
		switch s.Status {
		case StatusError:
			return StatusError
		case StatusFail:
			overall = StatusFail
		}
	}
	return overall
}

func (tr *TestResult) Summary() string {
	parts := make([]string, 0, len(tr.Scenarios)+1)
	parts = append(parts, fmt.Sprintf("overall: %s", tr.Overall))
	for _, s := range tr.Scenarios {
		line := fmt.Sprintf("  %s: %s", s.Scenario, s.Status)
		if s.Error != nil {
			line += fmt.Sprintf(" (error: %s)", s.Error)
		}
		parts = append(parts, line)
	}
	return strings.Join(parts, "\n")
}
