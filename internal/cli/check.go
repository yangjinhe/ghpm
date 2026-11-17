package cli

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"ghpm/internal/app"
)

type checkJSON struct {
	ID               string `json:"id"`
	InstalledVersion string `json:"installed_version,omitempty"`
	LatestVersion    string `json:"latest_version,omitempty"`
	LatestTag        string `json:"latest_tag,omitempty"`
	UpdateAvailable  bool   `json:"update_available"`
	Comparable       bool   `json:"comparable"`
	Pinned           bool   `json:"pinned"`
	Error            string `json:"error,omitempty"`
}

func newCheckCmd() *cobra.Command {
	var notify bool
	cmd := &cobra.Command{
		Use:   "check [id...]",
		Short: "Check GitHub for newer releases",
		Long:  "Fetches the latest release of every configured app (or the named ones) and records available updates.",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := newService()
			if err != nil {
				return err
			}
			pf := newProgressPrinter()
			results := svc.Check(cmd.Context(), args, pf.Event)
			pf.Stop()

			if flags.json {
				out := make([]checkJSON, len(results))
				for i, r := range results {
					out[i] = checkJSON{
						ID: r.ID, InstalledVersion: r.InstalledVersion,
						LatestVersion: r.LatestVersion, LatestTag: r.LatestTag,
						UpdateAvailable: r.UpdateAvailable, Comparable: r.Comparable,
						Pinned: r.Pinned,
					}
					if r.Err != nil {
						out[i].Error = r.Err.Error()
					}
				}
				if err := printJSON(os.Stdout, out); err != nil {
					return err
				}
			} else {
				w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
				fmt.Fprintln(w, "ID\tINSTALLED\tLATEST\tSTATUS")
				for _, r := range results {
					status := "up to date"
					switch {
					case r.Err != nil:
						status = "error: " + r.Err.Error()
					case r.Pinned:
						status = "pinned"
					case !r.Installed:
						status = "not installed"
					case r.UpdateAvailable && r.Comparable:
						status = "update available"
					case r.UpdateAvailable:
						status = "different version"
					}
					cur := r.InstalledVersion
					if cur == "" {
						cur = "-"
					}
					latest := r.LatestTag
					if latest == "" {
						latest = "-"
					}
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.ID, cur, latest, status)
				}
				if err := w.Flush(); err != nil {
					return err
				}
			}

			failed := 0
			updates := 0
			for _, r := range results {
				if r.Err != nil {
					failed++
				}
				if r.UpdateAvailable && !r.Pinned {
					updates++
				}
			}
			if !flags.json {
				fmt.Fprintf(cmd.OutOrStdout(), "\n%d app(s) checked, %d update(s) available\n", len(results), updates)
			}
			if notify {
				var updates []string
				for _, r := range results {
					if r.UpdateAvailable && !r.Pinned && r.Err == nil {
						updates = append(updates, r.ID)
					}
				}
				if len(updates) > 0 {
					if err := notifyUpdates(updates); err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v\n", err)
					}
				}
			}
			if failed > 0 {
				return app.CheckErrors(results)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&notify, "notify", false, "send a desktop notification when updates are available")
	return cmd
}
