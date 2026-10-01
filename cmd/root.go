package cmd

import (
	"fmt"
	"os"
	"runtime"

	"github.com/dbpod-io/dbpod/internal/external"
	"github.com/dbpod-io/dbpod/internal/globalconfig"
	"github.com/dbpod-io/dbpod/internal/instance"
	"github.com/spf13/cobra"
)

// Version is the dbpod release version, injectable at build time:
//
//	go build -ldflags "-X github.com/dbpod-io/dbpod/cmd.Version=v0.1.0"
var Version = "dev"

// Commit is the git commit the binary was built from, injectable the same
// way as Version ("none" when built from a plain checkout).
var Commit = "none"

var rootCmd = &cobra.Command{
	Use:   "dbpod",
	Short: "Lightweight, project-local database management CLI",
	Long: `dbpod is a lightweight, cross-platform, project-local database management tool.

It manages install-free database engine binaries (like images), runs them as
detached background processes (like containers, no daemon), and stores data in
project-local volumes under ./.dbpod/.

A dbpod.yaml in the project root lets you reproduce the exact database
environment with one command.`,
	SilenceUsage: true,
}

// versionText renders the shared version output of `dbpod version` and
// `dbpod --version`.
func versionText() string {
	line := "dbpod version " + Version
	if Commit != "none" {
		line += " (commit " + Commit + ")"
	}
	return line + "\n" + runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH + "\n"
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the dbpod version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Fprint(os.Stdout, versionText())
	},
}

func Execute() {
	// reap auto-remove instances that stopped without their monitor; the
	// monitor itself must not reap (it would kill the instance it is about
	// to start)
	if len(os.Args) < 2 || os.Args[1] != "monitor" {
		instance.Reap(os.Stderr)
	}
	// apply global config (network proxy) and mount config-declared
	// engines (engines.d manifests) before any command runs
	if cfg, err := globalconfig.Load(); err == nil {
		cfg.Apply()
		manifests, merrs := globalconfig.LoadEngines()
		for _, me := range merrs {
			fmt.Fprintf(os.Stderr, "note: engine manifest: %v\n", me)
		}
		external.Mount(manifests, &globalconfig.Config{}, os.Stderr)
	} else {
		fmt.Fprintf(os.Stderr, "note: global config: %v\n", err)
	}
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(versionCmd)
	// --version prints the same text as the version subcommand
	rootCmd.Version = versionText()
	rootCmd.SetVersionTemplate("{{ .Version }}")
}
