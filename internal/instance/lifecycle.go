package instance

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dbpod-io/dbpod/internal/dist"
	"github.com/dbpod-io/dbpod/internal/engine"
	"github.com/dbpod-io/dbpod/internal/project"
	"github.com/shirou/gopsutil/v4/process"
)

// Spec describes how to start (or resolve) an instance.
type Spec struct {
	Name       string
	Engine     string // engine name, e.g. "mysql"
	Version    string // full version
	DataDir    string // absolute datadir
	DataEnv    string // optional data environment name
	Port       int
	AutoRemove bool // --rm: delete all state when the server stops
}

// engineBin resolves a binary of the record's engine via the dist cache.
func engineBin(engName, version, bin string) (string, error) {
	return dist.BinaryPath(engName, version, bin)
}

// Running reports whether the record points at a live process and syncs the
// record's PID from the engine pid file when the record is stale.
func (r *Record) Running() bool {
	if r.PID > 0 && pidAlive(r.PID) {
		return true
	}
	// recover PID from the engine pid file (e.g. record pid recycled)
	if data, err := os.ReadFile(pidFile(r.DataDir)); err == nil {
		if pid, err := strconv.Atoi(string(data)); err == nil && pidAlive(pid) {
			r.PID = pid
			_ = r.save()
			return true
		}
	}
	return false
}

// pidAlive checks process liveness. A zombie answers signal 0 but is dead
// for our purposes, so the process status is consulted through gopsutil
// (works on /proc, libproc and Windows APIs — no external commands).
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := process.NewProcess(int32(pid))
	if err != nil {
		return false // process does not exist
	}
	statuses, err := p.Status()
	if err != nil {
		// exists but status unavailable — fall back to signal 0
		return p.SendSignal(syscall.Signal(0)) == nil
	}
	for _, st := range statuses {
		switch strings.ToLower(strings.TrimSpace(st)) {
		case "", "z", "zombie", "dead", "x", "empty":
			return false // zombie / dead state
		}
	}
	return true
}

// killPID sends SIGTERM (best effort).
func killPID(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGTERM)
}

// forceKillPID sends SIGKILL (last resort escalation).
func forceKillPID(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGKILL)
}

