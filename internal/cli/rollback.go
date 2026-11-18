package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newRollbackCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rollback <id> [version]",
		Short: "Switch an app back to a previously installed version",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := newService()
			if err != nil {
				return err
			}
			target := ""
			if len(args) == 2 {
				target = args[1]
			}
			res, err := svc.Rollback(args[0], target)
			if err != nil {
				return err
			}
			for _, w := range res.Warnings {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", w)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %s -> %s\n", res.ID, res.Previous, res.Version)
			return nil
		},
	}
}
