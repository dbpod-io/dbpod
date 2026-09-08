package external

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbpod-io/dbpod/internal/contract"
	"github.com/dbpod-io/dbpod/internal/dist"
	"github.com/dbpod-io/dbpod/internal/engine"
	"github.com/dbpod-io/dbpod/internal/globalconfig"
	"github.com/dbpod-io/dbpod/internal/metadata"

	// register the builtin mysql provider for the shadow guard test
	_ "github.com/dbpod-io/dbpod/internal/providers/mysql"
)

// testEnv spins up an HTTP server serving a native-format index and a
// fake sums file, and returns an engine manifest pointing at it plus the
// index fetch counter. Each test uses its own engine name:
// registrations are global for the life of the test binary.
func testEnv(t *testing.T, engineName string) (globalconfig.EngineManifest, *globalconfig.Config, *int64) {
	t.Helper()
	t.Setenv("DBPOD_HOME", t.TempDir())

	var hits int64
	mux := http.NewServeMux()
	mux.HandleFunc("/"+engineName+".json", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		fmt.Fprintf(w, `{"engine":%q,"versions":{"11.4.5":{"version":"11.4.5","series":"11.4","lts":true}}}`, engineName)
	})
	mux.HandleFunc("/mariadb-11.4.5/bintar-linux-systemd-x86_64/sha256sums.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "abc  mariadb-11.4.5-linux-systemd-x86_64.tar.gz\ndef  other.tar.gz\n")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// the user layer (config.yaml providers.<name>) carries the source base
	cfg := &globalconfig.Config{Providers: map[string]globalconfig.ProviderConfig{
		engineName: {
			DefaultSource: "local",
			Sources:       map[string]globalconfig.Source{"local": {Base: srv.URL + "/"}},
		},
	}}

	return globalconfig.EngineManifest{
		Name:     engineName,
		Family:   "mysql",
		IndexURL: srv.URL + "/" + engineName + ".json",
		Download: &globalconfig.DownloadSpec{
			Targets: map[string]globalconfig.DownloadTarget{
				"linux/amd64": {
					URL:  "mariadb-{version}/bintar-linux-systemd-x86_64/mariadb-{version}-linux-systemd-x86_64.tar.gz",
					Kind: "tar.gz",
				},
			},
			Checksums: "sha256sums.txt",
		},
		EngineProfile: validProfile(engineName),
	}, cfg, &hits
}

func validProfile(engineName string) contract.EngineProfile {
	p := contract.EngineProfile{
		Init:     "mariadb-install-db --defaults-file={{ config_path }} --auth-root-authentication-method=normal",
		Start:    "mariadbd --defaults-file={{ config_path }}",
		Client:   "mariadb -u root -h {{ host }} -P {{ port }}",
		Exec:     "mariadb -u root -h {{ host }} -P {{ port }} -e {{ sql }}",
		Shutdown: "mariadb-admin -u root -h {{ host }} -P {{ port }} shutdown",
	}
	p.Config = "[mysqld]\nport = {{ port }}\nbind-address = {{ bind_address }}\n"
	p.Engine = engineName
	p.ContractVersion = contract.Version
	return p
}

func TestMountAndLifecycle(t *testing.T) {
	m, cfg, hits := testEnv(t, "mariadb-1")
	var warn strings.Builder
	Mount([]globalconfig.EngineManifest{m}, cfg, &warn)

	if warn.String() != "" {
		t.Fatalf("unexpected warnings: %s", warn.String())
	}

	// engine registered with profile data
	eng, err := engine.Get("mariadb-1")
	if err != nil {
		t.Fatal(err)
	}
	server, client, admin := eng.BinaryNames()
	if server != "mariadbd" || client != "mariadb" || admin != "mariadb-admin" {
		t.Errorf("binary names = %s/%s/%s", server, client, admin)
	}

	// provider registered, versions cached after first fetch
	p, err := dist.ProviderFor("mariadb-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.EnsureVersions(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.EnsureVersions(); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt64(hits); n != 1 {
		t.Errorf("index fetched %d times, want 1 (cache used indefinitely)", n)
	}

	// an aged cache is still served: no auto-refresh
	cacheAge(t, "mariadb-1", 72*time.Hour)
	if _, err := p.EnsureVersions(); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt64(hits); n != 1 {
		t.Errorf("aged cache triggered a refetch (%d hits), want 1", n)
	}

	// series resolution from the index
	v, err := p.ResolveVersion("11.4", "")
	if err != nil || v != "11.4.5" {
		t.Errorf("ResolveVersion(11.4) = %q, %v", v, err)
	}
	if got := p.SeriesOf("11.4.5", true, false); got[0] != "11.4" {
		t.Errorf("SeriesOf = %v", got)
	}
}

