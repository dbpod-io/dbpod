package server

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/shapled/dbpod/internal/dist"
	"github.com/shapled/dbpod/internal/engine"
	"github.com/shapled/dbpod/internal/project"
)

// Spec describes how to start (or resolve) a server.
type Spec struct {
	Name    string
	Engine  string // engine name, e.g. "mysql"
	Version string // full version
	DataDir string // absolute datadir
	DataEnv string // optional data environment name
	Port    int
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

// pidAlive checks process existence (signal 0).
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// killPID sends SIGTERM (best effort).
func killPID(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGTERM)
}

// Start launches a detached background server for the spec, waiting until it
// accepts connections. Idempotent: an already-running server with the same
// name is reported, not restarted.
func Start(spec Spec, stdout io.Writer) (*Record, error) {
	if existing, err := load(spec.Name); err == nil {
		if existing.Running() {
			fmt.Fprintf(stdout, "server %q is already running (pid %d, port %d)\n", existing.Name, existing.PID, existing.Port)
			return existing, nil
		}
	}

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

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	defer logFile.Close()

	cmd := exec.Command(binPath, eng.ServerArgs(opts)...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	cmd.Dir = "/"
	cmd.SysProcAttr = detachedAttr()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start %s: %w", serverBin, err)
	}
	pid := cmd.Process.Pid
	go func() { _ = cmd.Wait() }() // reap the child; it outlives us either way

	record := &Record{
		Name:      spec.Name,
		Engine:    spec.Engine,
		Version:   spec.Version,
		DataDir:   spec.DataDir,
		DataEnv:   spec.DataEnv,
		Port:      spec.Port,
		PID:       pid,
		LogPath:   logPath,
		CreatedAt: time.Now(),
	}

	fmt.Fprintf(stdout, "starting %s@%s on 127.0.0.1:%d (pid %d)\n", spec.Engine, spec.Version, spec.Port, pid)
	if err := eng.WaitReady(opts, nil); err != nil {
		tail, _ := Tail(logPath, 20)
		_ = killPID(pid)
		return nil, fmt.Errorf("%w\n--- last log lines (%s) ---\n%s", err, logPath, tail)
	}
	if err := record.save(); err != nil {
		return nil, err
	}
	return record, nil
}

// Stop performs a graceful shutdown (engine admin tool first, SIGTERM as
// fallback) and waits for the process to exit.
func Stop(name string, stdout io.Writer) (*Record, error) {
	r, err := load(name)
	if err != nil {
		return nil, err
	}
	if !r.Running() {
		fmt.Fprintf(stdout, "server %q is not running\n", name)
		return r, nil
	}
	fmt.Fprintf(stdout, "stopping %q (pid %d)\n", name, r.PID)
	if err := gracefulStop(r); err != nil {
		return r, err
	}
	r.PID = 0
	if err := r.save(); err != nil {
		return nil, err
	}
	fmt.Fprintf(stdout, "server %q stopped\n", name)
	return r, nil
}

func gracefulStop(r *Record) error {
	if eng, err := engine.Get(r.Engine); err == nil {
		_, _, admin := eng.BinaryNames()
		if adminPath, err := engineBin(r.Engine, r.Version, admin); err == nil {
			opts := engine.Options{DataDir: r.DataDir, Port: r.Port, BinDir: filepath.Dir(adminPath)}
			if err := exec.Command(adminPath, eng.ShutdownArgs(opts)...).Run(); err == nil {
				return waitExit(r.PID, 30*time.Second)
			}
		}
	}
	if err := killPID(r.PID); err != nil {
		return err
	}
	return waitExit(r.PID, 30*time.Second)
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
	fmt.Fprintf(stdout, "server %q removed\n", name)
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
