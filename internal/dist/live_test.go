package dist

import (
	"os"
	"testing"
)

// TestLiveInstallRealDownload performs a REAL download (~164MB from the
// official CDN), MD5 verification and extraction. It is intentionally
// excluded from normal test runs because downloading is heavy:
//
//	DBPOD_LIVE_TEST=1 go test ./internal/dist/ -run TestLiveInstallRealDownload -v
//
// All other tests in the repo are offline and never download.
func TestLiveInstallRealDownload(t *testing.T) {
	if os.Getenv("DBPOD_LIVE_TEST") != "1" {
		t.Skip("live download test disabled; set DBPOD_LIVE_TEST=1 to run it (~164MB download)")
	}
	t.Setenv("DBPOD_HOME", t.TempDir())

	const engine, version = "mysql", "8.0.46"
	if Installed(engine, version) {
		t.Fatalf("%s@%s unexpectedly installed in a fresh DBPOD_HOME", engine, version)
	}

	ref, err := ParseRef(engine + "@" + version)
	if err != nil {
		t.Fatal(err)
	}
	if err := Install(ref, "", os.Stderr); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !Installed(engine, version) {
		t.Error("Installed() = false after successful Install")
	}
	if _, err := BinaryPath(engine, version, "mysqld"); err != nil {
		t.Errorf("mysqld binary missing after install: %v", err)
	}
	if Size(engine, version) < 100<<20 {
		t.Errorf("installed size = %d bytes, want >= 100MB", Size(engine, version))
	}
	if p := Path(engine, version); p == "" {
		t.Error("Path() empty after install")
	}
	// cleanup the heavy download from the temp DBPOD_HOME
	if err := Remove(engine, version); err != nil {
		t.Errorf("Remove: %v", err)
	}
	if Installed(engine, version) {
		t.Error("Installed() = true after Remove")
	}
}
