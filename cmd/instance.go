package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/shapled/dbpod/internal/dataenv"
	"github.com/shapled/dbpod/internal/dist"
	"github.com/shapled/dbpod/internal/engine"
	"github.com/shapled/dbpod/internal/instance"
	"github.com/shapled/dbpod/internal/metadata"
	"github.com/shapled/dbpod/internal/project"
	"github.com/spf13/cobra"
)

var (
	runName    string
	runEngine  string
	runData    string
	runPort    int
	runRm      bool
	runDetach  bool
	runAll     bool
	followLogs bool
)

var runCmd = &cobra.Command{
	Use:   "run [--name <name>] [--engine <engine>@<version>] [--data <dir>] [--port <port>]",
	Short: "Run a database instance from an engine distribution",
	Long: `Run a database instance from an installed engine distribution.

Everything defaults: the name is generated, a free port is picked, and the
datadir lives under the global instances directory (DBPOD_INSTANCES_DIR,
default <DBPOD_HOME>/instances/<name>/data).

Running an already-running instance is a no-op; a stopped instance with the
same name is started again.`,
	Example: `  dbpod run                                     # all defaults
  dbpod run --engine=mysql@8.0.35               # pin the engine
  dbpod run --name=app-db --port=3307           # pin name and port
  dbpod run --name=app-db --data=./data/app-db  # datadir inside the project`,
	RunE: func(cmd *cobra.Command, args []string) error {
		spec, err := buildSpec()
		if err != nil {
			return err
		}
		spec.AutoRemove = runRm
		if err := ensureEngine(spec.Engine, spec.Version); err != nil {
			return err
		}
		r, err := instance.Start(*spec, os.Stdout)
		if err != nil {
			return err
		}
		printConnString(spec.Name, spec.DataDir)

		// -d: the per-instance monitor keeps the instance running; we are done
		if runDetach {
			if runRm {
				fmt.Fprintf(os.Stdout, "auto-removes when the instance stops\n")
			}
			return nil
		}

		// foreground attach: leave when the instance stops, propagate its
		// exit code; Ctrl-C / TERM shut the instance down gracefully
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(sigCh)
		done := make(chan int, 1)
		go func() {
			code, err := instance.AttachWait(r.Name, 0)
			if err != nil {
				code = 1
			}
			done <- code
		}()
		select {
		case code := <-done:
			if code != 0 {
				os.Exit(code)
			}
			return nil
		case <-sigCh:
			fmt.Fprintf(os.Stdout, "shutting down %q...\n", r.Name)
			_, _ = instance.Stop(r.Name, os.Stdout)
			code := <-done
			if code != 0 {
				os.Exit(code)
			}
			return nil
		}
	},
}

var startCmd = &cobra.Command{
	Use:   "start <name>...",
	Short: "Start stopped database instances from their records",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var errs []error
		for _, name := range args {
			r, err := instanceGet(name)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			spec := instance.Spec{
				Name:    r.Name,
				Engine:  r.Engine,
				Version: r.Version,
				DataDir: r.DataDir,
				Port:    r.Port,
			}
			if _, err := instance.Start(spec, os.Stdout); err != nil {
				errs = append(errs, err)
				continue
			}
			printConnString(r.Name, r.DataDir)
		}
		return errors.Join(errs...)
	},
}

var killCmd = &cobra.Command{
	Use:     "kill <name>...",
	Aliases: []string{"stop"},
	Short:   "Stop running database instances (graceful shutdown)",
	Args:    cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var errs []error
		for _, name := range args {
			if _, err := instance.Stop(name, os.Stdout); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	},
}

var rmForce bool

var rmCmd = &cobra.Command{
	Use:     "rm <name>...",
	Aliases: []string{"remove"},
	Short:   "Remove stopped instances: delete their records and datadirs (-f skips confirmation)",
	Args:    cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// load and validate every target before touching anything
		var targets []*instance.Record
		var running []string
		var errs []error
		for _, name := range args {
			r, err := instanceGet(name)
			if err != nil {
				errs = append(errs, err) // e.g. already auto-removed: warn, keep going
				continue
			}
			if r.Running() {
				running = append(running, r.Name)
				continue
			}
			targets = append(targets, r)
		}
		if len(running) > 0 {
			return fmt.Errorf("running instances must be stopped first: dbpod kill %s", strings.Join(running, " "))
		}
		if len(targets) == 0 {
			return nil
		}
		if !rmForce {
			fmt.Fprintf(os.Stdout, "remove %d instance(s) and their datadirs?\n", len(targets))
			for _, r := range targets {
				fmt.Fprintf(os.Stdout, "  - %s (%s)\n", r.Name, r.DataDir)
			}
			fmt.Fprint(os.Stdout, "[y/N] ")
			answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if answer = strings.TrimSpace(strings.ToLower(answer)); answer != "y" && answer != "yes" {
				fmt.Fprintln(os.Stdout, "aborted")
				return nil
			}
		}
		for _, r := range targets {
			if _, err := instance.Remove(r.Name, os.Stdout); err != nil {
				errs = append(errs, err)
				continue
			}
			if err := os.RemoveAll(r.DataDir); err != nil {
				errs = append(errs, err)
				continue
			}
			fmt.Fprintf(os.Stdout, "datadir %s deleted\n", r.DataDir)
		}
		return errors.Join(errs...)
	},
}

