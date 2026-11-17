package cli

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"ghpm/internal/app"
)

type listRow struct {
	ID              string `json:"id"`
	Repo            string `json:"repo"`
	Channel         string `json:"channel,omitempty"`
	Installed       bool   `json:"installed"`
	CurrentVersion  string `json:"current_version,omitempty"`
	LatestVersion   string `json:"latest_version,omitempty"`
	LatestTag       string `json:"latest_tag,omitempty"`
	UpdateAvailable bool   `json:"update_available"`
	Pinned          bool   `json:"pinned"`
	Status          string `json:"status"`
	LinkPath        string `json:"link_path,omitempty"`
	CheckError      string `json:"check_error,omitempty"`
}

func newListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured and installed apps",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := newService()
			if err != nil {
				return err
			}
			views, err := svc.Views()
			if err != nil {
				return err
			}
			rows := make([]listRow, 0, len(views))
			for _, v := range views {
				rows = append(rows, listRow{
					ID: v.ID, Repo: v.Repo, Channel: v.Channel,
					Installed: v.Installed, CurrentVersion: v.CurrentVersion,
					LatestVersion: v.LatestVersion, LatestTag: v.LatestTag,
					UpdateAvailable: v.UpdateAvailable, Pinned: v.Pinned,
					Status: statusText(v), LinkPath: v.LinkPath, CheckError: v.CheckError,
				})
			}
			if flags.json {
				return printJSON(os.Stdout, rows)
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tINSTALLED\tLATEST\tSTATUS\tREPO")
			for _, r := range rows {
				cur := r.CurrentVersion
				if !r.Installed {
					cur = "-"
				}
				latest := r.LatestTag
				if latest == "" {
					latest = "-"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.ID, cur, latest, r.Status, r.Repo)
			}
			return w.Flush()
		},
	}
}

func statusText(v app.AppView) string {
	if !v.Installed {
		return "not installed"
	}
	if v.Pinned {
		return "pinned"
	}
	if v.CheckError != "" {
		return "check failed"
	}
	if v.UpdateAvailable {
		if !v.Comparable {
			return "different version"
		}
		return "update available"
	}
	if v.LatestVersion == "" {
		return "not checked"
	}
	return "up to date"
}
