package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

// Version is the dbpod release version.
const Version = "0.1.0"

var mirror string

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

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddGroup(
		&cobra.Group{
			ID:    "project",
			Title: "Project Commands (operate on the current project's ./.dbpod)",
		},
		&cobra.Group{
			ID:    "global",
			Title: "Global Commands",
		},
	)
	rootCmd.PersistentFlags().StringVar(&mirror, "mirror", "", "mirror base URL replacing the official download host (e.g. https://mirrors.example.com/mysql)")
}
