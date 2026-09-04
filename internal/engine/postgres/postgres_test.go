package postgres

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shapled/dbpod/internal/engine"
)

func testOpts(root string, port int) engine.Options {
	return engine.Options{
		Name:    "pg-test",
		BinDir:  filepath.Join(root, "basedir", "bin"),
		DataDir: root,
		Port:    port,
	}
}

func TestWriteConfigRendersTemplates(t *testing.T) {
	root := filepath.Join(t.TempDir(), "inst")
	opts := testOpts(root, 5433)
	e := &Engine{}

	conf, err := e.WriteConfig(opts)
	if err != nil {
		t.Fatal(err)
	}
	if conf != filepath.Join(root, "postgresql.conf") {
		t.Fatalf("config path = %s", conf)
	}

	data, err := os.ReadFile(conf)
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(data)
	for _, want := range []string{
		"port = 5433",
		"listen_addresses = '127.0.0.1'",
		"unix_socket_directories = '" + filepath.Join(root, "tmp") + "'",
		"log_directory = '" + filepath.Join(root, "log") + "'",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("postgresql.conf missing %q", want)
		}
	}

	hba, err := os.ReadFile(filepath.Join(root, "pg_hba.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(hba), "trust") {
		t.Error("pg_hba.conf should contain trust rules")
	}

	// dirs pre-created
	for _, d := range []string{e.dataDir(opts), e.socketDir(opts), filepath.Join(root, "log")} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Errorf("directory %s not created", d)
		}
	}
}

func TestServerArgsAndSocket(t *testing.T) {
	root := filepath.Join(t.TempDir(), "inst")
	opts := testOpts(root, 5433)
	e := &Engine{}

	args := e.ServerArgs(opts)
	joined := strings.Join(args, " ")
	for _, want := range []string{"-D", filepath.Join(root, "data"), "config_file=" + filepath.Join(root, "postgresql.conf"), "hba_file=" + filepath.Join(root, "pg_hba.conf")} {
		if !strings.Contains(joined, want) {
			t.Errorf("server args missing %q: %v", want, args)
		}
	}

	wantSock := filepath.Join(root, "tmp", ".s.PGSQL.5433")
	if got := e.SocketPath(opts); got != wantSock {
		t.Errorf("SocketPath = %s, want %s", got, wantSock)
	}
}

func TestClientAndExecArgs(t *testing.T) {
	root := t.TempDir()
	opts := testOpts(root, 5433)
	e := &Engine{}

	client := e.ClientArgs(opts)
	if !strings.Contains(strings.Join(client, " "), "-U postgres") ||
		!strings.Contains(strings.Join(client, " "), filepath.Join(root, "tmp")) {
		t.Errorf("client args = %v", client)
	}

	exec := e.ExecArgs(opts, "SELECT 1")
	joined := strings.Join(exec, " ")
	if !strings.Contains(joined, "-c") || !strings.Contains(joined, "SELECT 1") {
		t.Errorf("exec args = %v", exec)
	}
}

func TestDataDirInitialized(t *testing.T) {
	root := t.TempDir()
	opts := testOpts(root, 5433)
	e := &Engine{}

	if e.DataDirInitialized(opts) {
		t.Error("empty root should not be initialized")
	}
	if err := os.MkdirAll(e.dataDir(opts), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.dataDir(opts), "PG_VERSION"), []byte("17\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !e.DataDirInitialized(opts) {
		t.Error("PG_VERSION present but not detected as initialized")
	}
}

func TestEnvInjectsLibraryPath(t *testing.T) {
	root := t.TempDir()
	opts := testOpts(root, 5433)
	opts.BinDir = filepath.Join(root, "basedir", "bin")
	e := &Engine{}

	env := e.Env(opts)
	if len(env) != 1 {
		t.Fatalf("env = %v", env)
	}
	want := "LD_LIBRARY_PATH=" + filepath.Join(root, "basedir", "shared_libs") + ":" + filepath.Join(root, "basedir", "lib")
	if env[0] != want {
		t.Errorf("env[0] = %q, want %q", env[0], want)
	}
}
