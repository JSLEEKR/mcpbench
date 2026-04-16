package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/JSLEEKR/mcpbench/internal/compare"
)

type compareFlags struct {
	p95      string
	p99      string
	errDelta string
	output   string
}

// NewCompareCmd builds the `mcpbench compare` subcommand.
func NewCompareCmd() *cobra.Command {
	f := &compareFlags{}
	cmd := &cobra.Command{
		Use:   "compare BASELINE.json CANDIDATE.json",
		Short: "Diff two run files and fail on regression",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return compareExecute(cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], args[1], f)
		},
	}
	cmd.Flags().StringVar(&f.p95, "threshold-p95", "+20%", "allowed p95 regression (e.g. +20%, +10ms)")
	cmd.Flags().StringVar(&f.p99, "threshold-p99", "+25%", "allowed p99 regression")
	cmd.Flags().StringVar(&f.errDelta, "threshold-error", "+2pp", "allowed error-rate regression (pp)")
	cmd.Flags().StringVar(&f.output, "output", "text", "output format: text|json")
	return cmd
}

func compareExecute(stdout, stderr io.Writer, aPath, bPath string, f *compareFlags) error {
	cfg := compare.Default()
	if f.p95 != "" {
		th, err := compare.ParseThreshold(f.p95)
		if err != nil {
			return err
		}
		cfg.P95Threshold = &th
	}
	if f.p99 != "" {
		th, err := compare.ParseThreshold(f.p99)
		if err != nil {
			return err
		}
		cfg.P99Threshold = &th
	}
	if f.errDelta != "" {
		th, err := compare.ParseThreshold(f.errDelta)
		if err != nil {
			return err
		}
		cfg.ErrorThreshold = &th
	}

	baseline, err := compare.LoadRun(aPath)
	if err != nil {
		return err
	}
	candidate, err := compare.LoadRun(bPath)
	if err != nil {
		return err
	}
	rpt := compare.Compare(baseline, candidate, cfg)

	switch f.output {
	case "", "text":
		rpt.WriteText(stdout)
	case "json":
		if err := rpt.WriteJSON(stdout); err != nil {
			return err
		}
	default:
		return fmt.Errorf("compare: unknown output %q", f.output)
	}
	if rpt.Regressed {
		// Cobra's SilenceErrors prevents the usage print, but we still want a
		// non-zero exit. Return a sentinel error so Execute's caller (main)
		// can translate to exit code 1.
		return ErrRegression
	}
	// Ensure stderr is not empty when no output-file is unknown; we just use
	// stdout/stderr.
	_ = stderr
	_ = os.Stdout
	return nil
}

// ErrRegression is returned by the compare command when one or more
// thresholds are exceeded. main.go translates this to exit code 1 without
// printing a duplicate message.
var ErrRegression = errSentinel("mcpbench: regression detected")

type errSentinel string

func (e errSentinel) Error() string { return string(e) }
