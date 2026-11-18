package reporter

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/adirwt852-cloud/crash-test-guard/internal/config"
	"github.com/adirwt852-cloud/crash-test-guard/internal/orchestrator"
)

// DefaultJUnitOutput is the file written when the reporting config does not
// specify an explicit output path for JUnit reports.
const DefaultJUnitOutput = "crash-test-report.xml"

const junitSuiteName = "crash-test-guard"

// JUnitReporter renders an orchestrator.TestResult as JUnit XML so that CI
// systems (GitHub Actions, GitLab CI, Jenkins, ...) can ingest the outcome.
type JUnitReporter struct {
	out  io.Writer
	file *os.File
}

// NewJUnit builds a JUnitReporter from the config's reporting section. When
// Output is empty the report is written to DefaultJUnitOutput.
func NewJUnit(cfg config.ReportingConfig) (*JUnitReporter, error) {
	path := cfg.Output
	if path == "" {
		path = DefaultJUnitOutput
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open junit report output %q: %w", path, err)
	}
	return &JUnitReporter{out: f, file: f}, nil
}

// Close releases the report output file when it was opened by NewJUnit.
func (r *JUnitReporter) Close() error {
	if r.file != nil {
		err := r.file.Close()
		r.file = nil
		r.out = nil
		return err
	}
	return nil
}

// Report writes the JUnit XML report for result to the reporter's output.
func (r *JUnitReporter) Report(result *orchestrator.TestResult) error {
	if r.out == nil {
		return fmt.Errorf("junit reporter is closed")
	}
	return WriteJUnit(r.out, result)
}

type junitTestSuites struct {
	XMLName xml.Name         `xml:"testsuites"`
	Suites  []junitTestSuite `xml:"testsuite"`
}

type junitTestSuite struct {
	Name      string          `xml:"name,attr"`
	Tests     int             `xml:"tests,attr"`
	Failures  int             `xml:"failures,attr"`
	Errors    int             `xml:"errors,attr"`
	Time      string          `xml:"time,attr"`
	TestCases []junitTestCase `xml:"testcase"`
}

type junitTestCase struct {
	Name      string        `xml:"name,attr"`
	Classname string        `xml:"classname,attr"`
	Time      string        `xml:"time,attr"`
	Failure   *junitProblem `xml:"failure,omitempty"`
	Error     *junitProblem `xml:"error,omitempty"`
}

type junitProblem struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr,omitempty"`
	Body    string `xml:",chardata"`
}

// WriteJUnit encodes result as a JUnit XML document to w.
func WriteJUnit(w io.Writer, result *orchestrator.TestResult) error {
	if result == nil {
		result = &orchestrator.TestResult{Overall: orchestrator.StatusError}
	}

	suite := junitTestSuite{
		Name: junitSuiteName,
		Time: junitSeconds(result.Duration),
	}

	for _, sc := range result.Scenarios {
		tc := junitTestCase{
			Name:      junitCaseName(sc),
			Classname: junitSuiteName,
			Time:      junitSeconds(sc.Duration),
		}
		switch sc.Status {
		case orchestrator.StatusFail:
			tc.Failure = &junitProblem{
				Message: junitFailureMessage(sc),
				Type:    "verification",
				Body:    junitDetails(sc),
			}
			suite.Failures++
		case orchestrator.StatusError:
			tc.Error = &junitProblem{
				Message: junitErrorMessage(sc),
				Type:    "isolation",
				Body:    junitDetails(sc),
			}
			suite.Errors++
		}
		suite.TestCases = append(suite.TestCases, tc)
	}
	suite.Tests = len(suite.TestCases)

	if _, err := io.WriteString(w, xml.Header); err != nil {
		return fmt.Errorf("write junit header: %w", err)
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	doc := junitTestSuites{Suites: []junitTestSuite{suite}}
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("encode junit report: %w", err)
	}
	if err := enc.Flush(); err != nil {
		return fmt.Errorf("flush junit report: %w", err)
	}
	if _, err := io.WriteString(w, "\n"); err != nil {
		return fmt.Errorf("write junit trailer: %w", err)
	}
	return nil
}

func junitCaseName(sc orchestrator.ScenarioResult) string {
	if step := junitCurrentStep(sc); step != "" {
		return fmt.Sprintf("%s at step %s", sc.Scenario, step)
	}
	if idx := junitCrashStepIndex(sc); idx > 0 {
		return fmt.Sprintf("%s at step %d", sc.Scenario, idx)
	}
	return sc.Scenario
}

func junitFailureMessage(sc orchestrator.ScenarioResult) string {
	if sc.Error == nil {
		return "Scenario failed"
	}
	msg := sc.Error.Error()
	if i := strings.Index(msg, ":"); i > 0 {
		msg = strings.TrimSpace(msg[:i])
	}
	if msg == "" {
		return "Scenario failed"
	}
	return strings.ToUpper(msg[:1]) + msg[1:] + " failed"
}

func junitErrorMessage(sc orchestrator.ScenarioResult) string {
	if sc.Error == nil {
		return "Scenario errored"
	}
	return sc.Error.Error()
}

func junitDetails(sc orchestrator.ScenarioResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "scenario: %s\n", sc.Scenario)
	fmt.Fprintf(&b, "type: %s\n", sc.Type)
	fmt.Fprintf(&b, "status: %s\n", sc.Status)
	fmt.Fprintf(&b, "duration: %s\n", junitDuration(sc.Duration))

	if step := junitCurrentStep(sc); step != "" {
		fmt.Fprintf(&b, "crash_step: %s (index %d)\n", step, junitCrashStepIndex(sc))
	}
	if sc.PreCheckpoint != nil {
		fmt.Fprintf(&b, "pre_checkpoint: %s seq=%d\n", sc.PreCheckpoint.CheckpointID, sc.PreCheckpoint.Sequence)
	}
	if sc.PostCheckpoint != nil {
		fmt.Fprintf(&b, "post_checkpoint: %s seq=%d\n", sc.PostCheckpoint.CheckpointID, sc.PostCheckpoint.Sequence)
	}
	if sc.CrashRun != nil {
		if st := sc.CrashRun.State; st != nil {
			fmt.Fprintf(&b, "workflow: %s status=%s completed_steps=%d current_step=%s\n",
				sc.CrashRun.WorkflowID, st.Status, len(st.CompletedSteps), st.CurrentStep)
		}
		fmt.Fprintf(&b, "side_effects: %d\n", len(sc.CrashRun.SideEffects))
	}
	if sc.Error != nil {
		fmt.Fprintf(&b, "error: %v\n", sc.Error)
	}
	return strings.TrimRight(b.String(), "\n")
}

func junitCurrentStep(sc orchestrator.ScenarioResult) string {
	if sc.CrashRun == nil || sc.CrashRun.State == nil {
		return ""
	}
	return sc.CrashRun.State.CurrentStep
}

func junitCrashStepIndex(sc orchestrator.ScenarioResult) int {
	if sc.CrashRun == nil || sc.CrashRun.State == nil {
		return 0
	}
	state := sc.CrashRun.State
	for i, step := range state.CompletedSteps {
		if step == state.CurrentStep {
			return i
		}
	}
	if len(state.CompletedSteps) > 0 {
		return len(state.CompletedSteps) - 1
	}
	return 0
}

func junitDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%.0fms", d.Seconds()*1000)
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func junitSeconds(d time.Duration) string {
	secs := d.Seconds()
	if secs < 0 {
		secs = 0
	}
	return strconv.FormatFloat(secs, 'f', 3, 64)
}
