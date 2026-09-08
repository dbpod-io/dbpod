package mysql

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dbpod-io/dbpod/internal/engine"
	"github.com/dbpod-io/dbpod/internal/project"
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

	// the .example reference mirrors the current default, but no live
	// template is materialized
	tplDir, err := project.TemplatesDir()
	if err != nil {
		t.Fatal(err)
	}
	example, err := os.ReadFile(filepath.Join(tplDir, "mysql.cnf.tmpl.example"))
	if err != nil {
		t.Fatalf("example reference not written: %v", err)
	}
	if string(example) != DefaultProfile().Config {
		t.Errorf("example does not mirror the default template")
	}
	if _, err := os.Stat(filepath.Join(tplDir, "mysql.cnf.tmpl")); !os.IsNotExist(err) {
		t.Errorf("live template must not be materialized: %v", err)
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
	// the user creates the template file: an explicit customization
	tplDir, _ := project.TemplatesDir()
	if err := os.MkdirAll(tplDir, 0o755); err != nil {
		t.Fatal(err)
	}
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
