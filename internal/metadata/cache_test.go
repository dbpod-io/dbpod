package metadata

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testIndex builds a small valid index for tests.
func testIndex(version string, withPackages bool) *Index {
	ix := &Index{
		Engine:    "mysql",
		FetchedAt: time.Now(),
		Versions: map[string]*VersionInfo{
			version: {Version: version, Series: SeriesOf(version), Latest: true, PackagesFetched: withPackages},
			"8.0.40": {Version: "8.0.40", Series: "8.0", PackagesFetched: withPackages, Packages: []Package{
				{Filename: "mysql-8.0.40-macos15-arm64.tar.gz", URL: CDNURL("8.0", "mysql-8.0.40-macos15-arm64.tar.gz"),
					OS: "darwin", Arch: "arm64", Kind: "tar.gz"},
			}},
		},
	}
	return ix
}

// serveIndex spins up an HTTP server acting as the repo CDN.
func serveIndex(t *testing.T, ix *Index, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_ = json.NewEncoder(w).Encode(ix)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("DBPOD_HOME", t.TempDir())
	return srv
}

func useRepoServer(t *testing.T, srv *httptest.Server) {
	t.Helper()
	old := repoURLs
	repoURLs = []string{srv.URL + "/mysql.json"}
	t.Cleanup(func() { repoURLs = old })
}

func useEmbedded(t *testing.T, ix *Index, err error) {
	t.Helper()
	old := embeddedLoader
	embeddedLoader = func(engine string) (*Index, error) { return ix, err }
	t.Cleanup(func() { embeddedLoader = old })
}

func TestEnsureUsesFreshCacheWithoutNetwork(t *testing.T) {
	// fresh cache must be served even when every source is broken
	srv := serveIndex(t, testIndex("9.9.9", true), http.StatusInternalServerError)
	useRepoServer(t, srv)
	useEmbedded(t, nil, errTest)

	cached := testIndex("8.0.46", true)
	if err := Save("mysql", cached); err != nil {
		t.Fatal(err)
	}
	ix, err := Ensure("mysql", "")
	if err != nil {
		t.Fatal(err)
	}
	if ix.Version("8.0.46") == nil || ix.Version("9.9.9") != nil {
		t.Errorf("fresh cache not used: versions %v", ix.ListVersions())
	}
}

func TestEnsureFetchesRepoWhenCacheStale(t *testing.T) {
	repo := testIndex("9.9.9", true)
	srv := serveIndex(t, repo, http.StatusOK)
	useRepoServer(t, srv)
	useEmbedded(t, nil, errTest)

	stale := testIndex("8.0.46", true)
	stale.FetchedAt = time.Now().Add(-2 * RefreshInterval)
	if err := Save("mysql", stale); err != nil {
		t.Fatal(err)
	}

	ix, err := Ensure("mysql", "")
	if err != nil {
		t.Fatal(err)
	}
	if ix.Version("9.9.9") == nil {
		t.Errorf("repo index not used: %v", ix.ListVersions())
	}
	// and it was cached for the next 24h
	reloaded, err := Load("mysql")
	if err != nil || reloaded.Version("9.9.9") == nil {
		t.Errorf("repo index not persisted: %v", err)
	}
}

func TestEnsureFallsBackToEmbedded(t *testing.T) {
	srv := serveIndex(t, nil, http.StatusInternalServerError)
	useRepoServer(t, srv)
	embedded := testIndex("8.0.46", true)
	useEmbedded(t, embedded, nil)

	ix, err := Ensure("mysql", "")
	if err != nil {
		t.Fatal(err)
	}
	if ix.Version("8.0.46") == nil {
		t.Errorf("embedded fallback not used: %v", ix.ListVersions())
	}
}

func TestEnsureNoSourceAvailable(t *testing.T) {
	srv := serveIndex(t, nil, http.StatusInternalServerError)
	useRepoServer(t, srv)
	useEmbedded(t, nil, errTest)
	if _, err := Ensure("mysql", ""); err == nil {
		t.Error("expected error when repo fails and embedded missing")
	}
}

