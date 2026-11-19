// Package cli implements ghpm's command line interface.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"ghpm/internal/app"
	"ghpm/internal/platform"
	"ghpm/internal/version"
)

type rootFlags struct {
	configPath string
	proxy      string
	noCache    bool
	json       bool
}

var flags rootFlags

// Execute runs the CLI and returns a non-nil error on failure.
func Execute(ctx context.Context) error {
	root := newRoot()
	root.SetContext(ctx)
	return root.Execute()
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "ghpm",
		Short:         "Manage GitHub release binaries",
		Long:          "ghpm installs, updates and rolls back binaries published on GitHub releases.\nConfigure a project once, and ghpm picks the right asset for your system.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version.String(),
	}
	root.PersistentFlags().StringVarP(&flags.configPath, "config", "c", "", "config file (default ~/.config/ghpm/config.yaml)")
	root.PersistentFlags().StringVar(&flags.proxy, "proxy", "", "HTTP(S) proxy URL, overrides config")
	root.PersistentFlags().BoolVar(&flags.noCache, "no-cache", false, "bypass the release metadata cache")
	root.PersistentFlags().BoolVar(&flags.json, "json", false, "machine readable output (where supported)")

	root.AddCommand(
		newAddCmd(),
		newInstallCmd(),
		newListCmd(),
		newCheckCmd(),
		newUpdateCmd(),
		newRollbackCmd(),
		newPinCmd(),
		newRemoveCmd(),
		newPruneCmd(),
		newAssetsCmd(),
		newDoctorCmd(),
		newConfigCmd(),
		newTUICmd(),
		newWebCmd(),
		newDocCmd(),
	)
	return root
}

func newService() (*app.Service, error) {
	return app.New(app.Options{ConfigPath: flags.configPath, Proxy: flags.proxy, NoCache: flags.noCache})
}

// resolveIDs accepts app ids, owner/repo strings or GitHub URLs and returns
// deduplicated internal ids. Every command that takes an app should go
// through this so `ghpm remove owner/repo` behaves like `ghpm add owner/repo`.
func resolveIDs(svc *app.Service, args []string) ([]string, error) {
	ids := make([]string, 0, len(args))
	seen := make(map[string]bool, len(args))
	for _, arg := range args {
		id, err := svc.ResolveID(arg)
		if err != nil {
			return nil, err
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, nil
}

func targetPlatform(osName, arch string) (platform.Platform, error) {
	if osName == "" && arch == "" {
		return platform.Current(), nil
	}
	if osName == "" {
		osName = runtime.GOOS
	}
	if arch == "" {
		arch = runtime.GOARCH
	}
	p := platform.Parse(osName, arch)
	if p.OS == "" || p.Arch == "" {
		return p, fmt.Errorf("invalid platform %s/%s", osName, arch)
	}
	return p, nil
}

func printJSON(w io.Writer, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}

func confirm(prompt string) bool {
	if !isTTY(os.Stdin) {
		return false
	}
	fmt.Fprintf(os.Stderr, "%s [y/N] ", prompt)
	var answer string
	_, _ = fmt.Fscanln(os.Stdin, &answer)
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes"
}

func isTTY(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

func humanBytes(n float64) string {
	const unit = 1024.0
	if n < unit {
		return fmt.Sprintf("%.0f B", n)
	}
	div, exp := unit, 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", n/div, "KMGTP"[exp])
}

func successf(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
func errorf(format string, args ...any)   { fmt.Fprintf(os.Stderr, "ghpm: "+format+"\n", args...) }
