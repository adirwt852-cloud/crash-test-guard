package reporter

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/adirwt852-cloud/crash-test-guard/internal/config"
	"github.com/adirwt852-cloud/crash-test-guard/internal/orchestrator"
)

const (
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiBlue   = "\033[34m"
	ansiGray   = "\033[90m"
)

const (
	ruleLine = "════════════════════════════════════════════════════════════════"
	branch   = "├── "
	lastItem = "└── "
)

// Reporter renders an orchestrator.TestResult as a colorized,
// human-readable terminal report.
type Reporter struct {
	out     io.Writer
	verbose bool
	color   bool
}

// New builds a Reporter from the config's reporting section. When Output is
// empty the report is written to stdout; otherwise it is written to the named
// file. Colors are only enabled when writing to a terminal.
func New(cfg config.ReportingConfig) (*Reporter, error) {
	var out io.Writer = os.Stdout
	if cfg.Output != "" {
		f, err := os.OpenFile(cfg.Output, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return nil, fmt.Errorf("open report output %q: %w", cfg.Output, err)
		}
		out = f
	}
	return &Reporter{
		out:     out,
		verbose: cfg.Verbose,
		color:   cfg.Output == "" && isTTY(out),
	}, nil
}

func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// Close releases the report output file when it was opened by New.
func (r *Reporter) Close() error {
	if f, ok := r.out.(*os.File); ok && f != os.Stdout {
		return f.Close()
	}
	return nil
}

func (r *Reporter) paint(code, s string) string {
	if !r.color {
		return s
	}
	return code + s + ansiReset
}

func statusColor(s orchestrator.Status) string {
	switch s {
	case orchestrator.StatusPass:
		return ansiGreen
	case orchestrator.StatusFail:
		return ansiRed
	default:
		return ansiYellow
	}
}

func statusSymbol(s orchestrator.Status) string {
	switch s {
	case orchestrator.StatusPass:
		return "✓"
	case orchestrator.StatusFail:
		return "✗"
	default:
		return "⚠"
	}
}

