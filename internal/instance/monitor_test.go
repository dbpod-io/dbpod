package instance

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// mustStartAuto starts an instance with a given --rm flag.
func mustStartAuto(t *testing.T, name string, autoRemove bool) *Record {
	t.Helper()
	r, err := Start(Spec{
		Name:       name,
		Engine:     "fake",
		Version:    "1.0.0",
		DataDir:    filepath.Join(instancesBase(t), name, "data"),
		Port:       13400,
		AutoRemove: autoRemove,
	}, io.Discard)
	if err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	return r
}

// killServer TERMs the fake mysqld (simulating an external stop or crash)
// and waits until the process is gone.
func killServer(t *testing.T, r *Record) {
	t.Helper()
	if err := killPID(r.PID); err != nil {
		t.Fatalf("term server: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !pidAlive(r.PID) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("fake server did not exit")
}

// eventually polls cond until it holds or the timeout expires.
func eventually(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("condition not reached in time")
}

func TestMonitorAutoRemoveOnServerExit(t *testing.T) {
	fakeDist(t, "fake", "1.0.0")
	const name = "rm1"

	r := mustStartAuto(t, name, true)
	defer func() { _, _ = Remove(name, io.Discard) }()

	killServer(t, r)

	// the monitor notices the exit and removes everything
	eventually(t, 10*time.Second, func() bool {
		_, err := load(name)
		return err != nil
	})
	if _, err := os.Stat(r.DataDir); !os.IsNotExist(err) {
		t.Errorf("datadir still present after auto-remove: %v", err)
	}
}

func TestMonitorUpdatesRecordOnServerExit(t *testing.T) {
	fakeDist(t, "fake", "1.0.0")
	const name = "keep1"

	r := mustStartAuto(t, name, false)
	defer func() { _, _ = Remove(name, io.Discard) }()

	killServer(t, r)

	// without --rm the record survives as stopped, with the exit code recorded
	eventually(t, 10*time.Second, func() bool {
		r2, err := load(name)
		return err == nil && !r2.Running()
	})
	r2, err := load(name)
	if err != nil {
		t.Fatal(err)
	}
	if r2.PID != 0 {
		t.Errorf("record pid = %d, want 0", r2.PID)
	}
	if r2.LastExitCode != 0 {
		t.Errorf("last exit code = %d, want 0", r2.LastExitCode)
	}
	if _, err := os.Stat(r2.DataDir); err != nil {
		t.Errorf("datadir should survive without --rm: %v", err)
	}
}

func TestReapAutoRemoveInstances(t *testing.T) {
	fakeDist(t, "fake", "1.0.0")
	base := instancesBase(t)

	// tombstone: auto-remove instance that stopped without its monitor
	tomb := &Record{
		Name: "tomb", Engine: "fake", Version: "1.0.0",
		DataDir:    filepath.Join(base, "tomb", "data"),
		Port:       13500,
		AutoRemove: true,
		CreatedAt:  time.Now(),
	}
	if err := tomb.save(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(tomb.DataDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// a running auto-remove instance must survive the reap
	sleep := exec.Command("sleep", "5")
	if err := sleep.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sleep.Process.Kill() }()
	live := &Record{
		Name: "live", Engine: "fake", Version: "1.0.0",
		DataDir:    filepath.Join(base, "live", "data"),
		Port:       13501,
		PID:        sleep.Process.Pid,
		AutoRemove: true,
		CreatedAt:  time.Now(),
	}
	if err := live.save(); err != nil {
		t.Fatal(err)
	}

	Reap(io.Discard)

	if _, err := load("tomb"); err == nil {
		t.Error("tombstone record was not reaped")
	}
	if _, err := os.Stat(tomb.DataDir); !os.IsNotExist(err) {
		t.Error("tombstone datadir was not reaped")
	}
	if _, err := load("live"); err != nil {
		t.Errorf("running auto-remove instance must survive the reap: %v", err)
	}
}
