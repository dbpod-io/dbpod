package dataenv

import (
	"os"
	"path/filepath"
	"testing"
)

func setup(t *testing.T) {
	t.Helper()
	t.Setenv("DBPOD_HOME", t.TempDir())
	// project-local dir = cwd/.dbpod; run the test inside a temp cwd
	tmp := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

func TestCreateListRemove(t *testing.T) {
	setup(t)
	if _, err := Create("app-test", "mysql", "8.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := Create("app-test", "mysql", "8.0"); err == nil {
		t.Error("duplicate create should fail")
	}
	if _, err := Create("../escape", "mysql", "8.0"); err == nil {
		t.Error("invalid name should fail")
	}

	envs, err := List()
	if err != nil || len(envs) != 1 || envs[0].Name != "app-test" {
		t.Fatalf("List() = %+v, %v", envs, err)
	}

	e, err := Load("app-test")
	if err != nil {
		t.Fatal(err)
	}
	dp, _ := e.DataPath()
	if filepath.Base(filepath.Dir(dp)) != "app-test" || filepath.Base(dp) != "data" {
		t.Errorf("unexpected datadir layout: %s", dp)
	}
	if err := Remove("app-test"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("app-test"); err == nil {
		t.Error("env should be gone")
	}
}

func TestResolveNamedAndPath(t *testing.T) {
	setup(t)
	if _, err := Create("app-test", "mysql", "8.0"); err != nil {
		t.Fatal(err)
	}
	if !Exists("app-test") {
		t.Error("Exists(app-test) = false")
	}
	e, dp, named, err := Resolve("app-test", "mysql", "8.0")
	if err != nil || e.Name != "app-test" || !named {
		t.Fatalf("Resolve named = %+v, %v, %v", e, named, err)
	}
	if filepath.Base(dp) != "data" {
		t.Errorf("named datadir = %s", dp)
	}
	e2, dp2, named2, err := Resolve("./custom-data", "mysql", "8.0")
	if err != nil {
		t.Fatal(err)
	}
	if e2.Name != "custom-data" || filepath.Base(dp2) != "custom-data" || named2 {
		t.Errorf("Resolve path = %+v, %s, %v", e2, dp2, named2)
	}
	if Exists("nope") {
		t.Error("Exists(nope) = true")
	}
	if _, _, _, err := Resolve("", "mysql", "8.0"); err == nil {
		t.Error("empty spec should fail")
	}
}

func TestDirSize(t *testing.T) {
	setup(t)
	e, _ := Create("app", "mysql", "8.0")
	dp, _ := e.DataPath()
	_ = os.WriteFile(filepath.Join(dp, "a.bin"), make([]byte, 100), 0o644)
	_ = os.MkdirAll(filepath.Join(dp, "sub"), 0o755)
	_ = os.WriteFile(filepath.Join(dp, "sub", "b.bin"), make([]byte, 50), 0o644)
	if got := DirSize(dp); got != 150 {
		t.Errorf("DirSize = %d, want 150", got)
	}
	if got := DirSize(filepath.Join(dp, "missing")); got != 0 {
		t.Errorf("DirSize(missing) = %d, want 0", got)
	}
}
