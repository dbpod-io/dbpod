package instance

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/shapled/dbpod/internal/engine"
	"github.com/shapled/dbpod/internal/project"
)

// fakeEngine simulates a database engine: its "mysqld" is a shell script
// that writes its pid to --pid-file and sleeps; its "mysqladmin shutdown"
// TERMs that pid — enough to exercise the whole lifecycle offline.
type fakeEngine struct{}

func (f *fakeEngine) Name() string { return "fake" }

func (f *fakeEngine) BinaryNames() (server, client, admin string) {
	return "mysqld", "mysql", "mysqladmin"
}

func (f *fakeEngine) ExecPaths() []string { return []string{"bin"} }

func (f *fakeEngine) SocketPath(opts engine.Options) string {
	return filepath.Join(opts.DataDir, "mysql.sock")
}

func (f *fakeEngine) WriteConfig(opts engine.Options) (string, error) {
	if err := os.MkdirAll(opts.DataDir, 0o755); err != nil {
		return "", err
	}
	cfg := filepath.Join(opts.DataDir, "my.cnf")
	return cfg, os.WriteFile(cfg, []byte("[mysqld]\n"), 0o644)
}

func (f *fakeEngine) DataDirInitialized(opts engine.Options) bool {
	_, err := os.Stat(opts.DataDir)
	return err == nil
}

func (f *fakeEngine) InitDataDir(opts engine.Options) error {
	return os.MkdirAll(opts.DataDir, 0o755)
}

func (f *fakeEngine) ServerArgs(opts engine.Options) []string {
	return []string{"--pid-file=" + filepath.Join(opts.DataDir, "dbpod.pid")}
}

func (f *fakeEngine) WaitReady(opts engine.Options, timeout func() bool) error {
	return nil // ready immediately
}

func (f *fakeEngine) ShutdownArgs(opts engine.Options) []string {
	return []string{"--pid-file=" + filepath.Join(opts.DataDir, "dbpod.pid"), "shutdown"}
}

func (f *fakeEngine) ClientArgs(opts engine.Options) []string { return nil }

func (f *fakeEngine) ExecArgs(opts engine.Options, inlineSQL string) []string { return nil }

func init() { engine.Register(&fakeEngine{}) }

// mysqldScript writes its pid to --pid-file, then sleeps until TERMed.
const mysqldScript = `#!/bin/sh
for a in "$@"; do
  case "$a" in --pid-file=*) echo $$ > "${a#--pid-file=}" ;; esac
done
trap 'exit 0' TERM
sleep 30 &
wait
`

// mysqladminScript reads the pidfile and TERMs the server (shutdown).
const mysqladminScript = `#!/bin/sh
for a in "$@"; do
  case "$a" in --pid-file=*) kill -TERM "$(cat "${a#--pid-file=}")" 2>/dev/null ;; esac
done
exit 0
`

