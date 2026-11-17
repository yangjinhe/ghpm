package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	var showPath bool
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show the configuration file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := newService()
			if err != nil {
				return err
			}
			if showPath {
				fmt.Fprintln(cmd.OutOrStdout(), svc.Cfg.Path)
				return nil
			}
			data, err := os.ReadFile(svc.Cfg.Path)
			if os.IsNotExist(err) {
				fmt.Fprintf(cmd.ErrOrStderr(), "no config yet at %s; add an app with `ghpm add <repo>`\n", svc.Cfg.Path)
				return nil
			}
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
	cmd.Flags().BoolVar(&showPath, "path", false, "print only the config file path")
	return cmd
}
