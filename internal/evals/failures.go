package evals

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"unicode/utf8"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
)

// FailureCause labels why an unsolved result failed. The names are brief 1.10's.
type FailureCause string

// The failure causes, in rule order.
const (
	CauseInfra           FailureCause = "infra"
	CauseBudget          FailureCause = "budget"
	CauseContextTooLarge FailureCause = "context-too-large"
	CauseRefusedOrError  FailureCause = "refused-or-error"
	CauseToolMisuse      FailureCause = "tool-misuse"
	CauseGaveUp          FailureCause = "gave-up"
	CauseNoRepro         FailureCause = "no-repro"
	CauseBrokeBuild      FailureCause = "broke-build"
	CauseBrokeOtherTests FailureCause = "broke-other-tests"
	CausePartialFix      FailureCause = "partial-fix"
	CauseWrongDiagnosis  FailureCause = "wrong-diagnosis"
	CauseUnclassified    FailureCause = "unclassified"
)

// FailureCauses lists every cause in rule order.
var FailureCauses = []FailureCause{
	CauseInfra, CauseBudget, CauseContextTooLarge, CauseRefusedOrError,
	CauseToolMisuse, CauseGaveUp, CauseNoRepro, CauseBrokeBuild, CauseBrokeOtherTests,
	CausePartialFix, CauseWrongDiagnosis, CauseUnclassified,
}

const (
	evidenceMaxBytes = 160
	answerMaxBytes   = 80
	toolStreakLimit  = 3
)

// Failure is the cause of one unsolved result.
type Failure struct {
	BugID    string       `json:"bug_id"`
	Agent    string       `json:"agent"`
	Provider string       `json:"provider"`
	RunID    string       `json:"run_id"`
	Cause    FailureCause `json:"cause"`
	Evidence string       `json:"evidence"`
}

// FailureReport is every failure of a report and the counts per agent.
type FailureReport struct {
	Failures []Failure                       `json:"failures"` // report order; never null
	ByAgent  map[string]map[FailureCause]int `json:"by_agent"` // agent id -> cause -> count (> 0 only); never null
}

// EventReader reads a run's events; *eventlog.Store implements it.
type EventReader interface {
	Read(ctx context.Context, runID string, afterSeq uint64) ([]*adlcv1.Event, error)
}

// cutBytes cuts s to at most n bytes at a UTF-8 boundary.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// oneLine collapses line breaks and caps s at 160 bytes.
func oneLine(s string) string {
	var b strings.Builder
	inBreak := false
	for _, r := range s {
		if r == '\r' || r == '\n' {
			if !inBreak {
				b.WriteByte(' ')
			}
			inBreak = true
			continue
		}
		inBreak = false
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if len(out) > evidenceMaxBytes {
		out = cutBytes(out, evidenceMaxBytes) + "..."
	}
	return out
}

// toolStreak finds the first run of at least 3 failures of one tool in a row.
func toolStreak(events []*adlcv1.Event) (tool string, from uint64, n int, ok bool) {
	names := map[string]string{}
	for _, ev := range events {
		if c := ev.GetToolCallRequested().GetCall(); c != nil {
			names[c.GetId()] = c.GetName()
		}
	}
	var cur string
	var curFrom uint64
	for _, ev := range events {
		tc := ev.GetToolCallCompleted()
		if tc == nil {
			continue
		}
		name := names[tc.GetCallId()]
		switch {
		case !tc.GetIsError():
			if ok {
				return tool, from, n, true
			}
			cur, n = "", 0
		case name == cur && n > 0:
			n++
		default:
			if ok {
				return tool, from, n, true
			}
			cur, n, curFrom = name, 1, ev.GetSeq()
		}
		if !ok && n >= toolStreakLimit {
			tool, from, ok = cur, curFrom, true
		}
	}
	return tool, from, n, ok
}

func lowerEnum(name, prefix string) string {
	return strings.ToLower(strings.TrimPrefix(name, prefix))
}

// classifyRun applies rules 1b to 5, which need only the run's state.
func classifyRun(state *eventlog.RunState, events []*adlcv1.Event) (FailureCause, string, bool) {
	stop := state.StopReason
	switch {
	case stop == adlcv1.StopReason_STOP_REASON_CANCELLED:
		return CauseInfra, "run cancelled", true
	case stop == adlcv1.StopReason_STOP_REASON_BUDGET_EXHAUSTED:
		kind := lowerEnum(state.ExhaustedBudget.String(), "BUDGET_KIND_")
		return CauseBudget, oneLine(kind + ": " + state.StopDetail), true
	case stop == adlcv1.StopReason_STOP_REASON_ERROR && state.StopDetail == "context budget exceeded":
		return CauseContextTooLarge, oneLine(fmt.Sprintf("context budget exceeded at step %d", state.Step)), true
	case stop == adlcv1.StopReason_STOP_REASON_ERROR, stop == adlcv1.StopReason_STOP_REASON_DENIED,
		stop == adlcv1.StopReason_STOP_REASON_AWAITING_APPROVAL:
		ev := lowerEnum(stop.String(), "STOP_REASON_")
		if state.StopDetail != "" {
			ev += ": " + state.StopDetail
		}
		return CauseRefusedOrError, oneLine(ev), true
	}
	if tool, from, n, ok := toolStreak(events); ok {
		return CauseToolMisuse, oneLine(fmt.Sprintf("%s failed %d times in a row from seq %d", tool, n, from)), true
	}
	return "", "", false
}

// firstLine returns the first line of out that match accepts.
func firstLine(out string, match func(string) bool) string {
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); match(l) {
			return l
		}
	}
	return ""
}

