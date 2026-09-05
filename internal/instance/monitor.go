package instance

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/dbpod-io/dbpod/internal/dist"
	"github.com/dbpod-io/dbpod/internal/engine"
)

// RunMonitor is the per-instance supervisor (conmon-style): it spawns the
// server process, marks the record running once ready, and performs the
// post-exit actions when the server stops — updating the record to stopped,
// or, with AutoRemove, deleting all instance state including the datadir.
//
// SIGINT/SIGTERM to the monitor trigger a graceful server shutdown first.
// It runs until the server process has exited and the aftermath is handled.
func RunMonitor(name string, stdout io.Writer) error {
	r, err := load(name)
	if err != nil {
		return err
	}
	eng, err := engine.Get(r.Engine)
	if err != nil {
		return err
	}
	serverBin, _, _ := eng.BinaryNames()
	binPath, err := dist.BinaryPath(r.Engine, r.Version, serverBin)
	if err != nil {
		return err
	}
	opts := engine.Options{
		Name:    r.Name,
		BinDir:  filepath.Dir(binPath),
		DataDir: r.DataDir,
		Port:    r.Port,
	}

	// first start of a fresh datadir
	if !eng.DataDirInitialized(opts) {
		fmt.Fprintf(stdout, "initializing data directory %s\n", r.DataDir)
		if err := eng.InitDataDir(opts); err != nil {
			return err
		}
	}

	logFile, err := os.OpenFile(r.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()

	cmd := exec.Command(binPath, eng.ServerArgs(opts)...)
	cmd.Env = append(os.Environ(), eng.Env(opts)...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	cmd.Dir = "/"
	cmd.SysProcAttr = detachedAttr() // own session, independent of any client
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start %s: %w", serverBin, err)
	}
	r.PID = cmd.Process.Pid
	r.MonitorPID = os.Getpid()

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	// mark running once the server accepts connections
	if err := eng.WaitReady(opts, func() bool {
		select {
		case <-waitCh:
			return true // died before becoming ready
		default:
			return false
		}
	}); err != nil {
		tail, _ := Tail(r.LogPath, 20)
		_ = killPID(r.PID)
		<-waitCh
		r.PID, r.LastExitCode = 0, 1
		_ = r.save()
		_ = r.writeDescriptor()
		return fmt.Errorf("%w\n--- last log lines (%s) ---\n%s", err, r.LogPath, tail)
	}
	_ = r.save()
	_ = r.writeDescriptor()
	fmt.Fprintf(stdout, "instance %q running on 127.0.0.1:%d (pid %d)\n", r.Name, r.Port, r.PID)

	// wait for the server to exit, or for a shutdown signal to the monitor
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigs)

	var exitErr error
	select {
	case exitErr = <-waitCh:
	case sig := <-sigs:
		fmt.Fprintf(stdout, "monitor received %v; shutting down the server\n", sig)
		_ = gracefulStop(r)
		exitErr = <-waitCh
	}

	exitCode := 0
	if ee, ok := exitErr.(*exec.ExitError); ok {
		exitCode = ee.ExitCode()
	}

	// post-exit actions
	r2, loadErr := load(r.Name) // the record may already be gone (reaped)
	if loadErr == nil {
		r2.PID, r2.MonitorPID, r2.LastExitCode = 0, 0, exitCode
		_ = r2.save()
		_ = r2.writeDescriptor()
		if r2.AutoRemove {
			if err := r2.remove(); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "instance %q auto-removed\n", r2.Name)
			return nil
		}
	}
	fmt.Fprintf(stdout, "instance %q stopped (exit code %d)\n", r.Name, exitCode)
	return nil
}

// AttachWait polls the instance until its monitor has finished and returns
// the server's last exit code. A vanished record means the auto-remove
// cleanup ran (treated as a clean exit). timeout <= 0 waits forever.
func AttachWait(name string, timeout time.Duration) (int, error) {
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for deadline.IsZero() || time.Now().Before(deadline) {
		r, err := load(name)
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil // auto-removed: clean exit
		}
		if err != nil {
			return 0, err
		}
		monitorAlive := r.MonitorPID > 0 && pidAlive(r.MonitorPID)
		if !monitorAlive && !r.Running() {
			return r.LastExitCode, nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return 0, fmt.Errorf("timed out waiting for instance %q to stop", name)
}

// Reap cleans up auto-remove instances that have stopped without their
// monitor being able to do so (e.g. monitor killed, machine crash). Called
// opportunistically at dbpod startup.
func Reap(stdout io.Writer) {
	records, err := List()
	if err != nil {
		return
	}
	for _, r := range records {
		if !r.AutoRemove || r.Running() {
			continue
		}
		if r.MonitorPID > 0 && pidAlive(r.MonitorPID) {
			continue // supervised: the monitor owns the cleanup
		}
		if err := r.remove(); err != nil {
			fmt.Fprintf(stdout, "reap %q failed: %v\n", r.Name, err)
			continue
		}
		fmt.Fprintf(stdout, "reaped auto-remove instance %q\n", r.Name)
	}
}

// spawnMonitor launches a detached monitor process for the named instance
// (self re-execution of the dbpod binary, conmon-style). The monitor's own
// output goes to the instance log.
func spawnMonitor(name string) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(exe, "monitor", "--name", name)
	cmd.SysProcAttr = detachedAttr()
	if r, err := load(name); err == nil && r.LogPath != "" {
		if f, err := os.OpenFile(r.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			cmd.Stdout, cmd.Stderr = f, f
			defer f.Close()
		}
	}
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	go func() { _ = cmd.Wait() }() // reap the monitor when it exits
	return pid, nil
}
