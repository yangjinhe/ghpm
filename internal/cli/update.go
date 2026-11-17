package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"ghpm/internal/app"
)

func newUpdateCmd() *cobra.Command {
	var (
		all    bool
		yes    bool
		osName string
		arch   string
		force  bool
	)
	cmd := &cobra.Command{
		Use:   "update [id...]",
		Short: "Install available updates",
		Long: `Checks for updates, shows what will change, and installs the selected ones.
Pinned apps are skipped unless named explicitly.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := newService()
			if err != nil {
				return err
			}
			p, err := targetPlatform(osName, arch)
			if err != nil {
				return err
			}
			ids := args
			if len(ids) > 0 {
				if err := requireConfigured(svc, ids); err != nil {
					return err
				}
			} else {
				pf := newProgressPrinter()
				results := svc.CheckFresh(cmd.Context(), nil, pf.Event)
				pf.Stop()
				var updates []app.CheckResult
				for _, r := range results {
					if r.Err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s: %v\n", r.ID, r.Err)
						continue
					}
					if r.Pinned {
						continue
					}
					if all || r.UpdateAvailable {
						updates = append(updates, r)
					}
				}
				if len(updates) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "Everything is up to date.")
					return nil
				}
				for _, r := range updates {
					cur := r.InstalledVersion
					if cur == "" {
						cur = "(not installed)"
					}
					fmt.Fprintf(cmd.OutOrStdout(), "  %-16s %s -> %s\n", r.ID, cur, r.LatestTag)
				}
				if !yes && !confirm(fmt.Sprintf("Update %d app(s)?", len(updates))) {
					fmt.Fprintln(cmd.ErrOrStderr(), "aborted")
					return nil
				}
				for _, r := range updates {
					ids = append(ids, r.ID)
				}
			}
			if len(ids) == 0 {
				return nil
			}
			pf := newProgressPrinter()
			defer pf.Stop()
			results := svc.InstallMany(cmd.Context(), ids, app.InstallOptions{
				Platform: p, Force: force, Progress: pf.Event,
			})
			return reportInstallResults(results)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "update every configured app, not only those known to be outdated")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	cmd.Flags().StringVar(&osName, "os", "", "target operating system (default: this machine)")
	cmd.Flags().StringVar(&arch, "arch", "", "target architecture (default: this machine)")
	cmd.Flags().BoolVar(&force, "force", false, "replace files and symlinks not owned by ghpm")
	return cmd
}