func checkEvidence(prefix, line string, c Check) string {
	switch {
	case line != "":
		return prefix + ": " + line
	case c.TimedOut:
		return prefix + " timed out"
	default:
		return fmt.Sprintf("%s exit %d", prefix, c.ExitCode)
	}
}

func joinParts(parts [][2]string) string {
	var out []string
	for _, p := range parts {
		if p[1] != "" {
			out = append(out, p[0]+": "+p[1])
		}
	}
	return strings.Join(out, "; ")
}

// classifyGrade applies rules 6 to 13, which need only the grade.
func classifyGrade(r Result, state *eventlog.RunState) (FailureCause, string) {
	g := r.Grade
	h := r.Hidden
	switch {
	case g == nil:
		return CauseUnclassified, "result has no grade"
	case g.FilesChanged == 0:
		if state.Result == "" {
			return CauseGaveUp, "no files changed; no final answer"
		}
		return CauseGaveUp, oneLine(fmt.Sprintf("no files changed; final answer: %q", cutBytes(state.Result, answerMaxBytes)))
	case g.TestFilesChanged == 0:
		return CauseNoRepro, oneLine(fmt.Sprintf("no _test.go file changed (%d files changed)", g.FilesChanged))
	case !g.Build.OK:
		line := firstLine(g.Build.Output, func(l string) bool { return l != "" && !strings.HasPrefix(l, "#") })
		return CauseBrokeBuild, oneLine(checkEvidence("go build", line, g.Build))
	case !g.OtherTests.OK:
		line := firstLine(g.OtherTests.Output, func(l string) bool { return strings.HasPrefix(l, "--- FAIL:") })
		if line == "" {
			line = firstLine(g.OtherTests.Output, func(l string) bool { return strings.HasPrefix(l, "FAIL") })
		}
		return CauseBrokeOtherTests, oneLine(checkEvidence("go test", line, g.OtherTests))
	case !g.HiddenPass && len(h.Passed) > 0:
		return CausePartialFix, oneLine(joinParts([][2]string{
			{"hidden passed", strings.Join(h.Passed, ", ")},
			{"failed", strings.Join(h.Failed, ", ")},
			{"missing", strings.Join(h.Missing, ", ")},
		}))
	case !g.HiddenPass:
		return CauseWrongDiagnosis, oneLine(wrongDiagnosisEvidence(h))
	}
	return CauseUnclassified, "no rule matched"
}

