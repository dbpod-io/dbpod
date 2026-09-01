package cmd

import (
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/shapled/dbpod/internal/dataenv"
	"github.com/shapled/dbpod/internal/dist"
	"github.com/shapled/dbpod/internal/engine"
	"github.com/shapled/dbpod/internal/server"
	"github.com/spf13/cobra"
)

var (
	dataEngine    string
	dataSQLFiles  []string
	dataOwnerName string
)

var dataCmd = &cobra.Command{
	Use:   "data",
	Short: "Manage data environments (project-local data directories)",
}

var dataLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List data environments of the current project",
	RunE: func(cmd *cobra.Command, args []string) error {
		envs, err := dataenv.List()
		if err != nil {
			return err
		}
		running := runningByDataEnv()
		tw := newTable(os.Stdout, "NAME", "ENGINE", "VERSION", "SIZE", "SERVER", "CREATED")
		for _, e := range envs {
			dp, _ := e.DataPath()
			srv := "-"
			if name, ok := running[e.Name]; ok {
				srv = name
			}
			tw.row(e.Name, e.Engine, e.Version, humanSize(dataenv.DirSize(dp)), srv, e.CreatedAt.Format("2006-01-02 15:04"))
		}
		return tw.flush()
	},
}

var dataCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create an isolated data environment",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ref, err := dist.ParseRef(dataEngine)
		if err != nil {
			return err
		}
		e, err := dataenv.Create(args[0], ref.Engine, ref.Version)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "created data environment %q (engine %s)\n", e.Name, ref)
		return nil
	},
}

var dataRmCmd = &cobra.Command{
	Use:     "rm <name>",
	Aliases: []string{"remove"},
	Short:   "Delete a data environment and its files",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// refuse when a running server sits on this environment
		env, dataDir, _, err := dataenv.Resolve(args[0], "", "")
		if err != nil {
			return err
		}
		for srvName, r := range runningRecords() {
			if r.DataDir == dataDir && r.Running() {
				return fmt.Errorf("server %q is running on data environment %q; stop it first: dbpod server stop %s", srvName, env.Name, srvName)
			}
		}
		if err := dataenv.Remove(args[0]); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "removed data environment %q\n", args[0])
		return nil
	},
}

var dataImportCmd = &cobra.Command{
	Use:   "import <name>",
	Short: "Import SQL files into a data environment",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(dataSQLFiles) == 0 {
			return fmt.Errorf("--sql=<file> is required (repeatable)")
		}
		env, dataDir, _, err := dataenv.Resolve(args[0], "", "")
		if err != nil {
			return err
		}
		if env.Engine == "" {
			return fmt.Errorf("data environment %q has no engine metadata", env.Name)
		}
		eng, err := engine.Get(env.Engine)
		if err != nil {
			return err
		}
		version, err := resolveVersion(dist.PackageRef{Engine: env.Engine, Version: env.Version})
		if err != nil {
			return err
		}

		// reuse a running server on this datadir, else start a temporary one
		port, tempSpec, err := pickServerFor(dataDir, env.Engine, version, env.Name)
		if err != nil {
			return err
		}
		if tempSpec != nil {
			if _, err := server.Start(*tempSpec, os.Stdout); err != nil {
				return err
			}
			defer func() {
				_, _ = server.Stop(tempSpec.Name, os.Stdout)
				_, _ = server.Remove(tempSpec.Name, os.Stdout)
			}()
		}

		opts := engine.Options{DataDir: dataDir, Port: port}
		_, client, _ := eng.BinaryNames() // (server, client, admin)
		clientPath, err := dist.BinaryPath(env.Engine, version, client)
		if err != nil {
			return err
		}
		opts.BinDir = filepath.Dir(clientPath)
		for _, f := range dataSQLFiles {
			fmt.Fprintf(os.Stdout, "importing %s\n", f)
			if err := importViaClient(clientPath, eng.ExecArgs(opts, ""), f); err != nil {
				return fmt.Errorf("import %s failed: %w", f, err)
			}
		}
		fmt.Fprintf(os.Stdout, "imported %d file(s) into %q\n", len(dataSQLFiles), env.Name)
		return nil
	},
}

func init() {
	dataCreateCmd.Flags().StringVar(&dataEngine, "engine", "mysql@8.0", "engine spec <engine>@<version|series>")
	dataImportCmd.Flags().StringArrayVar(&dataSQLFiles, "sql", nil, "SQL file to import (repeatable)")
	dataCmd.GroupID = "project"
	dataCmd.AddCommand(dataLsCmd, dataCreateCmd, dataRmCmd, dataImportCmd)
	rootCmd.AddCommand(dataCmd)
}

// runningRecords maps server name -> record for running servers.
func runningRecords() map[string]*server.Record {
	out := map[string]*server.Record{}
	if records, err := server.List(); err == nil {
		for _, r := range records {
			if r.Running() {
				out[r.Name] = r
			}
		}
	}
	return out
}

func runningByDataEnv() map[string]string {
	out := map[string]string{}
	for name, r := range runningRecords() {
		if r.DataEnv != "" {
			out[r.DataEnv] = name
		}
	}
	return out
}

// pickServerFor returns a port usable for the datadir: the port of a running
// server on it, or a free port plus a temporary server spec to start.
func pickServerFor(dataDir, engName, version, envName string) (int, *server.Spec, error) {
	for _, r := range runningRecords() {
		if r.DataDir == dataDir {
			return r.Port, nil, nil
		}
	}
	port, err := freePort()
	if err != nil {
		return 0, nil, err
	}
	spec := &server.Spec{
		Name:    "tmp-import-" + envName,
		Engine:  engName,
		Version: version,
		DataDir: dataDir,
		DataEnv: envName,
		Port:    port,
	}
	return port, spec, nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fM", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fK", float64(n)/(1<<10))
	default:
		return fmt.Sprint(n)
	}
}
