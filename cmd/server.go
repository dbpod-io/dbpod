package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shapled/dbpod/internal/dataenv"
	"github.com/shapled/dbpod/internal/dist"
	"github.com/shapled/dbpod/internal/metadata"
	"github.com/shapled/dbpod/internal/server"
	"github.com/spf13/cobra"
)

var (
	serverName   string
	serverEngine string
	serverData   string
	serverPort   int
	followLogs   bool
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Manage database servers (processes)",
}

var serverLsCmd = &cobra.Command{
	Use:     "ls",
	Aliases: []string{"list"},
	Short:   "List servers of the current project (same as dbpod ps)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return printServers()
	},
}

var serverStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start a server combining engine + data + port",
	Example: `  dbpod server start \
    --name=app-db-3307 \
    --engine=mysql@8.0.35 \
    --data=app-test \
    --port=3307`,
	RunE: func(cmd *cobra.Command, args []string) error {
		spec, _, err := buildSpec()
		if err != nil {
			return err
		}
		if err := ensureEngine(spec.Engine, spec.Version); err != nil {
			return err
		}
		if _, err := server.Start(*spec, os.Stdout); err != nil {
			return err
		}
		printConnString(spec.Engine, spec.Port)
		return nil
	},
}

var serverStopCmd = &cobra.Command{
	Use:   "stop <name>",
	Short: "Stop a server",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		_, err := server.Stop(args[0], os.Stdout)
		return err
	},
}

var serverRestartCmd = &cobra.Command{
	Use:   "restart <name>",
	Short: "Restart a server",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		_, err := server.Restart(args[0], os.Stdout)
		return err
	},
}

var serverRmCmd = &cobra.Command{
	Use:     "rm <name>",
	Aliases: []string{"remove"},
	Short:   "Stop (if running) and remove a server record",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		_, err := server.Remove(args[0], os.Stdout)
		return err
	},
}

var serverLogsCmd = &cobra.Command{
	Use:   "logs <name>",
	Short: "Show server logs",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		r, err := serverGet(args[0])
		if err != nil {
			return err
		}
		return server.Logs(r, followLogs, os.Stdout)
	},
}

// buildSpec assembles a server.Spec from CLI flags, resolving the data
// argument (environment name or path) and the engine version.
func buildSpec() (*server.Spec, string, error) {
	if serverName == "" {
		return nil, "", fmt.Errorf("--name is required")
	}
	if serverData == "" {
		return nil, "", fmt.Errorf("--data is required (data environment name or path)")
	}
	ref, err := dist.ParseRef(serverEngine)
	if err != nil {
		return nil, "", err
	}
	version, err := resolveVersion(ref)
	if err != nil {
		return nil, "", err
	}

	env, dataDir, named, err := dataenv.Resolve(serverData, ref.Engine, version)
	if err != nil {
		return nil, "", err
	}
	dataEnvName := ""
	if named {
		dataEnvName = env.Name
	}
	if serverPort <= 0 {
		serverPort = 3306
	}
	return &server.Spec{
		Name:    serverName,
		Engine:  ref.Engine,
		Version: version,
		DataDir: dataDir,
		DataEnv: dataEnvName,
		Port:    serverPort,
	}, dataEnvName, nil
}

// resolveVersion turns a possibly-series version ("8.0") into a full version,
// preferring a locally installed match, then the latest known release.
func resolveVersion(ref dist.PackageRef) (string, error) {
	if strings.Count(ref.Version, ".") >= 2 {
		return ref.Version, nil // already full
	}
	// installed match?
	local, err := dist.ListLocal()
	if err != nil {
		return "", err
	}
	best := ""
	for _, l := range local {
		if l.Engine == ref.Engine && strings.HasPrefix(l.Version, ref.Version+".") && l.Version > best {
			best = l.Version
		}
	}
	if best != "" {
		return best, nil
	}
	// latest known in that series
	ix, err := metadata.EnsureVersions(ref.Engine, mirror)
	if err != nil {
		return "", fmt.Errorf("cannot resolve series %q: %w (install a full version, e.g. %s@8.0.35)", ref.Version, err, ref.Engine)
	}
	for _, v := range ix.ListVersions() {
		if strings.HasPrefix(v, ref.Version+".") {
			return v, nil
		}
	}
	return "", fmt.Errorf("no known version in series %s for engine %s", ref.Version, ref.Engine)
}

// ensureEngine installs engine@version when missing.
func ensureEngine(engineName, version string) error {
	if dist.Installed(engineName, version) {
		return nil
	}
	fmt.Fprintf(os.Stdout, "engine %s@%s not installed yet\n", engineName, version)
	return dist.Install(dist.PackageRef{Engine: engineName, Version: version}, mirror, os.Stdout)
}

func printServers() error {
	records, err := server.List()
	if err != nil {
		return err
	}
	tw := newTable(os.Stdout, "NAME", "ENGINE", "VERSION", "PORT", "DATA", "PID", "STATUS")
	for _, r := range records {
		status, pid := "stopped", "-"
		if r.Running() {
			status, pid = "running", fmt.Sprint(r.PID)
		}
		data := r.DataDir
		if r.DataEnv != "" {
			data = r.DataEnv
		}
		tw.row(r.Name, r.Engine, r.Version, fmt.Sprint(r.Port), data, pid, status)
	}
	return tw.flush()
}

func serverGet(name string) (*server.Record, error) {
	records, err := server.List()
	if err != nil {
		return nil, err
	}
	for _, r := range records {
		if r.Name == name {
			return r, nil
		}
	}
	return nil, fmt.Errorf("server %q does not exist", name)
}

func printConnString(engName string, port int) {
	fmt.Fprintf(os.Stdout, "connection: mysql -h 127.0.0.1 -P %d -u root\n", port)
}

func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

func init() {
	serverStartCmd.Flags().StringVar(&serverName, "name", "", "server name")
	serverStartCmd.Flags().StringVar(&serverEngine, "engine", "mysql@8.0", "engine spec <engine>@<version|series>")
	serverStartCmd.Flags().StringVar(&serverData, "data", "", "data environment name or directory path")
	serverStartCmd.Flags().IntVar(&serverPort, "port", 3306, "TCP port")
	serverLogsCmd.Flags().BoolVarP(&followLogs, "follow", "f", false, "follow log output")

	serverCmd.GroupID = "project"
	serverCmd.AddCommand(serverLsCmd, serverStartCmd, serverStopCmd, serverRestartCmd, serverRmCmd, serverLogsCmd)
	rootCmd.AddCommand(serverCmd)

	rootCmd.AddCommand(&cobra.Command{
		Use:     "version",
		Short:   "Print the dbpod version",
		GroupID: "global",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "dbpod version %s\n", Version)
		},
	})

	rootCmd.AddCommand(&cobra.Command{
		Use:     "ps",
		Short:   "List servers of the current project (alias of: dbpod server ls)",
		GroupID: "project",
		RunE: func(cmd *cobra.Command, args []string) error {
			return printServers()
		},
	})
}
