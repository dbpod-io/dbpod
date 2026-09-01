package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c := &Config{
		Name:    "myproj",
		Engine:  "mysql",
		Version: "8.0.35",
		Port:    3307,
		InitSQL: []string{"./scripts/schema.sql", "./scripts/seed.sql"},
	}
	if err := Save(dir, c); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Name != "myproj" || loaded.Engine != "mysql" || loaded.Version != "8.0.35" ||
		loaded.Port != 3307 || len(loaded.InitSQL) != 2 {
		t.Errorf("round trip mismatch: %+v", loaded)
	}
}

func TestLoadDefaults(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("engine: mysql\nversion: 9.7.2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != 3306 {
		t.Errorf("default port = %d, want 3306", c.Port)
	}
}

func TestLoadValidation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("port: 3306\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Error("missing engine should fail")
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("engine: [unclosed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Error("invalid yaml should fail")
	}
}

func TestResolveInitSQL(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "schema.sql"), []byte("SELECT 1;"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &Config{InitSQL: []string{"./scripts/schema.sql"}}
	got, err := c.ResolveInitSQL(dir)
	if err != nil || len(got) != 1 {
		t.Fatalf("ResolveInitSQL = %v, %v", got, err)
	}
	if !filepath.IsAbs(got[0]) {
		t.Errorf("path not absolute: %s", got[0])
	}
	c.InitSQL = []string{"./missing.sql"}
	if _, err := c.ResolveInitSQL(dir); err == nil {
		t.Error("missing init-sql should fail")
	}
}

func TestDirSearchesUpward(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, FileName), []byte("engine: mysql\nversion: 8.0.35\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old, _ := os.Getwd()
	if err := os.Chdir(sub); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	// macOS /var is a symlink to /private/var — compare resolved paths
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Dir()
	if err != nil || got != realRoot {
		t.Errorf("Dir() = %q, %v; want %q", got, err, realRoot)
	}
}