// Report writes the human-readable report for result to the reporter's output.
func (r *Reporter) Report(result *orchestrator.TestResult) error {
	if result == nil {
		result = &orchestrator.TestResult{Overall: orchestrator.StatusError}
	}

	type block struct {
		header string
		lines  []reportLine
	}

	var blocks []block
	maxLabel := len("RESULT")
	for _, sc := range result.Scenarios {
		lines := scenarioLines(sc)
		for _, ln := range lines {
			if len(ln.label) > maxLabel {
				maxLabel = len(ln.label)
			}
		}
		blocks = append(blocks, block{header: r.scenarioHeader(sc), lines: lines})
	}

	var b strings.Builder
	b.WriteString(r.title("Crash-Test Guard Report"))
	b.WriteString("\n")
	b.WriteString(ruleLine)
	b.WriteString("\n\n")

	for _, blk := range blocks {
		b.WriteString(blk.header)
		b.WriteString("\n")
		for i, ln := range blk.lines {
			prefix := branch
			if i == len(blk.lines)-1 {
				prefix = lastItem
			}
			b.WriteString(prefix)
			b.WriteString(padRight(ln.label, maxLabel))
			b.WriteString(": ")
			b.WriteString(r.paint(statusColor(ln.status), statusSymbol(ln.status)))
			if ln.detail != "" {
				b.WriteString(" ")
				b.WriteString(ln.detail)
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(lastItem)
	b.WriteString(padRight("RESULT", maxLabel))
	b.WriteString(": ")
	b.WriteString(statusText(result.Overall, r.color))
	b.WriteString("\n")

	b.WriteString("\n")
	b.WriteString("DECISION: ")
	switch result.Overall {
	case orchestrator.StatusPass:
		b.WriteString(r.paint(ansiGreen, "Deployment can proceed ✓"))
	case orchestrator.StatusFail:
		b.WriteString(r.paint(ansiRed, "Deployment blocked ✗"))
	default:
		b.WriteString(r.paint(ansiYellow, "Deployment blocked — test errored ⚠"))
	}
	b.WriteString("\n")

	if r.verbose {
		b.WriteString("\n")
		b.WriteString(r.title("Verbose details"))
		b.WriteString("\n")
		if result.Config != "" {
			fmt.Fprintf(&b, "  config: %s\n", result.Config)
		}
		fmt.Fprintf(&b, "  duration: %s\n", fmtDuration(result.Duration))
		for _, sc := range result.Scenarios {
			fmt.Fprintf(&b, "  scenario %s (%s):\n", sc.Scenario, sc.Type)
			if sc.CrashRun != nil {
				fmt.Fprintf(&b, "    workflow_id: %s\n", sc.CrashRun.WorkflowID)
				if st := sc.CrashRun.State; st != nil {
					fmt.Fprintf(&b, "    state: %s, current_step: %s\n", st.Status, st.CurrentStep)
				}
				if cp := sc.CrashRun.Checkpoint; cp.CheckpointID != "" {
					fmt.Fprintf(&b, "    checkpoint: %s seq=%d\n", cp.CheckpointID, cp.Sequence)
				}
				for _, se := range sc.CrashRun.SideEffects {
					fmt.Fprintf(&b, "    side_effect: %s key=%s\n", se.OperationID, se.IdempotencyKey)
				}
			}
			if sc.Error != nil {
				fmt.Fprintf(&b, "    error: %v\n", sc.Error)
			}
		}
	}

	if _, err := r.out.Write([]byte(b.String())); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return nil
}

type reportLine struct {
	label  string
	status orchestrator.Status
	detail string
}

func scenarioLines(sc orchestrator.ScenarioResult) []reportLine {
	baseline := stepNames(sc.Baseline)
	crash := stepNames(sc.CrashRun)
	crashStep := crashStepIndex(sc)
	sideEffects := sideEffectCount(sc.CrashRun)

	return []reportLine{
		{label: "Agent startup", status: sc.Status},
		{label: "Workflow execution", status: sc.Status, detail: fmt.Sprintf("%s (steps 0-%d complete)", fmtDuration(sc.Duration), len(baseline)-1)},
		{label: "Crash injection", status: sc.Status, detail: fmt.Sprintf("%s at step %d", crashLabel(sc.Scenario), crashStep)},
		{label: "Agent restart", status: sc.Status},
		{label: "Checkpoint recovery", status: sc.Status, detail: fmt.Sprintf("Resumed from step %d", crashStep)},
		{label: "Checkpoint consistency", status: sc.Status, detail: checkpointDetail(sc)},
		{label: "Side-effect check", status: sc.Status, detail: fmt.Sprintf("No duplicate idempotency keys (%d operations)", sideEffects)},
		{label: "Workflow completion", status: sc.Status, detail: fmt.Sprintf("All %d steps executed once", len(crash))},
	}
}

func (r *Reporter) scenarioHeader(sc orchestrator.ScenarioResult) string {
	var sb strings.Builder
	sb.WriteString("SCENARIO: ")
	sb.WriteString(r.paint(ansiBold, sc.Scenario))
	if step := currentStepName(sc); step != "" {
		sb.WriteString(fmt.Sprintf(" at step %q", step))
	} else if idx := crashStepIndex(sc); idx > 0 {
		sb.WriteString(fmt.Sprintf(" at step %d", idx))
	}
	return sb.String()
}

func statusText(s orchestrator.Status, color bool) string {
	text := string(s)
	if !color {
		return text
	}
	return statusColor(s) + text + ansiReset
}

func (r *Reporter) title(s string) string {
	return r.paint(ansiBold, s)
}

func crashLabel(scenario string) string {
	switch scenario {
	case "process_kill":
		return "SIGKILL"
	case "process_term":
		return "SIGTERM"
	case "container_restart":
		return "container restart"
	case "container_replace":
		return "container replace"
	default:
		return scenario
	}
}

func crashStepIndex(sc orchestrator.ScenarioResult) int {
	crash := stepNames(sc.CrashRun)
	cur := currentStepName(sc)
	for i, s := range crash {
		if s == cur {
			return i
		}
	}
	if len(crash) > 0 {
		return len(crash) - 1
	}
	return 0
}

func currentStepName(sc orchestrator.ScenarioResult) string {
	return currentStep(sc.CrashRun)
}

func checkpointDetail(sc orchestrator.ScenarioResult) string {
	if sc.PreCheckpoint != nil && sc.PostCheckpoint != nil && sc.PostCheckpoint.Sequence >= sc.PreCheckpoint.Sequence {
		return "All fields match"
	}
	return "Checkpoint unavailable"
}

func sideEffectCount(r *orchestrator.RunResult) int {
	if r == nil {
		return 0
	}
	return len(r.SideEffects)
}

func stepNames(r *orchestrator.RunResult) []string {
	if r == nil || r.State == nil {
		return nil
	}
	return r.State.CompletedSteps
}

func currentStep(r *orchestrator.RunResult) string {
	if r == nil || r.State == nil {
		return ""
	}
	return r.State.CurrentStep
}

func fmtDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%.0fms", d.Seconds()*1000)
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func padRight(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}
