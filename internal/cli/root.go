// Package cli wires the cobra command tree for mcpbench.
package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// Version is the mcpbench CLI version. Set via -ldflags during build.
var Version = "0.1.0"

// NewRootCmd builds the top-level cobra command.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "mcpbench",
		Short: "Protocol-aware load tester and benchmarker for MCP servers",
		Long: `mcpbench is a concurrent JSON-RPC 2.0 load tester for MCP (Model Context
Protocol) servers. It fires requests against a stdio or HTTP/SSE MCP server,
measures per-tool latency distributions (p50/p95/p99), throughput, and error
rate, then produces machine-readable output for CI regression gating.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(NewRunCmd())
	root.AddCommand(NewCompareCmd())
	root.AddCommand(NewScenarioValidateCmd())
	root.AddCommand(NewVersionCmd())
	return root
}

// NewVersionCmd prints the CLI version.
func NewVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the mcpbench version",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "mcpbench", Version)
			return err
		},
	}
}

// Execute runs the root command, directing I/O to the provided streams.
// It returns an error rather than calling os.Exit so that callers (and
// tests) can decide how to surface the failure.
func Execute(args []string, stdout, stderr io.Writer) error {
	cmd := NewRootCmd()
	cmd.SetArgs(args)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	return cmd.Execute()
}
