package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dbpod-io/dbpod/internal/dist"
	"github.com/dbpod-io/dbpod/internal/engine"
	"github.com/spf13/cobra"
)

var execCmd = &cobra.Command{
	Use:   "exec <engine|instance> [binary] [args...]",
	Short: "Execute a binary from an engine's or an instance's distribution",
	Long: `Execute a binary from an installed engine distribution.

The first argument is an ENGINE id (mysql@8.0.46, series like mysql@8.0 work)
or an INSTANCE id (a name from dbpod ps). Engines are tried first, then
instances; when neither matches, both forms are hinted.

  dbpod exec mysql@8.0.46 mysql --version
      run a binary from the given engine
  dbpod exec dev mysqldump --help
      run a binary from the engine that instance "dev" runs
  dbpod exec mysql@8.0.46
      no binary named: defaults to the engine's client (SQL shell)

Execution environment:
  - the engine's exec paths (for MySQL: ./bin) are prepended to PATH, so
    bare binary names such as "mysql" resolve automatically
  - the working directory is the distribution basedir, so "bin/mysql"
    style paths work as well

Args after the binary are passed through as-is. If the first arg does not
name an existing binary of the distribution, ALL args are handed to the
engine's default client:
  dbpod exec mysql@8.0.46 -e "SELECT 1"     == mysql -e "SELECT 1"

The process exit code is propagated.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		engName, version, port, dataDir, err := resolveExecTarget(args[0])
		if err != nil {
			return err
		}
		return runInstanceExec(engName, version, port, dataDir, args[1:])
	},
}

// runInstanceExec executes a binary of the given engine distribution
// (shared by `dbpod exec` and `dbpod project exec`). When dataDir is set
// (an instance target) the default client is pre-wired with root@socket
// connection parameters.
func runInstanceExec(engName, version string, port int, dataDir string, rest []string) error {
	if !dist.Installed(engName, version) {
		return fmt.Errorf("engine %s@%s is not installed; run: dbpod engine install %s@%s", engName, version, engName, version)
	}
	eng, err := engineGet(engName)
	if err != nil {
		return err
	}
	base := dist.Path(engName, version)

	// pick the binary: if the first arg names an executable shipped with
	// the distribution, run it; otherwise everything goes to the
	// engine's default client
	var binary string
	if len(rest) > 0 && rest[0] == "--" {
		rest = rest[1:] // tolerated passthrough separator
	}
	if len(rest) > 0 {
		if hasExecutable(base, eng.ExecPaths(), rest[0]) {
			binary, rest = rest[0], rest[1:]
		} else if strings.ContainsRune(rest[0], '/') {
			return fmt.Errorf("binary %q not found in %s", rest[0], base)
		}
	}
	if binary == "" {
		_, binary, _ = eng.BinaryNames()
		if dataDir != "" { // instance target: pre-wire root@socket connection
			rest = append(eng.ClientArgs(engine.Options{DataDir: dataDir, Port: port}), rest...)
		}
	}

	binPath, err := resolveExecBinary(base, eng.ExecPaths(), binary)
	if err != nil {
		return err
	}

	proc := exec.Command(binPath, rest...)
	proc.Dir = base // cwd = basedir: "bin/mysql" style paths work
	// prepend the engine exec paths so the child (and tools it spawns)
	// resolves bare binary names the same way, plus engine-specific vars
	// (e.g. LD_LIBRARY_PATH for PGDG-extracted linux binaries)
	proc.Env = append(append(os.Environ(), eng.Env(engine.Options{DataDir: dataDir})...),
		"PATH="+prependPaths(base, eng.ExecPaths())+string(filepath.ListSeparator)+os.Getenv("PATH"))
	proc.Stdin = os.Stdin
	proc.Stdout = os.Stdout
	proc.Stderr = os.Stderr
	if err := proc.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		return err
	}
	return nil
}

// resolveExecTarget maps the first exec argument to engine@version plus the
// instance datadir ("" for a bare engine target): engine ids are tried
// first, then instance ids; anything else errors with a hint for both forms.
func resolveExecTarget(id string) (engName, version string, port int, dataDir string, err error) {
	if ref, perr := dist.ParseRef(id); perr == nil {
		if v, rerr := resolveVersion(ref); rerr == nil && dist.Installed(ref.Engine, v) {
			return ref.Engine, v, 0, "", nil
		}
		if r, serr := instanceGet(id); serr == nil {
			return r.Engine, r.Version, r.Port, r.DataDir, nil
		}
		return "", "", 0, "", fmt.Errorf("engine %q is not installed; run: dbpod engine install %s", id, id)
	}
	r, serr := instanceGet(id)
	if serr == nil {
		return r.Engine, r.Version, r.Port, r.DataDir, nil
	}
	return "", "", 0, "", fmt.Errorf("%q is neither an installed engine (e.g. mysql@8.0.46) nor a known instance (see `dbpod ps`)", id)
}

// hasExecutable reports whether arg names an executable shipped with the
// distribution: a "bin/xxx"-style path under the basedir, or a bare name in
// one of the exec paths.
func hasExecutable(base string, execPaths []string, arg string) bool {
	if strings.ContainsRune(arg, '/') {
		p := arg
		if !filepath.IsAbs(p) {
			p = filepath.Join(base, p)
		}
		fi, err := os.Stat(p)
		return err == nil && !fi.IsDir()
	}
	for _, p := range execPaths {
		if fi, err := os.Stat(filepath.Join(base, p, arg)); err == nil && !fi.IsDir() {
			return true
		}
	}
	return false
}

// resolveExecBinary locates the binary: paths with a separator resolve
// against the basedir (the exec cwd), bare names search the engine exec
// paths and then the inherited PATH.
func resolveExecBinary(base string, execPaths []string, binary string) (string, error) {
	if strings.ContainsRune(binary, '/') {
		if filepath.IsAbs(binary) {
			if _, err := os.Stat(binary); err != nil {
				return "", fmt.Errorf("binary %s not found", binary)
			}
			return binary, nil
		}
		p := filepath.Join(base, binary)
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("binary %s not found in %s", binary, base)
		}
		return binary, nil // relative to cmd.Dir (= basedir)
	}
	for _, p := range execPaths {
		cand := filepath.Join(base, p, binary)
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			return cand, nil
		}
	}
	if lp, err := exec.LookPath(binary); err == nil {
		return lp, nil
	}
	return "", fmt.Errorf("binary %q not found (searched %v of %s and PATH)", binary, execPaths, base)
}

// prependPaths turns exec paths (relative to base) into an absolute PATH
// prefix.
func prependPaths(base string, execPaths []string) string {
	abs := make([]string, 0, len(execPaths))
	for _, p := range execPaths {
		abs = append(abs, filepath.Join(base, p))
	}
	return strings.Join(abs, string(filepath.ListSeparator))
}

func init() {
	// everything after the first positional belongs to the executed binary
	execCmd.Flags().SetInterspersed(false)
	rootCmd.AddCommand(execCmd)
}