func TestEnsurePackagesReadOnly(t *testing.T) {
	srv := serveIndex(t, nil, http.StatusInternalServerError)
	useRepoServer(t, srv)

	ix := testIndex("8.0.40", true)
	useEmbedded(t, ix, nil)

	ixp, info, err := EnsurePackages("mysql", "8.0.40", "")
	if err != nil {
		t.Fatal(err)
	}
	if ixp.BaseURL != OfficialDownloadsBase {
		t.Errorf("embedded BaseURL = %q, want official", ixp.BaseURL)
	}
	if len(info.Packages) != 1 {
		t.Errorf("packages = %+v", info.Packages)
	}
	if _, _, err := EnsurePackages("mysql", "1.2.3", ""); err == nil {
		t.Error("unknown version should fail")
	}

	// version without package list must fail with a regen hint
	noPkg := testIndex("7.7.7", false)
	useEmbedded(t, noPkg, nil)
	if _, _, err := EnsurePackages("mysql", "7.7.7", ""); err == nil {
		t.Error("version without packages should fail")
	}
}

func TestEnsureMirrorChain(t *testing.T) {
	// mirror serves metadata: it wins over repo/embedded
	mirrorIndex := testIndex("9.9.9", true)
	mirrorIndex.BaseURL = "" // set by FetchFromMirror
	goodMirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mysql.json" {
			_ = json.NewEncoder(w).Encode(mirrorIndex)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer goodMirror.Close()
	t.Setenv("DBPOD_HOME", t.TempDir())
	// repo and embedded would both fail: only the mirror can satisfy
	useRepoServer(t, serveIndex(t, nil, http.StatusInternalServerError))
	useEmbedded(t, nil, errTest)

	ix, err := Ensure("mysql", goodMirror.URL)
	if err != nil {
		t.Fatal(err)
	}
	if ix.Version("9.9.9") == nil {
		t.Errorf("mirror index not used: %v", ix.ListVersions())
	}
	if ix.BaseURL != goodMirror.URL {
		t.Errorf("BaseURL = %q, want mirror base %q", ix.BaseURL, goodMirror.URL)
	}
}

func TestEnsureMirrorUnusableFallsBack(t *testing.T) {
	// mirror has no metadata: unusable -> chain continues to repo
	emptyMirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer emptyMirror.Close()
	t.Setenv("DBPOD_HOME", t.TempDir())
	repo := testIndex("8.0.46", true)
	useRepoServer(t, serveIndex(t, repo, http.StatusOK))
	useEmbedded(t, nil, errTest)

	ix, err := Ensure("mysql", emptyMirror.URL)
	if err != nil {
		t.Fatal(err)
	}
	if ix.Version("8.0.46") == nil {
		t.Errorf("repo fallback after unusable mirror failed: %v", ix.ListVersions())
	}
	if ix.BaseURL != OfficialDownloadsBase {
		t.Errorf("BaseURL = %q, want official", ix.BaseURL)
	}
}

func TestEnsureFreshCacheFromOtherMirrorIsStale(t *testing.T) {
	t.Setenv("DBPOD_HOME", t.TempDir())
	srv := serveIndex(t, nil, http.StatusInternalServerError)
	useRepoServer(t, srv)
	embedded := testIndex("8.0.46", true)
	useEmbedded(t, embedded, nil)

	// fresh cache pointing at mirror A must NOT be used when mirror B is set
	cached := testIndex("8.0.46", true)
	cached.BaseURL = "https://mirror-a.example.com"
	if err := Save("mysql", cached); err != nil {
		t.Fatal(err)
	}
	ix, err := Ensure("mysql", "https://mirror-b.example.com")
	if err != nil {
		t.Fatal(err)
	}
	// cache rejected (different mirror), repo broken -> embedded
	if ix.BaseURL == cached.BaseURL {
		t.Errorf("stale cross-mirror cache was used (BaseURL=%q)", ix.BaseURL)
	}
	if ix.BaseURL != OfficialDownloadsBase {
		t.Errorf("expected embedded official base, got %q", ix.BaseURL)
	}
}

