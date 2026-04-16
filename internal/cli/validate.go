package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/JSLEEKR/mcpbench/internal/scenario"
)

// NewScenarioValidateCmd builds the `mcpbench scenario-validate` subcommand.
func NewScenarioValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "scenario-validate FILE",
		Short: "Parse and validate a scenario YAML file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := scenario.Load(args[0])
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "scenario %q OK — %d tool(s)\n", s.Name, len(s.Tools))
			return err
		},
	}
}
