package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shapled/dbpod/internal/config"
	"github.com/shapled/dbpod/internal/dataenv"
	"github.com/shapled/dbpod/internal/instance"
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
				return fmt.Errorf("no %s found (run `dbpod project init` first)", config.FileName)
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

			spec := instance.Spec{
				Name:    name,
				Engine:  c.Engine,
				Version: version,
				DataDir: dataDir,
				DataEnv: name,
				Port:    c.Port,
			}
			record, err := instance.Start(spec, os.Stdout)
			if err != nil {
				return err
			}

			// first initialization: import init-sql once
			fresh := !instanceWasInitialized(record)
			if fresh {
				if files, err := c.ResolveInitSQL(dir); err != nil {
					fmt.Fprintf(os.Stderr, "warning: %v\n", err)
				} else if len(files) > 0 {
					if err := importSQL(c.Engine, version, files, c.Port); err != nil {
						return err
					}
					fmt.Fprintf(os.Stdout, "imported %d init-sql file(s)\n", len(files))
				}
				instanceMarkInitialized(record)
			}
			printStatus()
			return nil
		},
	},
	{
		Use:   "down",
		Short: "Stop the project database instance",
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := projectRecord()
			if err != nil {
				return err
			}
			_, err = instance.Stop(r.Name, os.Stdout)
			return err
		},
	},
	{
		Use:   "clean",
		Short: "Stop the project instance and remove all local dbpod state",
		RunE: func(cmd *cobra.Command, args []string) error {
			if records, err := instance.List(); err == nil {
				for _, r := range records {
					if r.Running() {
						if _, err := instance.Stop(r.Name, os.Stdout); err != nil {
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
		Use:   "ps",
		Short: "List running instances of this project",
		RunE: func(cmd *cobra.Command, args []string) error {
			records, err := projectInstances()
			if err != nil {
				return err
			}
			var running []*instance.Record
			for _, r := range records {
				if r.Running() {
					running = append(running, r)
				}
			}
			return printInstanceTable(running)
		},
	},
	{
		Use:   "logs [name]",
		Short: "Show logs of a project instance (defaults to the only one)",
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := defaultProjectInstance(args)
			if err != nil {
				return err
			}
			return instance.Logs(r, followLogs, os.Stdout)
		},
	},
	{
		Use:   "inspect [name]",
		Short: "Output project instance details in JSON format (defaults to the only one)",
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := defaultProjectInstance(args)
			if err != nil {
				return err
			}
			return printInspectJSON([]*instance.Record{r})
		},
	},
	{
		Use:   "exec [name] [binary] [args...]",
		Short: "Run a binary from a project instance's engine (defaults to the only one)",
		// passthrough: everything after `exec` belongs to the executed
		// binary, so args like `-c "SELECT 1"` must survive untouched
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
				return cmd.Help()
			}
			r, err := defaultProjectInstance(args)
			if err != nil {
				return err
			}
			rest := args
			if len(rest) > 0 && rest[0] == r.Name {
				rest = rest[1:] // explicit instance selector consumed
			}
			return runInstanceExec(r.Engine, r.Version, r.Port, r.DataDir, rest)
		},
	},
}

// projectCmd groups the project-local lifecycle commands.
var projectCmd = &cobra.Command{
	Use:     "project",
	Aliases: []string{"proj"},
	Short:   "Project-local database lifecycle: init, up, down, clean",
	Long: `Manage the database declared by this project's dbpod.yaml.

  dbpod project init      generate dbpod.yaml and update .gitignore
  dbpod project up        start the project database (first run: init + import init-sql)
  dbpod project down      stop the project database
  dbpod project clean     stop and remove all local dbpod state
  dbpod project ps        list this project's instances
  dbpod project logs      logs of a project instance (defaults to the only one)
  dbpod project exec      run a binary of a project instance's engine`,
	Example: `  dbpod proj up
  dbpod proj logs -f
  dbpod proj exec mysql -e "SELECT 1"`,
}

func init() {
	for _, c := range projectCmds {
		if c.Name() == "init" {
			c.Flags().StringVar(&initEngine, "engine", "mysql@8.0", "engine spec <engine>@<version|series>")
			c.Flags().IntVar(&initPort, "port", 3306, "instance port")
			c.Flags().BoolVarP(&forceFlag, "force", "f", false, "overwrite existing dbpod.yaml")
		}
		if c.Name() == "clean" {
			c.Flags().BoolVarP(&forceFlag, "force", "f", false, "skip confirmation")
		}
		projectCmd.AddCommand(c)
	}
	rootCmd.AddCommand(projectCmd)
}
