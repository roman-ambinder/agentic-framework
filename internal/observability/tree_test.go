package observability

import (
	"bytes"
	"testing"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
)

const fullRunTree = `run r1  [completed]  +0s  6s  in=150 out=30 $0.000750
├─ model m1  [tool_use]  +1s  1s  fake/fake-1 in=100 out=20 $0.000500
├─ tool t1 fs__read_file  [ok]  +2s  1s  read allow
│    · policy +2s adlc.decision=DECISION_ALLOW adlc.rule_id=r-read adlc.reason= adlc.source=DECISION_SOURCE_LOCAL
└─ model m2  [end_turn]  +4s  1s  fake/fake-1 in=50 out=10 $0.000250
`

func render(t *testing.T) string {
	t.Helper()
	root, err := Build(fullRun())
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteTree(&buf, root); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestWriteTreeGolden(t *testing.T) {
	if got := render(t); got != fullRunTree {
		t.Errorf("tree mismatch\n got:\n%s\nwant:\n%s", got, fullRunTree)
	}
}

func TestWriteTreeDeterministic(t *testing.T) {
	if a, b := render(t), render(t); a != b {
		t.Errorf("two renders differ:\n%s\n%s", a, b)
	}
}

func TestWriteTreeRunningRun(t *testing.T) {
	root, err := Build(numbered(runStarted(), toolRequested(1*sec)))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteTree(&buf, root); err != nil {
		t.Fatal(err)
	}
	want := "run r1  [running]  +0s  -  in=0 out=0 $0.000000\n" +
		"└─ tool t1 fs__read_file  [in flight]  +1s  -  read -\n"
	if buf.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestWriteTreeLastChildAndRootEvents(t *testing.T) {
	root, err := Build(numbered(
		runStarted(),
		errorOccurred(1*sec),
		toolRequested(2*sec),
		policy(2*sec, "t1", adlcv1.Decision_DECISION_DENY, "r-deny"),
		runCompleted(3*sec, adlcv1.StopReason_STOP_REASON_ERROR),
	))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteTree(&buf, root); err != nil {
		t.Fatal(err)
	}
	want := "run r1  [error]  +0s  3s  in=0 out=0 $0.000000\n" +
		"  · error +1s adlc.error_class=ERROR_CLASS_UNSPECIFIED adlc.message=boom adlc.component=\n" +
		"└─ tool t1 fs__read_file  [denied]  +2s  0s  read deny\n" +
		"     · policy +2s adlc.decision=DECISION_DENY adlc.rule_id=r-deny adlc.reason= adlc.source=DECISION_SOURCE_LOCAL\n"
	if buf.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", buf.String(), want)
	}
}