// fakeDist installs a fake engine distribution into a temp DBPOD_HOME.
func fakeDist(t *testing.T, engineName, version string) {
	t.Helper()
	t.Setenv("DBPOD_HOME", t.TempDir())
	base, err := project.ImageDir(engineName, version)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(base, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"mysqld":     mysqldScript,
		"mysqladmin": mysqladminScript,
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, ".dbpod-root"), []byte(".\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func instancesBase(t *testing.T) string {
	t.Helper()
	dir, err := project.InstancesDir()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func mustStart(t *testing.T, name string) *Record {
	t.Helper()
	r, err := Start(Spec{
		Name:    name,
		Engine:  "fake",
		Version: "1.0.0",
		DataDir: filepath.Join(instancesBase(t), name, "data"),
		Port:    13300,
	}, io.Discard)
	if err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	return r
}

func requireState(t *testing.T, name string, wantRunning bool) *Record {
	t.Helper()
	r, err := load(name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	if r.Running() != wantRunning {
		t.Fatalf("instance %s running = %v, want %v (pid %d)", name, r.Running(), wantRunning, r.PID)
	}
	return r
}

func TestLifecycleStartRunKillRestartRemove(t *testing.T) {
	fakeDist(t, "fake", "1.0.0")
	const name = "t1"

	// fresh -> Start -> running
	r1 := mustStart(t, name)
	defer func() { _, _ = Stop(name, io.Discard) }()
	if r1.PID <= 0 || !r1.Running() {
		t.Fatalf("after Start: pid=%d running=%v", r1.PID, r1.Running())
	}
	pid1 := r1.PID

	// record persisted (global) and descriptor written into the datadir
	requireState(t, name, true)
	if _, err := os.Stat(filepath.Join(r1.DataDir, descriptorName)); err != nil {
		t.Fatalf("descriptor missing after start: %v", err)
	}

	// running -> Start -> no-op, same process
	r2, err := Start(Spec{Name: name, Engine: "fake", Version: "1.0.0",
		DataDir: r1.DataDir, Port: r1.Port}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if r2.PID != pid1 {
		t.Fatalf("second Start respawned the process: %d -> %d", pid1, r2.PID)
	}

	// running -> Stop -> stopped (record PID 0, descriptor updated)
	if _, err := Stop(name, io.Discard); err != nil {
		t.Fatal(err)
	}
	r3 := requireState(t, name, false)
	if r3.PID != 0 {
		t.Fatalf("record PID not cleared after stop: %d", r3.PID)
	}
	desc, err := loadDescriptor(r3.DataDir)
	if err != nil || desc.PID != 0 {
		t.Fatalf("descriptor not updated on stop: %+v, %v", desc, err)
	}

	// stopped -> Stop -> no-op without error
	if _, err := Stop(name, io.Discard); err != nil {
		t.Fatalf("stop of stopped instance: %v", err)
	}

	// stopped -> Start -> running again with a new pid
	r4, err := Start(Spec{Name: name, Engine: "fake", Version: "1.0.0",
		DataDir: r1.DataDir, Port: r1.Port}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !r4.Running() || r4.PID == pid1 {
		t.Fatalf("restart: pid=%d (was %d), running=%v", r4.PID, pid1, r4.Running())
	}

	// running -> Remove -> stopped AND gone (record + descriptor deleted)
	if _, err := Remove(name, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := load(name); err == nil {
		t.Error("record still exists after Remove")
	}
	if _, err := os.Stat(filepath.Join(r4.DataDir, descriptorName)); !os.IsNotExist(err) {
		t.Error("descriptor still exists after Remove")
	}
}

func TestLifecycleRemoveStopped(t *testing.T) {
	fakeDist(t, "fake", "1.0.0")
	const name = "t2"

	mustStart(t, name)
	if _, err := Stop(name, io.Discard); err != nil {
		t.Fatal(err)
	}
	// stopped -> Remove -> gone
	if _, err := Remove(name, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := load(name); err == nil {
		t.Error("record still exists after Remove")
	}
}

// loadDescriptor reads the datadir descriptor for assertions.
func loadDescriptor(dataDir string) (*Record, error) {
	data, err := os.ReadFile(filepath.Join(dataDir, descriptorName))
	if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// TestRunningPidFallback verifies PID discovery: a live pid recorded only in
// the engine pidfile marks the record running; a dead pid does not.
func TestRunningPidFallback(t *testing.T) {
	dir := t.TempDir()

	// a real, short-lived process as the "server"
	sleep := exec.Command("sleep", "5")
	if err := sleep.Start(); err != nil {
		t.Fatal(err)
	}
	livePID := sleep.Process.Pid
	defer func() { _ = sleep.Process.Kill() }()

	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatalf("true: %v", err)
	}
	deadPID := dead.Process.Pid

	r := &Record{Name: "x", DataDir: dir}
	if r.Running() {
		t.Error("empty record should not be running")
	}

	// live pid in the engine pidfile, record pid unset -> recovered
	if err := os.WriteFile(filepath.Join(dir, "dbpod.pid"), []byte(fmt.Sprint(livePID)), 0o644); err != nil {
		t.Fatal(err)
	}
	if !r.Running() {
		t.Error("live pidfile should mark the record running")
	}
	if r.PID != livePID {
		t.Errorf("record pid = %d, want recovered %d", r.PID, livePID)
	}

	// dead pid everywhere -> not running
	if err := os.WriteFile(filepath.Join(dir, "dbpod.pid"), []byte(fmt.Sprint(deadPID)), 0o644); err != nil {
		t.Fatal(err)
	}
	r.PID = 0
	if r.Running() {
		t.Error("dead pidfile should not mark the record running")
	}
}
