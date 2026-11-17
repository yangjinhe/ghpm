package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newRemoveCmd() *cobra.Command {
	var (
		purge bool
		yes   bool
	)
	cmd := &cobra.Command{
		Use:   "remove <id...>",
		Short: "Uninstall apps and their files",
		Long:  "Removes installed versions, the symlink and the state record. --purge also deletes the app from the config.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := newService()
			if err != nil {
				return err
			}
			if !yes && !confirm(fmt.Sprintf("Remove %d app(s)?", len(args))) {
				fmt.Fprintln(cmd.ErrOrStderr(), "aborted")
				return nil
			}
			for _, id := range args {
				removed, err := svc.Remove(id, purge)
				if err != nil {
					return fmt.Errorf("%s: %w", id, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s removed (%d path(s))\n", id, len(removed))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "also remove the app from the config file")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}
