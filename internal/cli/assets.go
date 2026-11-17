package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"ghpm/internal/resolver"
)

type assetJSON struct {
	Name     string   `json:"name"`
	Size     int64    `json:"size"`
	URL      string   `json:"url"`
	Digest   string   `json:"digest,omitempty"`
	Format   string   `json:"format"`
	OS       string   `json:"os,omitempty"`
	Arches   []string `json:"arches,omitempty"`
	Score    int      `json:"score"`
	Eligible bool     `json:"eligible"`
	Chosen   bool     `json:"chosen"`
}

func newAssetsCmd() *cobra.Command {
	var (
		channel string
		osName  string
		arch    string
	)
	cmd := &cobra.Command{
		Use:   "assets <owner/repo>",
		Short: "Show release assets and how ghpm scores them",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := newService()
			if err != nil {
				return err
			}
			p, err := targetPlatform(osName, arch)
			if err != nil {
				return err
			}
			rel, err := svc.Release(cmd.Context(), args[0], channel)
			if err != nil {
				return err
			}
			best, _, _ := resolver.Best(rel.AssetNames(), p, resolver.Options{Prefer: svc.Cfg.Defaults.Prefer})
			type row struct {
				assetJSON
				size int64
			}
			rows := make([]row, 0, len(rel.Assets))
			for _, a := range rel.Assets {
				c := resolver.Classify(a.Name)
				scored, ok := resolver.Score(a.Name, p, resolver.Options{Prefer: svc.Cfg.Defaults.Prefer})
				score := 0
				if ok {
					score = scored.Score
				}
				rows = append(rows, row{
					assetJSON: assetJSON{
						Name: a.Name, Size: a.Size, URL: a.URL, Digest: a.Digest,
						Format: string(c.Format), OS: c.OS, Arches: c.Arches,
						Score: score, Eligible: ok, Chosen: ok && a.Name == best.Name,
					},
					size: a.Size,
				})
			}
			sort.SliceStable(rows, func(i, j int) bool {
				if rows[i].Score != rows[j].Score {
					return rows[i].Score > rows[j].Score
				}
				return rows[i].Name < rows[j].Name
			})
			if flags.json {
				out := make([]assetJSON, len(rows))
				for i, r := range rows {
					out[i] = r.assetJSON
				}
				return printJSON(os.Stdout, out)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s for %s\n\n", args[0], rel.TagName, p)
			w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(w, "  ASSET\tFORMAT\tSCORE\tSIZE\tOS/ARCH")
			for _, r := range rows {
				mark := " "
				if r.Chosen {
					mark = "*"
				}
				pa := "-"
				if r.OS != "" || len(r.Arches) > 0 {
					pa = r.OS + "/" + strings.Join(r.Arches, ",")
					if r.OS == "" {
						pa = "*/" + strings.Join(r.Arches, ",")
					}
				}
				fmt.Fprintf(w, "%s %s\t%s\t%d\t%s\t%s\n", mark, r.Name, r.Format, r.Score, humanBytes(float64(r.Size)), pa)
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&channel, "channel", "", "latest, prerelease or tag:<tag>")
	cmd.Flags().StringVar(&osName, "os", "", "target operating system (default: this machine)")
	cmd.Flags().StringVar(&arch, "arch", "", "target architecture (default: this machine)")
	return cmd
}
