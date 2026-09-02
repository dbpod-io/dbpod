package mysql

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shapled/dbpod/internal/engine"
	"github.com/shapled/dbpod/internal/project"
)

func TestWriteConfigRendersTemplate(t *testing.T) {
	t.Setenv("DBPOD_HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "instance", "data")
	e := &Engine{}

	opts := engine.Options{
		Name:    "t",
		BinDir:  filepath.Join(root, "basedir", "bin"),
		DataDir: root,
		Port:    3307,
	}
	path, err := e.WriteConfig(opts)
	if err != nil {
		t.Fatal(err)
	}

	// seeded global template
	tplDir, err := project.TemplatesDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(tplDir, "mysql.cnf.tmpl")); err != nil {
		t.Fatalf("global template not seeded: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(data)
	for _, want := range []string{
		"port                              = 3307",
		"basedir                           = " + filepath.Dir(opts.BinDir),
		"datadir                           = " + filepath.Join(root, "data"),
		"socket                            = " + e.socketPath(opts), // may be the hashed temp path
		"tmpdir                            = " + filepath.Join(root, "tmp"),
		"server-id                         = 3307",
		"gtid_mode                         = on",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("rendered config missing %q", want)
		}
	}
	// multi-level dirs pre-created
	for _, d := range []string{"data", "log", "tmp", "bin-logs", "relay-logs", filepath.Join("innodb", "data")} {
		if fi, err := os.Stat(filepath.Join(root, d)); err != nil || !fi.IsDir() {
			t.Errorf("directory %s not created", d)
		}
	}
}

func TestWriteConfigHonorsGlobalOverride(t *testing.T) {
	t.Setenv("DBPOD_HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "inst")
	e := &Engine{}
	opts := engine.Options{
		BinDir:  filepath.Join(root, "basedir", "bin"),
		DataDir: root,
		Port:    3307,
	}
	// first call seeds the template
	if _, err := e.WriteConfig(opts); err != nil {
		t.Fatal(err)
	}
	// user customizes the global template
	tplDir, _ := project.TemplatesDir()
	override := "# my custom header\n[mysqld]\nport = {{ port }}\n"
	if err := os.WriteFile(filepath.Join(tplDir, "mysql.cnf.tmpl"), []byte(override), 0o644); err != nil {
		t.Fatal(err)
	}
	path, err := e.WriteConfig(opts)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "# my custom header") {
		t.Errorf("override template ignored: %q", data)
	}
	if strings.Contains(string(data), "gtid_mode") {
		t.Errorf("default template should not leak into override output")
	}
}
