package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/costreport"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
)

// costOptions are the flags of `adlc cost`.
type costOptions struct {
	db, by string
	asJSON bool
}

func newCostCmd() *cobra.Command {
	var o costOptions
	cmd := &cobra.Command{
		Use:   "cost [RUN_ID ...]",
		Short: "Report tokens and cost per run, model or agent",
		Long: "Report tokens and cost from the event log. With no run ids, all runs " +
			"in the log are included. Totals are the sums of the events' cost fields.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return costReport(cmd.Context(), o, args, cmd.OutOrStdout())
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.db, "db", ".adlc/events.db", "event log file")
	f.StringVar(&o.by, "by", "run", "group rows by run, model or agent")
	f.BoolVar(&o.asJSON, "json", false, "print as JSON")
	return cmd
}

// costReport builds the cost report for ids (all runs if none) and prints it.
func costReport(ctx context.Context, o costOptions, ids []string, stdout io.Writer) error {
	store, err := eventlog.Open(ctx, o.db)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	defer store.Close()
	runs, err := loadRuns(ctx, store, ids)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	rep, err := costreport.Build(costreport.By(o.by), runs)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	if o.asJSON {
		err = costreport.WriteJSON(stdout, rep)
	} else {
		err = costreport.WriteText(stdout, rep)
	}
	if err != nil {
		return &exitError{code: 1, err: fmt.Errorf("cost: write report: %w", err)}
	}
	return nil
}

// loadRuns returns the events of each run in ids, or of every run in the
// store when ids is empty. A named run with no events is an error.
func loadRuns(ctx context.Context, store *eventlog.Store, ids []string) (map[string][]*adlcv1.Event, error) {
	if len(ids) == 0 {
		all, err := store.Runs(ctx)
		if err != nil {
			return nil, err
		}
		ids = all
	}
	runs := make(map[string][]*adlcv1.Event, len(ids))
	for _, id := range ids {
		events, err := store.Read(ctx, id, 0)
		if err != nil {
			return nil, err
		}
		if len(events) == 0 {
			return nil, fmt.Errorf("run %q not found", id)
		}
		runs[id] = events
	}
	return runs, nil
}
