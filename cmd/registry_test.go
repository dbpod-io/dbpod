package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dbpod-io/dbpod/internal/external"
	"github.com/dbpod-io/dbpod/internal/globalconfig"
	"github.com/dbpod-io/dbpod/internal/metadata"
	"github.com/dbpod-io/dbpod/internal/project"
)

const testManifestYAML = `name: mariadb
family: mysql
init: mariadb-install-db --defaults-file={{ config_path }} --auth-root-authentication-method=normal
start: mariadbd --defaults-file={{ config_path }}
client: mariadb -u root -h {{ host }} -P {{ port }}
exec: mariadb -u root -h {{ host }} -P {{ port }} -e {{ sql }}
shutdown: mariadb-admin -u root -h {{ host }} -P {{ port }} shutdown
config: |
  [mysqld]
  port = {{ port }}
  bind-address = {{ bind_address }}
index_url: "https://example.invalid/mariadb.json"
`

func writeManifestFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mariadb.yaml")
	if err := os.WriteFile(path, []byte(testManifestYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRegistryLifecycle(t *testing.T) {
	t.Setenv("DBPOD_HOME", t.TempDir())
	fixture := writeManifestFixture(t)

	// add → engines.d/mariadb.yaml
	var out strings.Builder
	if err := runRegistryAdd(fixture, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "registered mariadb") {
		t.Errorf("add output = %q", out.String())
	}
	path, disabled, err := manifestPath("mariadb")
	if err != nil || disabled {
		t.Fatalf("manifestPath = %q, %v, disabled=%v", path, err, disabled)
	}
	_ = path

	// duplicate add → error
	if err := runRegistryAdd(fixture, &out); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Errorf("duplicate add err = %v", err)
	}

	// disable → .yaml.disabled, active form gone
	out.Reset()
	if err := runRegistryToggle([]string{"mariadb"}, &out, true); err != nil {
		t.Fatal(err)
	}
	if _, disabled, err := manifestPath("mariadb"); err != nil || !disabled {
		t.Fatalf("after disable: disabled=%v err=%v", disabled, err)
	}
	if err := runRegistryToggle([]string{"mariadb"}, &out, true); err == nil || !strings.Contains(err.Error(), "already disabled") {
		t.Errorf("double disable err = %v", err)
	}

	// enable → active again
	if err := runRegistryToggle([]string{"mariadb"}, &out, false); err != nil {
		t.Fatal(err)
	}
	if _, disabled, err := manifestPath("mariadb"); err != nil || disabled {
		t.Fatalf("after enable: disabled=%v err=%v", disabled, err)
	}

	// rm → manifest and metadata cache gone
	home, err := project.HomeDir()
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(home, "metadata", "mariadb.json")
	if err := os.MkdirAll(filepath.Dir(cache), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runRegistryRm([]string{"mariadb"}, &out); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manifestPath("mariadb"); err == nil {
		t.Error("manifest still present after rm")
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Errorf("metadata cache still present: %v", err)
	}
	if err := runRegistryRm([]string{"mariadb"}, &out); err == nil || !strings.Contains(err.Error(), "not a registered engine") {
		t.Errorf("rm after rm err = %v", err)
	}
}

func TestRegistryAddResolvesRelativeIndexURL(t *testing.T) {
	t.Setenv("DBPOD_HOME", t.TempDir())
	// manifest and index ship side by side; the index_url is relative
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "mariadb.yaml")
	if err := os.WriteFile(src, []byte(strings.Replace(testManifestYAML,
		`index_url: "https://example.invalid/mariadb.json"`, `index_url: mariadb.json`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := runRegistryAdd(src, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "version index not found at") {
		t.Errorf("missing index file should warn: %q", out.String())
	}

	// the stored manifest carries the frozen absolute file URL
	home, err := project.HomeDir()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, "engines.d", "mariadb.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	want := "file://" + filepath.ToSlash(filepath.Join(srcDir, "mariadb.json"))
	if !strings.Contains(string(data), want) {
		t.Errorf("stored manifest missing %q:\n%s", want, data)
	}

	// with a valid index present: verified and cached, no warning
	if err := os.WriteFile(filepath.Join(srcDir, "mariadb.json"),
		[]byte(`{"engine":"mariadb","versions":{"1.0.0":{"version":"1.0.0","series":"1.0"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runRegistryRm([]string{"mariadb"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runRegistryAdd(src, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "index ok: 1 versions") {
		t.Errorf("valid index should verify: %q", out.String())
	}
}

// index_url also accepts an absolute local path (frozen to file://) and
// relative paths resolve per protocol against remote manifest URLs too.
func TestRegistryAddIndexURLForms(t *testing.T) {
	t.Run("absolute local path", func(t *testing.T) {
		t.Setenv("DBPOD_HOME", t.TempDir())
		index := filepath.Join(t.TempDir(), "mariadb.json")
		if err := os.WriteFile(index, []byte(`{"engine":"mariadb","versions":{"1.0.0":{"version":"1.0.0"}}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		src := filepath.Join(t.TempDir(), "mariadb.yaml")
		if err := os.WriteFile(src, []byte(strings.Replace(testManifestYAML,
			`index_url: "https://example.invalid/mariadb.json"`, `index_url: `+index, 1)), 0o644); err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		if err := runRegistryAdd(src, &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "index ok: 1 versions") {
			t.Errorf("abs path index should verify: %q", out.String())
		}
		data, _ := os.ReadFile(filepath.Join(projectHome(t), "engines.d", "mariadb.yaml"))
		if !strings.Contains(string(data), "file://"+filepath.ToSlash(index)) {
			t.Errorf("abs path not frozen to file URL:\n%s", data)
		}
	})

	t.Run("relative against remote manifest URL", func(t *testing.T) {
		t.Setenv("DBPOD_HOME", t.TempDir())
		var hits int64
		mux := http.NewServeMux()
		mux.HandleFunc("/pkg/mariadb.yaml", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, strings.Replace(testManifestYAML,
				`index_url: "https://example.invalid/mariadb.json"`, `index_url: mariadb.json`, 1))
		})
		mux.HandleFunc("/pkg/mariadb.json", func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt64(&hits, 1)
			fmt.Fprint(w, `{"engine":"mariadb","versions":{"1.0.0":{"version":"1.0.0"}}}`)
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()

		var out strings.Builder
		if err := runRegistryAdd(srv.URL+"/pkg/mariadb.yaml", &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "index ok: 1 versions") {
			t.Errorf("remote relative index should resolve+verify: %q", out.String())
		}
		if atomic.LoadInt64(&hits) != 1 {
			t.Errorf("index fetched %d times, want 1", hits)
		}
	})
}

func TestRegistryUpdate(t *testing.T) {
	t.Setenv("DBPOD_HOME", t.TempDir())
	var hits int64
	index := `{"engine":"mariadb","version":"0.1","versions":{"1.0.0":{"version":"1.0.0","series":"1.0"}}}`
	mux := http.NewServeMux()
	mux.HandleFunc("/mariadb.json", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		fmt.Fprint(w, index)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	src := filepath.Join(t.TempDir(), "mariadb.yaml")
	if err := os.WriteFile(src, []byte(strings.Replace(testManifestYAML,
		`index_url: "https://example.invalid/mariadb.json"`, `index_url: `+srv.URL+"/mariadb.json", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := runRegistryAdd(src, &out); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt64(&hits); n != 1 {
		t.Fatalf("add fetched index %d times, want 1", n)
	}

	// mount like the next dbpod invocation would, then update: a locally
	// added manifest records no source — update re-fetches its index only
	manifests, merrs := globalconfig.LoadEngines()
	if len(merrs) != 0 {
		t.Fatalf("manifest errors: %v", merrs)
	}
	external.Mount(manifests, &globalconfig.Config{}, io.Discard)

	out.Reset()
	if err := runRegistryUpdate([]string{"mariadb"}, "", &out); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt64(&hits); n != 2 {
		t.Errorf("update fetched index %d times total, want 2", n)
	}
	if !strings.Contains(out.String(), "index up to date (v0.1, 1 versions)") {
		t.Errorf("update output = %q", out.String())
	}

	// the stored manifest stays the self-contained copy (no source recorded)
	home, err := project.HomeDir()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, "engines.d", "mariadb.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "source:") {
		t.Errorf("local manifest must not record a source:\n%s", data)
	}
}

func projectHome(t *testing.T) string {
	t.Helper()
	home, err := project.HomeDir()
	if err != nil {
		t.Fatal(err)
	}
	return home
}

func TestRegistryGuardBlocksOnInstances(t *testing.T) {
	t.Setenv("DBPOD_HOME", t.TempDir())
	fixture := writeManifestFixtureNamed(t, "mariadb-g")
	if err := runRegistryAdd(fixture, io.Discard); err != nil {
		t.Fatal(err)
	}

	// an instance record referencing the engine blocks rm and disable
	writeInstanceRecord(t, "pg1", "mariadb-g")
	if err := runRegistryRm([]string{"mariadb-g"}, io.Discard); err == nil || !strings.Contains(err.Error(), "pg1") {
		t.Errorf("rm guard err = %v", err)
	}
	if err := runRegistryToggle([]string{"mariadb-g"}, io.Discard, true); err == nil || !strings.Contains(err.Error(), "pg1") {
		t.Errorf("disable guard err = %v", err)
	}
}

func TestRegistryToggleUnknownEngine(t *testing.T) {
	t.Setenv("DBPOD_HOME", t.TempDir())
	err := runRegistryToggle([]string{"no-such-engine"}, io.Discard, true)
	if err == nil || !strings.Contains(err.Error(), "not a registered engine") {
		t.Errorf("toggle unknown engine err = %v", err)
	}
}

// builtin engines toggle their metadata file (disable = hidden from
// engine ls; rm is refused) and update compares content revisions.
func TestRegistryBuiltinSemantics(t *testing.T) {
	atomic.StoreInt32(&remoteRevision, 2)
	t.Setenv("DBPOD_HOME", t.TempDir())

	// serve the canonical update URL locally (revision 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"engine":"mysql","version":"%d.0","versions":{"9.9.9":{"version":"9.9.9","series":"9.9"}}}`, atomic.LoadInt32(&remoteRevision))
	}))
	defer srv.Close()
	oldURL := builtinEngines["mysql"]
	builtinEngines["mysql"] = srv.URL + "/mysql.json"
	t.Cleanup(func() { builtinEngines["mysql"] = oldURL })

	// seed the builtin metadata file
	if err := runRegistryUpdate([]string{"mysql"}, "", io.Discard); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := runRegistryToggle([]string{"mysql"}, &out, true); err != nil {
		t.Fatal(err)
	}
	if !metadata.Disabled("mysql") {
		t.Error("mysql metadata not disabled")
	}

	// update on a disabled builtin is refused
	if err := runRegistryUpdate([]string{"mysql"}, "", &out); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Errorf("update disabled err = %v", err)
	}

	if err := runRegistryRm([]string{"mysql"}, &out); err == nil || !strings.Contains(err.Error(), "not removed") {
		t.Errorf("rm builtin err = %v", err)
	}

	if err := runRegistryToggle([]string{"mysql"}, &out, false); err != nil {
		t.Fatal(err)
	}
	if metadata.Disabled("mysql") {
		t.Error("mysql metadata still disabled")
	}

	// same revision as local: up to date, file untouched
	out.Reset()
	if err := runRegistryUpdate([]string{"mysql"}, "", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "index up to date (v2.0,") {
		t.Errorf("update output = %q", out.String())
	}

	// a newer upstream revision replaces the local file
	atomic.StoreInt32(&remoteRevision, 3)
	out.Reset()
	if err := runRegistryUpdate([]string{"mysql"}, "", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "index updated (v2.0 → v3.0") {
		t.Errorf("update output = %q", out.String())
	}
}

// remoteRevision lets tests change the served metadata revision.
var remoteRevision int32

// A manifest with a recorded source refreshes from it when the upstream
// revision is newer; same/older revisions keep the local copy.
func TestRegistryUpdateManifestFromSource(t *testing.T) {
	t.Setenv("DBPOD_HOME", t.TempDir())

	var upstreamVersion int32 = 1
	manifestBody := func() string {
		return strings.Replace(strings.Replace(testManifestYAML,
			`index_url: "https://example.invalid/mariadb.json"`, `index_url: mariadb.json`, 1),
			`name: mariadb`, fmt.Sprintf("name: mariadb\nversion: \"%d.0\"", atomic.LoadInt32(&upstreamVersion)), 1)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/pkg/mariadb.yaml", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, manifestBody())
	})
	mux.HandleFunc("/pkg/mariadb.json", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"engine":"mariadb","revision":1,"versions":{"1.0.0":{"version":"1.0.0","series":"1.0"}}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var out strings.Builder
	if err := runRegistryAdd(srv.URL+"/pkg/mariadb.yaml", &out); err != nil {
		t.Fatal(err)
	}

	// same revision: up to date
	if err := runRegistryUpdate([]string{"mariadb"}, "", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "up to date (v1.0)") {
		t.Errorf("update output = %q", out.String())
	}

	// upstream bumps to v2: manifest replaced
	atomic.StoreInt32(&upstreamVersion, 2)
	out.Reset()
	if err := runRegistryUpdate([]string{"mariadb"}, "", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "updated mariadb (v1.0 → v2.0") {
		t.Errorf("update output = %q", out.String())
	}

	// the stored manifest carries v2 with the frozen index URL
	home, err := project.HomeDir()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, "engines.d", "mariadb.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `version: "2.0"`) || !strings.Contains(string(data), srv.URL+"/pkg/mariadb.json") {
		t.Errorf("stored manifest = %s", data)
	}
}

func TestRegistryLsStates(t *testing.T) {
	t.Setenv("DBPOD_HOME", t.TempDir())
	fixture := writeManifestFixtureNamed(t, "mariadb-ls")
	if err := runRegistryAdd(fixture, io.Discard); err != nil {
		t.Fatal(err)
	}
	// a disabled second engine and a broken third
	broken := filepath.Join(t.TempDir(), "broken.yaml")
	if err := os.WriteFile(broken, []byte("name: [unclosed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runRegistryAdd(broken, io.Discard); err == nil {
		t.Error("broken manifest should fail validation")
	}
	dir, _ := globalconfig.EnginesDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte("name: [unclosed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runRegistryToggle([]string{"mariadb-ls"}, io.Discard, true); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := runRegistryLs(&out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"mariadb-ls", "disabled", "invalid"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("ls output missing %q:\n%s", want, out.String())
		}
	}
}

func writeInstanceRecord(t *testing.T, name, engine string) {
	t.Helper()
	rec := map[string]any{"name": name, "engine": engine, "version": "1.0.0", "port": 3307}
	data, _ := json.Marshal(rec)
	dir, err := project.InstancesDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeManifestFixtureNamed writes the test manifest with a custom engine name.
func writeManifestFixtureNamed(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".yaml")
	s := strings.Replace(testManifestYAML, "name: mariadb", "name: "+name, 1)
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
