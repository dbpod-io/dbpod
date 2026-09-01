package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

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
	rootCmd.PersistentFlags().StringVar(&mirror, "mirror", "", "mirror base URL replacing the official download host (e.g. https://mirrors.example.com/mysql)")
}
