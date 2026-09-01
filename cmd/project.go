package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shapled/dbpod/internal/config"
	"github.com/shapled/dbpod/internal/dataenv"
	"github.com/shapled/dbpod/internal/server"
	"github.com/spf13/cobra"
)

var (
	initEngine string
	initPort   int
	forceFlag  bool
)

var projectCmds = []*cobra.Command{
	{
		Use:   "init",
		Short: "Generate dbpod.yaml and update .gitignore for this project",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			yamlPath := filepath.Join(cwd, config.FileName)
			if _, err := os.Stat(yamlPath); err == nil && !forceFlag {
				return fmt.Errorf("%s already exists (use --force to overwrite)", config.FileName)
			}
			ref, err := parseEngineSpec(initEngine)
			if err != nil {
				return err
			}
			version, err := resolveVersion(*ref)
			if err != nil {
				fmt.Fprintf(os.Stderr, "note: %v; keeping series version in yaml\n", err)
				version = ref.Version
			}
			c := &config.Config{
				Engine:  ref.Engine,
				Version: version,
				Port:    initPort,
			}
			if err := config.Save(cwd, c); err != nil {
				return err
			}
			gitignoreUpdated, err := ensureGitignore(cwd)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not update .gitignore: %v\n", err)
			}
			fmt.Fprintf(os.Stdout, "created %s\n", yamlPath)
			if gitignoreUpdated {
				fmt.Fprintf(os.Stdout, "added .dbpod/ to .gitignore\n")
			}
			fmt.Fprintf(os.Stdout, "next: edit %s (add init-sql if needed), then run `dbpod up`\n", config.FileName)
			return nil
		},
	},
	{
		Use:   "up",
		Short: "Start the project database (auto-init data dir and import init-sql on first run)",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := config.Dir()
			if err != nil {
				return err
			}
			if dir == "" {
				return fmt.Errorf("no %s found (run `dbpod init` first)", config.FileName)
			}
			c, err := config.Load(dir)
			if err != nil {
				return err
			}
			name := projectName(c, dir)
			version, err := resolveVersion(*engineRef(c.Engine, c.Version))
			if err != nil {
				return err
			}
			if err := ensureEngine(c.Engine, version); err != nil {
				return err
			}

			env, err := dataenv.Create(name, c.Engine, version)
			if err != nil {
				// existing environment is fine — reuse it
				if env, err = dataenv.Load(name); err != nil {
					return err
				}
			}
			dataDir, err := env.DataPath()
			if err != nil {
				return err
			}

			spec := server.Spec{
				Name:    name,
				Engine:  c.Engine,
				Version: version,
				DataDir: dataDir,
				DataEnv: name,
				Port:    c.Port,
			}
			record, err := server.Start(spec, os.Stdout)
			if err != nil {
				return err
			}

			// first initialization: import init-sql once
			fresh := !serverWasInitialized(record)
			if fresh {
				if files, err := c.ResolveInitSQL(dir); err != nil {
					fmt.Fprintf(os.Stderr, "warning: %v\n", err)
				} else if len(files) > 0 {
					if err := importSQL(c.Engine, version, files, c.Port); err != nil {
						return err
					}
					fmt.Fprintf(os.Stdout, "imported %d init-sql file(s)\n", len(files))
				}
				markInitialized(record)
			}
			printStatus(c.Engine, c.Port)
			return nil
		},
	},
	{
		Use:   "status",
		Short: "Show project server status, port, pid, data size and connection string",
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := projectRecord()
			if err != nil {
				return err
			}
			running := r.Running()
			size := dataenv.DirSize(r.DataDir)
			fmt.Fprintf(os.Stdout, "server:    %s\n", r.Name)
			fmt.Fprintf(os.Stdout, "engine:    %s@%s\n", r.Engine, r.Version)
			fmt.Fprintf(os.Stdout, "status:    %s\n", map[bool]string{true: "running", false: "stopped"}[running])
			fmt.Fprintf(os.Stdout, "port:      %d\n", r.Port)
			if running {
				fmt.Fprintf(os.Stdout, "pid:       %d\n", r.PID)
			}
			fmt.Fprintf(os.Stdout, "data dir:  %s (%s)\n", r.DataDir, humanSize(size))
			fmt.Fprintf(os.Stdout, "connect:   mysql -h 127.0.0.1 -P %d -u root\n", r.Port)
			return nil
		},
	},
	{
		Use:   "down",
		Short: "Stop the project server",
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := projectRecord()
			if err != nil {
				return err
			}
			_, err = server.Stop(r.Name, os.Stdout)
			return err
		},
	},
	{
		Use:   "clean",
		Short: "Stop the project server and remove all local dbpod state",
		RunE: func(cmd *cobra.Command, args []string) error {
			if records, err := server.List(); err == nil {
				for _, r := range records {
					if r.Running() {
						if _, err := server.Stop(r.Name, os.Stdout); err != nil {
							return err
						}
					}
				}
			}
			dir, err := dbpodDir()
			if err != nil {
				return err
			}
			if !forceFlag {
				fmt.Fprintf(os.Stdout, "remove %s? [y/N] ", dir)
				answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				if answer = strings.TrimSpace(strings.ToLower(answer)); answer != "y" && answer != "yes" {
					fmt.Fprintln(os.Stdout, "aborted")
					return nil
				}
			}
			if err := os.RemoveAll(dir); err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "removed %s\n", dir)
			return nil
		},
	},
	{
		Use:   "logs",
		Short: "Show project server logs",
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := projectRecord()
			if err != nil {
				return err
			}
			return server.Logs(r, followLogs, os.Stdout)
		},
	},
}

func init() {
	for _, c := range projectCmds {
		c.GroupID = "project"
		if c.Name() == "init" {
			c.Flags().StringVar(&initEngine, "engine", "mysql@8.0", "engine spec <engine>@<version|series>")
			c.Flags().IntVar(&initPort, "port", 3306, "server port")
			c.Flags().BoolVarP(&forceFlag, "force", "f", false, "overwrite existing dbpod.yaml")
		}
		if c.Name() == "clean" {
			c.Flags().BoolVarP(&forceFlag, "force", "f", false, "skip confirmation")
		}
		if c.Name() == "logs" {
			c.Flags().BoolVarP(&followLogs, "follow", "f", false, "follow log output")
		}
		rootCmd.AddCommand(c)
	}
}