var psCmd = &cobra.Command{
	Use:   "ps [-a]",
	Short: "List running instances (-a: all, including stopped and directory-discovered ones)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if !runAll {
			return printInstances()
		}
		records, err := instance.List()
		if err != nil {
			return err
		}
		dir, err := project.InstancesDir()
		if err != nil {
			return err
		}
		known := map[string]bool{}
		for _, r := range records {
			known[r.Name] = true
		}
		discovered, err := instance.ListGlobal(dir)
		if err != nil {
			return err
		}
		for _, r := range discovered {
			if !known[r.Name] {
				records = append(records, r)
			}
		}
		sort.Slice(records, func(i, j int) bool { return records[i].Name < records[j].Name })
		return printInstanceTable(records)
	},
}

var logsCmd = &cobra.Command{
	Use:   "logs <name>",
	Short: "Show logs of a database instance",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		r, err := instanceGet(args[0])
		if err != nil {
			return err
		}
		return instance.Logs(r, followLogs, os.Stdout)
	},
}

// buildSpec assembles an instance.Spec from CLI flags. Missing values
// default: name generated, port auto-picked, datadir under the global
// instances directory.
func buildSpec() (*instance.Spec, error) {
	ref, err := dist.ParseRef(runEngine)
	if err != nil {
		return nil, err
	}
	version, err := resolveVersion(ref)
	if err != nil {
		return nil, err
	}
	name := runName
	if name == "" {
		name, err = autoInstanceName(ref.Engine)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(os.Stdout, "no --name given, using %q\n", name)
	}
	dataDir := runData
	if dataDir == "" {
		base, err := project.InstancesDir()
		if err != nil {
			return nil, err
		}
		dataDir = filepath.Join(base, name, "data")
	} else {
		_, dataDir, _, err = dataenv.Resolve(dataDir, ref.Engine, version)
		if err != nil {
			return nil, err
		}
	}
	port := runPort
	if port <= 0 {
		port, err = freePort()
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(os.Stdout, "no --port given, using %d\n", port)
	}
	return &instance.Spec{
		Name:    name,
		Engine:  ref.Engine,
		Version: version,
		DataDir: dataDir,
		Port:    port,
	}, nil
}

const nameChars = "abcdefghijklmnopqrstuvwxyz0123456789"

// autoInstanceName generates an unused name like "mysql-k3jm9f".
func autoInstanceName(engine string) (string, error) {
	for range 100 {
		suffix := make([]byte, 6)
		for i := range suffix {
			suffix[i] = nameChars[rand.Intn(len(nameChars))]
		}
		name := engine + "-" + string(suffix)
		if _, err := instanceGet(name); err != nil {
			return name, nil // not taken
		}
	}
	return "", fmt.Errorf("could not generate a free instance name")
}

// freePort asks the kernel for an available TCP port.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
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

func printInstances() error {
	records, err := instance.List()
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
}

func printInstanceTable(records []*instance.Record) error {
	tw := newTable(os.Stdout, "NAME", "ENGINE", "VERSION", "PORT", "DATA", "PID", "STATUS")
	for _, r := range records {
		status, pid := "", ""
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

func instanceGet(name string) (*instance.Record, error) {
	records, err := instance.List()
	if err != nil {
		return nil, err
	}
	for _, r := range records {
		if r.Name == name {
			return r, nil
		}
	}
	return nil, fmt.Errorf("instance %q does not exist", name)
}

func printConnString(name, dataDir string) {
	sock := instanceSocket(name, dataDir)
	fmt.Fprintf(os.Stdout, "connect: dbpod exec %s mysql -u root -S %s\n", name, sock)
}

// instanceSocket resolves the unix socket path of an instance.
func instanceSocket(name, dataDir string) string {
	eng, err := engineGet(name2engine(name))
	if err != nil {
		return ""
	}
	return eng.SocketPath(engine.Options{DataDir: dataDir})
}

// name2engine maps an instance name to its engine via the record; on any
// failure it falls back to mysql (the only supported engine today).
func name2engine(name string) string {
	if r, err := instanceGet(name); err == nil {
		return r.Engine
	}
	return "mysql"
}

var monitorName string

var monitorCmd = &cobra.Command{
	Use:    "monitor",
	Short:  "Run the per-instance supervisor (spawned by dbpod itself, conmon-style)",
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return instance.RunMonitor(monitorName, os.Stdout)
	},
}

func init() {
	runCmd.Flags().StringVar(&runName, "name", "", "instance name (default: generated)")
	runCmd.Flags().StringVar(&runEngine, "engine", "mysql@8.0", "engine spec <engine>@<version|series>")
	runCmd.Flags().StringVar(&runData, "data", "", "datadir path (default: <instances-dir>/<name>/data)")
	runCmd.Flags().IntVar(&runPort, "port", 0, "TCP port (default: a free port)")
	runCmd.Flags().BoolVar(&runRm, "rm", false, "automatically remove the instance when it stops")
	runCmd.Flags().BoolVarP(&runDetach, "detach", "d", false, "run in the background and return immediately")
	psCmd.Flags().BoolVarP(&runAll, "all", "a", false, "list all instances, including stopped ones and orphans")
	logsCmd.Flags().BoolVarP(&followLogs, "follow", "f", false, "follow log output")
	rmCmd.Flags().BoolVarP(&rmForce, "force", "f", false, "skip confirmation")
	monitorCmd.Flags().StringVar(&monitorName, "name", "", "instance name")

	rootCmd.AddCommand(runCmd, startCmd, killCmd, rmCmd, psCmd, logsCmd)
	rootCmd.AddCommand(monitorCmd)
}

// humanSize renders byte counts for tables.
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