// Start launches an instance: the actual server process is spawned and
// supervised by a per-instance monitor, and Start returns once the server
// accepts connections. Idempotent: an already-running instance is reported,
// not restarted.
func Start(spec Spec, stdout io.Writer) (*Record, error) {
	var existing *Record
	if e, err := load(spec.Name); err == nil {
		existing = e
		if e.Running() {
			fmt.Fprintf(stdout, "instance %q is already running (pid %d, port %d)\n", e.Name, e.PID, e.Port)
			return e, nil
		}
		spec.AutoRemove = spec.AutoRemove || e.AutoRemove
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	// client-side pre-checks so failures surface fast (the monitor retries
	// them anyway)
	eng, err := engine.Get(spec.Engine)
	if err != nil {
		return nil, err
	}
	serverBin, _, _ := eng.BinaryNames()
	binPath, err := engineBin(spec.Engine, spec.Version, serverBin)
	if err != nil {
		return nil, err
	}
	opts := engine.Options{
		Name:    spec.Name,
		BinDir:  filepath.Dir(binPath),
		DataDir: spec.DataDir,
		Port:    spec.Port,
	}
	if !eng.DataDirInitialized(opts) {
		fmt.Fprintf(stdout, "initializing data directory %s\n", spec.DataDir)
		if err := eng.InitDataDir(opts); err != nil {
			return nil, err
		}
	}
	logPath, err := project.ServiceLogPath(spec.Name)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, err
	}

	record := &Record{
		Name:       spec.Name,
		Engine:     spec.Engine,
		Version:    spec.Version,
		DataDir:    spec.DataDir,
		DataEnv:    spec.DataEnv,
		Port:       spec.Port,
		AutoRemove: spec.AutoRemove,
		LogPath:    logPath,
		CreatedAt:  time.Now(),
	}
	if existing != nil {
		record.CreatedAt = existing.CreatedAt
	}
	if err := record.save(); err != nil {
		return nil, err
	}
	if err := record.writeDescriptor(); err != nil {
		return nil, err
	}

	if testing.Testing() {
		// under `go test` self re-execution is impossible: run the monitor
		// inline in the background instead
		go func() { _ = RunMonitor(spec.Name, io.Discard) }()
	} else {
		mpid, err := spawnMonitor(spec.Name)
		if err != nil {
			return nil, err
		}
		record.MonitorPID = mpid
		_ = record.save()
	}

	fmt.Fprintf(stdout, "starting %s@%s on 127.0.0.1:%d\n", spec.Engine, spec.Version, spec.Port)

	// wait until the monitor reports the server ready
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		r, err := load(spec.Name)
		if errors.Is(err, os.ErrNotExist) { // auto-remove reap of a died-instantly instance
			return nil, fmt.Errorf("instance %q failed to start (see %s)", spec.Name, logPath)
		}
		if err == nil && r.Running() {
			return r, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	tail, _ := Tail(logPath, 20)
	return nil, fmt.Errorf("instance %q did not become ready within 90s\n--- last log lines (%s) ---\n%s", spec.Name, logPath, tail)
}

// Stop shuts a running instance down: when its monitor is alive the monitor
// performs the graceful shutdown and the post-exit actions (--rm cleanup);
// otherwise Stop falls back to a direct graceful shutdown.
func Stop(name string, stdout io.Writer) (*Record, error) {
	r, err := load(name)
	if err != nil {
		return nil, err
	}
	if !r.Running() {
		fmt.Fprintf(stdout, "instance %q is not running\n", name)
		return r, nil
	}
	fmt.Fprintf(stdout, "stopping %q (pid %d)\n", name, r.PID)

	monitorAlive := r.MonitorPID > 0 && r.MonitorPID != os.Getpid() && pidAlive(r.MonitorPID)
	if monitorAlive {
		// TERM the monitor: it performs the graceful shutdown and updates
		// the record. We wait for the SERVER to exit — if it survives
		// (e.g. an older monitor without escalation) we kill it ourselves.
		_ = killPID(r.MonitorPID)
		if err := waitExit(r.PID, 35*time.Second); err == nil {
			r2, lerr := load(name)
			if errors.Is(lerr, os.ErrNotExist) { // auto-remove cleanup already ran
				fmt.Fprintf(stdout, "instance %q removed (--rm)\n", name)
				return nil, nil
			}
			if lerr == nil && r2.AutoRemove {
				if _, err := Remove(name, stdout); err != nil {
					return nil, err
				}
				fmt.Fprintf(stdout, "instance %q auto-removed\n", name)
				return nil, nil
			}
			if lerr == nil {
				fmt.Fprintf(stdout, "instance %q stopped\n", name)
			}
			return r2, lerr
		}
		fmt.Fprintf(stdout, "server survived the monitor shutdown; forcing SIGKILL\n")
	}

	if err := gracefulStop(r); err != nil {
		return r, err
	}
	r.PID, r.MonitorPID, r.LastExitCode = 0, 0, 0
	_ = r.save()
	_ = r.writeDescriptor()

	r2, lerr := load(name)
	if errors.Is(lerr, os.ErrNotExist) { // auto-remove cleanup already ran
		fmt.Fprintf(stdout, "instance %q removed (--rm)\n", name)
		return nil, nil
	}
	if lerr != nil {
		return r2, lerr
	}
	if r2.Running() {
		return r2, fmt.Errorf("instance %q did not stop", name)
	}
	if r2.AutoRemove { // direct path: finish the cleanup here
		if _, err := Remove(name, stdout); err != nil {
			return nil, err
		}
		fmt.Fprintf(stdout, "instance %q auto-removed\n", name)
		return nil, nil
	}
	fmt.Fprintf(stdout, "instance %q stopped\n", name)
	return r2, nil
}

// gracefulStop shuts the server down with escalating force: engine admin
// tool -> SIGTERM -> SIGKILL (a wedged server must never survive a kill).
func gracefulStop(r *Record) error {
	if eng, err := engine.Get(r.Engine); err == nil {
		_, _, admin := eng.BinaryNames()
		if adminPath, err := engineBin(r.Engine, r.Version, admin); err == nil {
			opts := engine.Options{DataDir: r.DataDir, Port: r.Port, BinDir: filepath.Dir(adminPath)}
			admin := exec.Command(adminPath, eng.ShutdownArgs(opts)...)
			admin.Env = append(os.Environ(), eng.Env(opts)...)
			if err := admin.Run(); err == nil {
				if err := waitExit(r.PID, 30*time.Second); err == nil {
					return nil
				}
			}
		}
	}
	if err := killPID(r.PID); err == nil {
		if err := waitExit(r.PID, 30*time.Second); err == nil {
			return nil
		}
	}
	if err := forceKillPID(r.PID); err != nil {
		return err
	}
	return waitExit(r.PID, 5*time.Second)
}

// Remove stops the server (if running) and deletes its record.
func Remove(name string, stdout io.Writer) (*Record, error) {
	r, err := load(name)
	if err != nil {
		return nil, err
	}
	if r.Running() {
		if _, err := Stop(name, stdout); err != nil {
			return r, err
		}
	}
	if err := r.remove(); err != nil {
		return nil, err
	}
	fmt.Fprintf(stdout, "instance %q removed\n", name)
	return r, nil
}

// Restart = Stop + Start using the stored record.
func Restart(name string, stdout io.Writer) (*Record, error) {
	r, err := load(name)
	if err != nil {
		return nil, err
	}
	if r.Running() {
		if _, err := Stop(name, stdout); err != nil {
			return nil, err
		}
	}
	return Start(Spec{
		Name:    r.Name,
		Engine:  r.Engine,
		Version: r.Version,
		DataDir: r.DataDir,
		DataEnv: r.DataEnv,
		Port:    r.Port,
	}, stdout)
}

// waitExit polls until the pid is gone.
func waitExit(pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("process %d did not exit within %s", pid, timeout)
}
