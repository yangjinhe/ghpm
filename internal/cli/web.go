package cli

import (
	"fmt"
	"net"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"

	"ghpm/internal/web"
)

func newWebCmd() *cobra.Command {
	var (
		listen      string
		open        bool
		allowRemote bool
	)
	cmd := &cobra.Command{
		Use:   "web",
		Short: "Serve a local browser interface",
		Long: `Starts a small HTTP server and serves a browser UI for the configured apps.

The server binds to loopback by default, rejects requests with non-loopback
Host headers, and only accepts JSON writes. Pass --allow-remote to lift those
restrictions (place it behind a reverse proxy with authentication if you do).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := newService()
			if err != nil {
				return err
			}
			ln, err := net.Listen("tcp", listen)
			if err != nil {
				return fmt.Errorf("listen %s: %w", listen, err)
			}
			url := web.Addr(ln)
			fmt.Fprintf(cmd.ErrOrStderr(), "ghpm web listening on %s (Ctrl-C to stop)\n", url)
			if allowRemote {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning: --allow-remote disables the loopback guard; use a reverse proxy with authentication")
			}
			if open {
				if err := openBrowser(url); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not open a browser: %v\n", err)
				}
			}
			return web.New(svc).Serve(cmd.Context(), ln, allowRemote)
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:8787", "address to listen on (127.0.0.1:8787)")
	cmd.Flags().BoolVar(&open, "open", false, "open the interface in the default browser")
	cmd.Flags().BoolVar(&allowRemote, "allow-remote", false, "accept non-loopback requests")
	return cmd
}

func openBrowser(url string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name = "open"
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		name = "xdg-open"
	}
	return exec.Command(name, append(args, url)...).Start()
}
