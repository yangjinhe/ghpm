package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check paths, permissions, token and rate limit",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := newService()
			if err != nil {
				return err
			}
			d := svc.Diagnostics()
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "version:    %s\n", d.Version)
			fmt.Fprintf(out, "platform:   %s/%s\n", d.OS, d.Arch)
			fmt.Fprintf(out, "config:     %s\n", d.ConfigPath)
			fmt.Fprintf(out, "data dir:   %s\n", d.DataDir)
			fmt.Fprintf(out, "bin dir:    %s\n", d.BinDir)
			fmt.Fprintf(out, "cache dir:  %s\n", d.CacheDir)
			fmt.Fprintf(out, "apps:       %d configured, %d installed\n", d.Apps, d.Installed)
			fmt.Fprintf(out, "token:      %s\n", yesNo(d.TokenSet))
			fmt.Fprintf(out, "bin on PATH: %s\n", yesNo(d.BinOnPath))
			if d.RateLimit.Limit > 0 {
				fmt.Fprintf(out, "rate limit: %d/%d remaining", d.RateLimit.Remaining, d.RateLimit.Limit)
				if !d.RateLimit.Reset.IsZero() {
					fmt.Fprintf(out, " (resets %s)", d.RateLimit.Reset.Local().Format("15:04:05"))
				}
				fmt.Fprintln(out)
			}
			if len(d.Issues) == 0 {
				fmt.Fprintln(out, "\nNo issues found.")
				return nil
			}
			fmt.Fprintln(out, "\nIssues:")
			for _, issue := range d.Issues {
				fmt.Fprintf(out, "  - %s\n", issue)
			}
			return nil
		},
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