// cacheAge rewrites the engine's cached index with an old fetched_at.
func cacheAge(t *testing.T, engine string, age time.Duration) {
	t.Helper()
	ix, err := metadata.Load(engine)
	if err != nil || ix == nil {
		t.Fatalf("no cache for %s: %v", engine, err)
	}
	ix.FetchedAt = time.Now().Add(-age)
	if err := metadata.Save(engine, ix); err != nil {
		t.Fatal(err)
	}
}

func TestResolveDownload(t *testing.T) {
	m, cfg, _ := testEnv(t, "mariadb-2")
	m.Download.Targets["linux/amd64"] = globalconfig.DownloadTarget{
		URL:  "Percona-Server-{series}/Percona-Server-{version}/Percona-Server-{version}-{series}-Linux.tar.gz",
		Kind: "tar.gz",
	}
	m.Download.Checksums = "" // the fake archive tree has no sums for this path
	var warn strings.Builder
	Mount([]globalconfig.EngineManifest{m}, cfg, &warn)
	p, err := dist.ProviderFor("mariadb-2")
	if err != nil {
		t.Fatal(err)
	}

	plan, err := p.ResolveDownload("11.4.5", "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	base := cfg.Providers["mariadb-2"].Sources["local"].Base
	wantURL := strings.TrimSuffix(base, "/") + "/Percona-Server-11.4/Percona-Server-11.4.5/Percona-Server-11.4.5-11.4-Linux.tar.gz"
	if plan.Main.URL != wantURL {
		t.Errorf("url = %q, want %q", plan.Main.URL, wantURL)
	}
	if !strings.Contains(plan.Main.URL, "/Percona-Server-11.4/") || strings.Contains(plan.Main.URL, "{series}") || strings.Contains(plan.Main.URL, "{version}") {
		t.Errorf("{series}/{version} substitution broken: %q", plan.Main.URL)
	}

	// restore the real target and verify checksum/kind handling
	m.Download.Targets["linux/amd64"] = globalconfig.DownloadTarget{
		URL:  "mariadb-{version}/bintar-linux-systemd-x86_64/mariadb-{version}-linux-systemd-x86_64.tar.gz",
		Kind: "tar.gz",
	}
	m.Download.Checksums = "sha256sums.txt"
	plan, err = p.ResolveDownload("11.4.5", "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	wantURL = strings.TrimSuffix(cfg.Providers["mariadb-2"].Sources["local"].Base, "/") + "/mariadb-11.4.5/bintar-linux-systemd-x86_64/mariadb-11.4.5-linux-systemd-x86_64.tar.gz"
	if plan.Main.URL != wantURL {
		t.Errorf("url = %q, want %q", plan.Main.URL, wantURL)
	}
	if plan.Main.SHA256 != "abc" {
		t.Errorf("sha256 = %q, want abc", plan.Main.SHA256)
	}
	if plan.Main.Kind != "tar.gz" {
		t.Errorf("kind = %q", plan.Main.Kind)
	}

	// unsupported platform errors with configured targets
	if _, err := p.ResolveDownload("11.4.5", "darwin", "arm64"); err == nil || !strings.Contains(err.Error(), "linux/amd64") {
		t.Errorf("darwin error = %v", err)
	}
}

func TestExactVersionWithoutIndex(t *testing.T) {
	t.Setenv("DBPOD_HOME", t.TempDir())
	m := globalconfig.EngineManifest{
		Name:   "mariadb-3",
		Family: "mysql",
		Download: &globalconfig.DownloadSpec{
			Targets: map[string]globalconfig.DownloadTarget{
				"linux/amd64": {URL: "mariadb-{version}/x.tar.gz", Kind: "tar.gz"},
			},
		},
		EngineProfile: validProfile("mariadb-3"),
	}
	cfg := &globalconfig.Config{Providers: map[string]globalconfig.ProviderConfig{
		"mariadb-3": {
			DefaultSource: "official",
			Sources:       map[string]globalconfig.Source{"official": {Base: "https://archive.mariadb.org/"}},
		},
	}}
	var warn strings.Builder
	Mount([]globalconfig.EngineManifest{m}, cfg, &warn)
	p, err := dist.ProviderFor("mariadb-3")
	if err != nil {
		t.Fatal(err)
	}
	if v, err := p.ResolveVersion("11.4.5", ""); err != nil || v != "11.4.5" {
		t.Errorf("exact version passthrough = %q, %v", v, err)
	}
	if _, err := p.ResolveVersion("11.4", ""); err == nil {
		t.Error("series without index_url should error")
	}
}

func TestMountSkipsBroken(t *testing.T) {
	t.Setenv("DBPOD_HOME", t.TempDir())
	m := globalconfig.EngineManifest{
		Name:   "mariadb-broken",
		Family: "mysql",
		EngineProfile: func() contract.EngineProfile {
			p := validProfile("mariadb-broken")
			p.Start = "../evil/mariadbd --x" // fails validation
			return p
		}(),
	}
	var warn strings.Builder
	Mount([]globalconfig.EngineManifest{m}, &globalconfig.Config{}, &warn)
	if _, err := engine.Get("mariadb-broken"); err == nil {
		t.Error("engine should not register with an invalid profile")
	}
	if !strings.Contains(warn.String(), "mariadb-broken") {
		t.Errorf("warning = %q", warn.String())
	}
}

func TestMountNeverShadowsBuiltin(t *testing.T) {
	t.Setenv("DBPOD_HOME", t.TempDir())
	m := globalconfig.EngineManifest{
		Name:          "mysql",
		Family:        "mysql",
		EngineProfile: validProfile("mysql"),
	}
	var warn strings.Builder
	Mount([]globalconfig.EngineManifest{m}, &globalconfig.Config{}, &warn)
	p, err := dist.ProviderFor("mysql")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(*Provider); ok {
		t.Error("builtin mysql provider was replaced by a config-declared one")
	}
}

func TestTemplateSeeding(t *testing.T) {
	t.Setenv("DBPOD_HOME", t.TempDir())
	m := globalconfig.EngineManifest{
		Name:          "mariadb-4",
		Family:        "mysql",
		EngineProfile: validProfile("mariadb-4"),
	}
	var warn strings.Builder
	Mount([]globalconfig.EngineManifest{m}, &globalconfig.Config{}, &warn)
	eng, err := engine.Get("mariadb-4")
	if err != nil {
		t.Fatal(err)
	}
	opts := engine.Options{DataDir: t.TempDir(), BinDir: filepath.Join(t.TempDir(), "bin"), Port: 3307}
	path, err := eng.WriteConfig(opts)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "port = 3307") {
		t.Errorf("rendered config missing port: %s", data)
	}
}

func TestLoadEnginesFromDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DBPOD_HOME", home)
	dir := filepath.Join(home, "engines.d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mariadb.yaml"), []byte(
		"name: mariadb\nfamily: mysql\ninit: mariadb-install-db --defaults-file={{ config_path }}\nstart: mariadbd --defaults-file={{ config_path }}\nclient: mariadb -u root -h {{ host }} -P {{ port }}\nexec: mariadb -u root -h {{ host }} -P {{ port }} -e {{ sql }}\nshutdown: mariadb-admin -u root -h {{ host }} -P {{ port }} shutdown\nconfig: \"[mysqld]\\n\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// a non-manifest file must be ignored
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore me"), 0o644); err != nil {
		t.Fatal(err)
	}

	ms, errs := globalconfig.LoadEngines()
	if len(errs) != 0 {
		t.Fatalf("load errors = %v", errs)
	}
	if len(ms) != 1 || ms[0].Name != "mariadb" {
		t.Fatalf("manifests = %+v", ms)
	}
	if ms[0].Start != "mariadbd --defaults-file={{ config_path }}" || ms[0].Family != "mysql" {
		t.Errorf("manifest = %+v", ms[0])
	}

	// missing directory yields no manifests
	t.Setenv("DBPOD_HOME", filepath.Join(home, "nonexistent"))
	ms, errs = globalconfig.LoadEngines()
	if len(errs) != 0 || ms != nil {
		t.Errorf("missing dir: manifests=%v errs=%v", ms, errs)
	}
}
