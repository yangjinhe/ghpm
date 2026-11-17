package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"
)

func newDocCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:    "doc",
		Short:  "Generate man pages",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			manDir := filepath.Join(dir, "man")
			if err := os.MkdirAll(manDir, 0o755); err != nil {
				return err
			}
			root := cmd.Root()
			root.DisableAutoGenTag = true
			header := &doc.GenManHeader{
				Title:   "GHPM",
				Section: "1",
				Source:  "ghpm",
				Manual:  "ghpm manual",
			}
			if err := doc.GenManTree(root, header, manDir); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "wrote man pages to %s\n", manDir)
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "docs", "output directory")
	return cmd
}
