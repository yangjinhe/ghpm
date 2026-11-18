package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newPinCmd() *cobra.Command {
	var unpin bool
	cmd := &cobra.Command{
		Use:   "pin <id> [version]",
		Short: "Exclude an app from update checks",
		Long: `Pins an app so ` + "`ghpm check`" + ` and ` + "`ghpm update`" + ` skip it.
With a version argument, ghpm first rolls back to that installed version.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := newService()
			if err != nil {
				return err
			}
			id, err := svc.ResolveID(args[0])
			if err != nil {
				return err
			}
			if unpin {
				if err := svc.SetPinned(id, false); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s unpinned\n", id)
				return nil
			}
			if len(args) == 2 {
				res, err := svc.Rollback(id, args[1])
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %s -> %s\n", res.ID, res.Previous, res.Version)
			}
			if err := svc.SetPinned(id, true); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s pinned\n", id)
			return nil
		},
	}
	cmd.Flags().BoolVar(&unpin, "unpin", false, "allow updates again")
	return cmd
}