func TestGenerateIncremental(t *testing.T) {
	ga := readFixture(t, "ga_index.html")
	swapURLs(t, newFakeServer(t, &ga, nil))

	dir := t.TempDir()
	out := filepath.Join(dir, "mysql.json")

	// first generation from an empty file crawls everything the fake serves
	if err := Generate(dir, "mysql", 4, os.Stderr); err != nil {
		t.Fatal(err)
	}
	first, err := loadGenerated(out)
	if err != nil || first == nil {
		t.Fatalf("generated file missing: %v", err)
	}
	got := first.Version("8.0.46")
	if got == nil || !got.PackagesFetched || len(got.Packages) == 0 {
		t.Fatalf("8.0.46 packages not generated: %+v", got)
	}
	marker := got.Packages[0].MD5

	// second generation with a new release on the GA page: only the new
	// version is crawled, existing entries stay untouched
	ga = strings.ReplaceAll(ga, "26.7.0", "26.7.1")
	if err := Generate(dir, "mysql", 4, os.Stderr); err != nil {
		t.Fatal(err)
	}
	second, _ := loadGenerated(out)
	if second.Version("26.7.0") == nil || second.Version("26.7.1") == nil {
		t.Errorf("incremental merge broken: %v %v", second.Version("26.7.0"), second.Version("26.7.1"))
	}
	if now := second.Version("8.0.46"); now.Packages[0].MD5 != marker {
		t.Errorf("existing version was re-crawled/mutated")
	}
}

func TestFetchFromRepoPicksFirstHealthySource(t *testing.T) {
	t.Setenv("DBPOD_HOME", t.TempDir())
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer bad.Close()
	good := serveIndex(t, testIndex("9.9.9", true), http.StatusOK)

	old := repoURLs
	repoURLs = []string{bad.URL + "/x.json", good.URL + "/mysql.json"}
	t.Cleanup(func() { repoURLs = old })

	ix, err := FetchFromRepo()
	if err != nil {
		t.Fatal(err)
	}
	if ix.Version("9.9.9") == nil {
		t.Errorf("healthy source not used: %v", ix.ListVersions())
	}
}

type errTestError struct{}

func (errTestError) Error() string { return "embedded unavailable" }

var errTest = errTestError{}

// TestRealEmbeddedMetadata verifies the gzip-embedded generated data decodes
// and resolves packages for arbitrary versions (uses the real embedded file,
// not the swapped loader).
func TestRealEmbeddedMetadata(t *testing.T) {
	ix, err := Embedded("mysql")
	if err != nil {
		t.Fatal(err)
	}
	if len(ix.Versions) < 250 {
		t.Fatalf("embedded index has only %d versions", len(ix.Versions))
	}
	for _, v := range []string{"8.4.11", "9.7.1"} {
		info := ix.Version(v)
		if info == nil || !info.PackagesFetched {
			t.Fatalf("embedded metadata missing packages for %s", v)
		}
		if _, err := info.Select("darwin", "arm64"); err != nil {
			t.Errorf("select darwin/arm64 for %s: %v", v, err)
		}
	}
	// pre-Apple-Silicon versions must not pretend to have arm64 builds
	old := ix.Version("5.7.36")
	if old == nil || !old.PackagesFetched {
		t.Fatal("embedded metadata missing packages for 5.7.36")
	}
	if _, err := old.Select("linux", "amd64"); err != nil {
		t.Errorf("select linux/amd64 for 5.7.36: %v", err)
	}
	if _, err := old.Select("darwin", "arm64"); err == nil {
		t.Error("5.7.36 should have no darwin/arm64 package")
	}
}
