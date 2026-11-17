package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"ghpm/internal/app"
)

func newInstallCmd() *cobra.Command {
	var (
		all    bool
		osName string
		arch   string
		tag    string
		force  bool
	)
	cmd := &cobra.Command{
		Use:   "install [id...]",
		Short: "Download and install configured apps",
		Long: `Installs the named apps. With no arguments only missing apps are installed;
--all installs or upgrades every configured app.`,
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
			switch {
			case len(ids) == 0 && all:
				ids = svc.ConfiguredIDs()
			case len(ids) == 0:
				ids = svc.MissingIDs()
				if len(ids) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "Everything configured is installed. Use --all to upgrade.")
					return nil
				}
			}
			if err := requireConfigured(svc, ids); err != nil {
				return err
			}
			pf := newProgressPrinter()
			defer pf.Stop()
			results := svc.InstallMany(cmd.Context(), ids, app.InstallOptions{
				Platform: p, Tag: tag, Force: force, Progress: pf.Event,
			})
			return reportInstallResults(results)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "install or upgrade every configured app")
	cmd.Flags().StringVar(&osName, "os", "", "target operating system (default: this machine)")
	cmd.Flags().StringVar(&arch, "arch", "", "target architecture (default: this machine)")
	cmd.Flags().StringVar(&tag, "tag", "", "install a specific tag instead of the configured channel")
	cmd.Flags().BoolVar(&force, "force", false, "replace files and symlinks not owned by ghpm")
	return cmd
}

func requireConfigured(svc *app.Service, ids []string) error {
	for _, id := range ids {
		if svc.Cfg.AppByID(id) == nil {
			return fmt.Errorf("app %q is not configured; run `ghpm add <owner/repo>`", id)
		}
	}
	return nil
}

func reportInstallResults(results []app.InstallResult) error {
	if flags.json {
		return printJSON(os.Stdout, resultsJSON(results))
	}
	failures := 0
	for _, r := range results {
		for _, w := range r.Warnings {
			fmt.Fprintf(os.Stderr, "warning: %s: %s\n", r.ID, w)
		}
		if r.Err != nil {
			failures++
		}
	}
	if failures > 0 {
		return fmt.Errorf("%d of %d operation(s) failed", failures, len(results))
	}
	return nil
}

type installResultJSON struct {
	ID       string   `json:"id"`
	Version  string   `json:"version"`
	Tag      string   `json:"tag,omitempty"`
	Previous string   `json:"previous,omitempty"`
	Asset    string   `json:"asset"`
	Type     string   `json:"install_type,omitempty"`
	SHA256   string   `json:"sha256,omitempty"`
	Link     string   `json:"link,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Removed  []string `json:"removed_versions,omitempty"`
	Error    string   `json:"error,omitempty"`
}

func resultsJSON(results []app.InstallResult) []installResultJSON {
	out := make([]installResultJSON, len(results))
	for i, r := range results {
		out[i] = installResultJSON{
			ID: r.ID, Version: r.Version, Tag: r.Tag, Previous: r.Previous,
			Asset: r.Asset, Type: r.Type, SHA256: r.SHA256, Link: r.Link,
			Warnings: r.Warnings, Removed: r.Removed,
		}
		if r.Err != nil {
			out[i].Error = r.Err.Error()
		}
	}
	return out
}
