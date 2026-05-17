package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/t4db/t4/internal/version"
)

func versionCmd() *cobra.Command {
	var short bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the t4 build version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := version.Info()
			out := cmd.OutOrStdout()
			if short {
				fmt.Fprintln(out, info.Version)
				return nil
			}
			fmt.Fprintf(out, "t4 %s\n", info.Version)
			if info.Commit != "" {
				fmt.Fprintf(out, "  commit: %s\n", info.Commit)
			}
			if info.Date != "" {
				fmt.Fprintf(out, "  built:  %s\n", info.Date)
			}
			fmt.Fprintf(out, "  go:     %s\n", info.GoVersion)
			return nil
		},
	}
	cmd.Flags().BoolVar(&short, "short", false, "Print only the version string")
	return cmd
}
