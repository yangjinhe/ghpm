package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newPruneCmd() *cobra.Command {
	var keep int
	var yes bool
	cmd := &cobra.Command{
		Use:   "prune [id...]",
		Short: "Delete old installed versions",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := newService()
			if err != nil {
				return err
			}
			if keep <= 0 {
				keep = svc.Cfg.Defaults.PruneKeep
			}
			if !yes && !confirm(fmt.Sprintf("Keep only the newest %d version(s)?", keep)) {
				fmt.Fprintln(cmd.ErrOrStderr(), "aborted")
				return nil
			}
			removed, err := svc.Prune(args, keep)
			if err != nil {
				return err
			}
			if len(removed) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "Nothing to prune.")
				return nil
			}
			for id, versions := range removed {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: removed %v\n", id, versions)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&keep, "keep", 0, "number of versions to keep (default from config)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}
