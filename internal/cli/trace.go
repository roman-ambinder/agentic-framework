package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/roman-ambinder/agentic-framework/internal/observability"
)

func newTraceCmd() *cobra.Command {
	var db string
	var otelStdout bool
	cmd := &cobra.Command{
		Use:   "trace RUN_ID",
		Short: "Print a run as a span tree",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := traceRun(cmd, db, args[0], otelStdout); err != nil {
				return &exitError{code: 1, err: err}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&db, "db", defaultDBPath, "event log file")
	f.BoolVar(&otelStdout, "otel-stdout", false, "print OpenTelemetry spans as JSON instead of the tree")
	return cmd
}

func traceRun(cmd *cobra.Command, db, runID string, otelStdout bool) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	store, err := openExistingLog(ctx, db)
	if err != nil {
		return err
	}
	defer store.Close()
	events, err := store.Read(ctx, runID, 0)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return fmt.Errorf("run %q not found", runID)
	}
	root, err := observability.Build(events)
	if err != nil {
		return err
	}
	if !otelStdout {
		return observability.WriteTree(cmd.OutOrStdout(), root)
	}
	exp, err := stdouttrace.New(stdouttrace.WithWriter(cmd.OutOrStdout()), stdouttrace.WithPrettyPrint())
	if err != nil {
		return fmt.Errorf("trace: stdout exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp),
		sdktrace.WithIDGenerator(observability.NewIDGenerator(runID)))
	if err := observability.Export(ctx, root, tp); err != nil {
		_ = tp.Shutdown(ctx)
		return err
	}
	return tp.Shutdown(ctx)
}
