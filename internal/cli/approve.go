package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
	"github.com/roman-ambinder/agentic-framework/internal/harness"
)

// summaryMaxRunes is the longest SUMMARY shown by `adlc approvals`.
const summaryMaxRunes = 80

func newApprovalsCmd() *cobra.Command {
	var db string
	cmd := &cobra.Command{
		Use:   "approvals",
		Short: "List the approvals that paused runs are waiting for",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return listApprovals(cmd.Context(), db, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&db, "db", defaultDBPath, "event log file")
	return cmd
}

// listApprovals prints one row per pending approval of every paused run.
func listApprovals(ctx context.Context, db string, w io.Writer) error {
	store, err := openExistingLog(ctx, db)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	defer store.Close()
	ids, err := store.Runs(ctx)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	var rows []string
	for _, id := range ids {
		r, err := pendingRows(ctx, store, id)
		if err != nil {
			return &exitError{code: 1, err: err}
		}
		rows = append(rows, r...)
	}
	if len(rows) == 0 {
		fmt.Fprint(w, "no pending approvals\n")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", "APPROVAL", "RUN", "CALL", "AGENT", "REQUESTED", "SUMMARY")
	for _, r := range rows {
		fmt.Fprint(tw, r)
	}
	return tw.Flush()
}

// pendingRows returns the table rows of one run; none unless it is paused.
func pendingRows(ctx context.Context, store *eventlog.Store, runID string) ([]string, error) {
	events, err := store.Read(ctx, runID, 0)
	if err != nil {
		return nil, fmt.Errorf("approvals: run %q: %w", runID, err)
	}
	state, err := eventlog.Fold(events)
	if err != nil {
		return nil, fmt.Errorf("approvals: run %q: %w", runID, err)
	}
	if state.Status != eventlog.StatusPaused {
		return nil, nil
	}
	var rows []string
	for _, aid := range state.PendingApprovals {
		for _, ev := range events {
			req := ev.GetApprovalRequested()
			if req.GetApprovalId() != aid {
				continue
			}
			rows = append(rows, fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s\n", aid, runID, req.GetCallId(), state.AgentID,
				ev.GetTime().AsTime().UTC().Format(time.RFC3339), oneLine(req.GetSummary())))
		}
	}
	return rows, nil
}

// oneLine replaces line breaks and tabs with spaces and shortens s to
// summaryMaxRunes runes.
func oneLine(s string) string {
	s = strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(s)
	if r := []rune(s); len(r) > summaryMaxRunes {
		return string(r[:summaryMaxRunes-3]) + "..."
	}
	return s
}

// approveOptions are the flags of `adlc approve`.
type approveOptions struct {
	runOptions
	reject  bool
	comment string
	by      string
}

func newApproveCmd() *cobra.Command {
	var o approveOptions
	cmd := &cobra.Command{
		Use:   "approve APPROVAL_ID RUN_ID [AGENT_YAML|NAME@N|NAME@ENV]",
		Short: "Approve or reject a tool call and continue the paused run",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 3 {
				o.agentPath = args[2]
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return approveRun(ctx, o, args[0], args[1], cmd.OutOrStdout(), openSandboxBackend)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&o.reject, "reject", false, "reject the call instead of approving it")
	f.StringVar(&o.comment, "comment", "", "comment recorded with the answer")
	f.StringVar(&o.by, "by", "", "who answers (default: $USER, else \"local\")")
	f.StringVar(&o.db, "db", defaultDBPath, "event log file")
	f.StringVar(&o.controlDB, "control-db", defaultControlDBPath, "control database (kill switches)")
	f.StringVar(&o.fakeScript, "fake-script", "", "fake model script (provider fake only)")
	f.StringVar(&o.modelName, "model-name", "", "overrides model.name from the agent file")
	return cmd
}

// approverName returns who answers: the flag, else $USER, else "local".
func approverName(flag string, getenv func(string) string) string {
	if flag != "" {
		return flag
	}
	if u := getenv("USER"); u != "" {
		return u
	}
	return "local"
}

// approveRun records the answer to one approval and, when it was the last one
// the run waited for, continues the run and prints its summary.
func approveRun(ctx context.Context, o approveOptions, approvalID, runID string, stdout io.Writer,
	openSandbox func(context.Context) (sandboxLookup, error),
) error {
	store, err := openExistingLog(ctx, o.db)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	defer store.Close()
	events, err := store.Read(ctx, runID, 0)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	st, err := harness.CheckApproval(events, runID, approvalID)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	res := harness.Resolution{
		ApprovalID: approvalID, Approved: !o.reject, By: approverName(o.by, os.Getenv), Comment: o.comment,
	}
	if len(st.PendingApprovals) > 1 {
		return resolveOnly(ctx, store, runID, res, len(st.PendingApprovals)-1, stdout)
	}
	if o.agentPath == "" {
		return &exitError{code: 1, err: fmt.Errorf("approve: AGENT_YAML is required to continue run %q", runID)}
	}
	state, err := continueRun(ctx, o, store, runID, res, openSandbox)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	printSummary(stdout, state)
	if state.StopReason == adlcv1.StopReason_STOP_REASON_COMPLETED {
		return nil
	}
	return &exitError{code: 2}
}

// resolveOnly records res for a run that still waits for other approvals.
func resolveOnly(ctx context.Context, store *eventlog.Store, runID string, res harness.Resolution, left int, w io.Writer) error {
	if _, err := harness.ResolveApproval(ctx, store, runID, res); err != nil {
		return &exitError{code: 1, err: err}
	}
	verdict := "approved"
	if !res.Approved {
		verdict = "rejected"
	}
	fmt.Fprintf(w, "approval %s %s; run %s still waits for %d approval(s)\n", res.ApprovalID, verdict, runID, left)
	return nil
}

// continueRun builds the run as `adlc resume` does and calls harness.Approve.
func continueRun(ctx context.Context, o approveOptions, store *eventlog.Store, runID string, res harness.Resolution,
	openSandbox func(context.Context) (sandboxLookup, error),
) (*eventlog.RunState, error) {
	ro, cleanupRef, err := resolveAgentRef(ctx, o.runOptions)
	if err != nil {
		return nil, err
	}
	defer cleanupRef()
	o.runOptions = ro
	a, err := loadAgent(o.runOptions)
	if err != nil {
		return nil, err
	}
	ks, closeKS, err := openKillSwitch(ctx, o.controlDB)
	if err != nil {
		return nil, err
	}
	defer closeKS()
	if err := attachSandbox(ctx, a, runID, openSandbox); err != nil {
		return nil, err
	}
	cfg, deps, cleanup, err := buildRun(ctx, a, o.runOptions, runID, store)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cleanup() }()
	deps.KillSwitch = ks
	return harness.Approve(ctx, cfg, deps, res)
}
