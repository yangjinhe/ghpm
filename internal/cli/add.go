package cli

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"ghpm/internal/app"
	"ghpm/internal/config"
)

func newAddCmd() *cobra.Command {
	var (
		id         string
		osName     string
		arch       string
		asset      string
		channel    string
		prerelease bool
		installNow bool
	)
	cmd := &cobra.Command{
		Use:   "add <owner/repo | github-url>",
		Short: "Configure a GitHub project and pick the right assets",
		Long: `Resolves the latest release, identifies the asset for this machine (or the
requested --os/--arch), and writes a match section to the config file.

Examples:
  ghpm add jesseduffield/lazygit
  ghpm add https://github.com/sharkdp/fd --install
  ghpm add BurntSushi/ripgrep --arch arm64 --os linux`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := newService()
			if err != nil {
				return err
			}
			p, err := targetPlatform(osName, arch)
			if err != nil {
				return err
			}
			pf := newProgressPrinter()
			defer pf.Stop()
			opts := app.AddOptions{
				Input:      args[0],
				ID:         id,
				Platform:   p,
				Asset:      asset,
				Channel:    channel,
				Prerelease: prerelease,
				Install:    installNow,
				Progress:   pf.Event,
			}
			res, err := svc.Add(cmd.Context(), opts)
			var choice *app.AssetChoiceError
			if errors.As(err, &choice) && isTTY(os.Stdin) {
				picked, pickErr := promptAsset(choice)
				if pickErr != nil {
					return pickErr
				}
				opts.Asset = picked
				fmt.Fprintf(cmd.ErrOrStderr(), "Using asset %s\n", picked)
				res, err = svc.Add(cmd.Context(), opts)
			}
			if err != nil {
				return err
			}
			a := res.App
			fmt.Printf("Added %s (%s)\n", a.ID, a.Repo)
			fmt.Printf("  channel:  %s\n", a.Channel)
			fmt.Printf("  asset:    %s\n", res.Chosen.Name)
			fmt.Printf("  install:  %s\n", installDescription(a))
			if len(res.Platforms) > 0 {
				fmt.Printf("  platforms: %s\n", strings.Join(res.Platforms, ", "))
			}
			fmt.Printf("  config:   %s\n", svc.Cfg.Path)
			for _, w := range res.Warnings {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", w)
			}
			if res.Installed != nil {
				for _, w := range res.Installed.Warnings {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", w)
				}
				if res.Installed.Err != nil {
					return res.Installed.Err
				}
				fmt.Printf("  installed: %s -> %s\n", res.Installed.Version, res.Installed.Link)
			} else {
				fmt.Printf("\nRun `ghpm install %s` to install it.\n", a.ID)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "application id shown by ghpm (default: repository name)")
	cmd.Flags().StringVar(&osName, "os", "", "target operating system (default: this machine)")
	cmd.Flags().StringVar(&arch, "arch", "", "target architecture (default: this machine)")
	cmd.Flags().StringVar(&asset, "asset", "", "force a specific release asset name")
	cmd.Flags().StringVar(&channel, "channel", "", "latest, prerelease or tag:<tag>")
	cmd.Flags().BoolVar(&prerelease, "prerelease", false, "track prereleases")
	cmd.Flags().BoolVar(&installNow, "install", false, "install immediately after adding")
	return cmd
}

func installDescription(a config.App) string {
	desc := a.Install.Type
	if desc == "" {
		desc = "auto"
	}
	if a.Install.Bin != "" {
		desc += " (bin " + a.Install.Bin + ")"
	}
	return desc
}

// promptAsset asks the user to choose among the assets that matched. It is
// only used on a terminal; scripts get the error and its --asset hint.
func promptAsset(choice *app.AssetChoiceError) (string, error) {
	fmt.Fprintf(os.Stderr, "%s %s: several assets match this platform:\n", choice.Repo, choice.Tag)
	for i, c := range choice.Candidates {
		fmt.Fprintf(os.Stderr, "  %2d) %-64s %-8s score %d\n", i+1, c.Name, c.Format, c.Score)
	}
	for {
		fmt.Fprint(os.Stderr, "Choose an asset (number or name): ")
		var answer string
		if _, err := fmt.Fscanln(os.Stdin, &answer); err != nil {
			return "", choice
		}
		answer = strings.TrimSpace(answer)
		if n, err := strconv.Atoi(answer); err == nil && n >= 1 && n <= len(choice.Candidates) {
			return choice.Candidates[n-1].Name, nil
		}
		for _, c := range choice.Candidates {
			if strings.EqualFold(c.Name, answer) {
				return c.Name, nil
			}
		}
		fmt.Fprintln(os.Stderr, "Enter a listed number or asset name.")
	}
}
