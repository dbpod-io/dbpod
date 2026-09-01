package cmd

import (
	"fmt"
	"os"

	"github.com/shapled/dbpod/internal/dist"
	"github.com/shapled/dbpod/internal/metadata"
	"github.com/spf13/cobra"
)

var engineCmd = &cobra.Command{
	Use:   "engine",
	Short: "Manage database engine binaries",
}

var engineLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List installed and available engine versions",
	RunE: func(cmd *cobra.Command, args []string) error {
		installed := map[string]bool{}
		local, err := dist.ListLocal()
		if err != nil {
			return err
		}
		for _, ref := range local {
			installed[ref.Engine+"@"+ref.Version] = true
		}

		ix, err := metadata.EnsureVersions("mysql", mirror)
		if err != nil {
			fmt.Fprintln(os.Stderr, "note: cannot fetch available versions:", err)
		}

		tw := newTable(os.Stdout, "ENGINE", "VERSION", "LTS", "STATUS", "SIZE")
		if ix != nil {
			for _, v := range ix.ListVersions() {
				vi := ix.Version(v)
				status := "available"
				if installed["mysql@"+v] {
					status = "installed"
				}
				lts := ""
				if vi.LTS {
					lts = "yes"
				}
				tw.row("mysql", v, lts, status, "")
			}
		}
		// local versions missing from the cloud index still deserve a row
		for _, ref := range local {
			if ix != nil && ix.Version(ref.Version) != nil {
				continue
			}
			tw.row(ref.Engine, ref.Version, "", "installed", "")
		}
		return tw.flush()
	},
}

var engineInstallCmd = &cobra.Command{
	Use:   "install <engine>@<version>",
	Short: "Download and install an engine version (e.g. mysql@8.0.35)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ref, err := dist.ParseRef(args[0])
		if err != nil {
			return err
		}
		return dist.Install(ref, mirror, os.Stdout)
	},
}

var engineRmCmd = &cobra.Command{
	Use:     "rm <engine>@<version>",
	Aliases: []string{"remove"},
	Short:   "Remove the cached binary of an engine version",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ref, err := dist.ParseRef(args[0])
		if err != nil {
			return err
		}
		return dist.Remove(ref.Engine, ref.Version)
	},
}

func init() {
	engineCmd.AddCommand(engineLsCmd, engineInstallCmd, engineRmCmd)
	rootCmd.AddCommand(engineCmd)
}