func wrongDiagnosisEvidence(h TestOutcome) string {
	failed, missing := strings.Join(h.Failed, ", "), strings.Join(h.Missing, ", ")
	switch {
	case failed != "" && missing != "":
		return "hidden failed: " + failed + "; missing: " + missing
	case failed != "":
		return "hidden failed: " + failed
	case missing != "":
		return "hidden missing: " + missing
	}
	if h.TimedOut {
		return "hidden tests timed out"
	}
	return "hidden tests exit " + strconv.Itoa(h.ExitCode)
}

// Classify applies the rules to one unsolved result. events is the run's log;
// it is not read (and may be nil) when r.Error != "".
func Classify(r Result, events []*adlcv1.Event) (FailureCause, string, error) {
	if r.Error != "" {
		return CauseInfra, oneLine(r.Error), nil
	}
	state, err := eventlog.Fold(events)
	if err != nil {
		return "", "", fmt.Errorf("evals: failures: run %q: %w", r.RunID, err)
	}
	if cause, ev, ok := classifyRun(state, events); ok {
		return cause, ev, nil
	}
	cause, ev := classifyGrade(r, state)
	return cause, ev, nil
}

// AnalyzeFailures classifies every result of rep that is not solved.
func AnalyzeFailures(ctx context.Context, rep Report, log EventReader) (FailureReport, error) {
	fr := FailureReport{Failures: []Failure{}, ByAgent: map[string]map[FailureCause]int{}}
	for _, r := range rep.Results {
		if r.Solved() {
			continue
		}
		var events []*adlcv1.Event
		if r.Error == "" {
			var err error
			if events, err = log.Read(ctx, r.RunID, 0); err != nil {
				return FailureReport{}, fmt.Errorf("evals: failures: read run %q: %w", r.RunID, err)
			}
			if len(events) == 0 {
				return FailureReport{}, fmt.Errorf("evals: failures: %w: %q", eventlog.ErrRunNotFound, r.RunID)
			}
		}
		cause, ev, err := Classify(r, events)
		if err != nil {
			return FailureReport{}, err
		}
		fr.Failures = append(fr.Failures, Failure{
			BugID: r.BugID, Agent: r.Agent, Provider: r.Provider, RunID: r.RunID, Cause: cause, Evidence: ev,
		})
		if fr.ByAgent[r.Agent] == nil {
			fr.ByAgent[r.Agent] = map[FailureCause]int{}
		}
		fr.ByAgent[r.Agent][cause]++
	}
	return fr, nil
}

func causeRank(c FailureCause) int {
	for i, x := range FailureCauses {
		if x == c {
			return i
		}
	}
	return len(FailureCauses)
}

// PrintFailures writes the two tables of fr.
func PrintFailures(w io.Writer, fr FailureReport) {
	if len(fr.Failures) == 0 {
		fmt.Fprintln(w, "failures: none")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "AGENT\tCAUSE\tCOUNT")
	agents := make([]string, 0, len(fr.ByAgent))
	for a := range fr.ByAgent {
		agents = append(agents, a)
	}
	sort.Strings(agents)
	for _, a := range agents {
		causes := make([]FailureCause, 0, len(fr.ByAgent[a]))
		for c, n := range fr.ByAgent[a] {
			if n > 0 {
				causes = append(causes, c)
			}
		}
		sort.SliceStable(causes, func(i, j int) bool {
			ni, nj := fr.ByAgent[a][causes[i]], fr.ByAgent[a][causes[j]]
			if ni != nj {
				return ni > nj
			}
			return causeRank(causes[i]) < causeRank(causes[j])
		})
		for _, c := range causes {
			fmt.Fprintf(tw, "%s\t%s\t%d\n", a, c, fr.ByAgent[a][c])
		}
	}
	_ = tw.Flush()
	fmt.Fprintln(w)
	tw = tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "BUG\tAGENT\tCAUSE\tRUN\tEVIDENCE")
	for _, f := range fr.Failures {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", f.BugID, f.Agent, f.Cause, f.RunID, f.Evidence)
	}
	_ = tw.Flush()
}
