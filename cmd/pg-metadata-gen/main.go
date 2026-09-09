// Command pg-metadata-gen regenerates the PostgreSQL version index
// (internal/providers/postgres/versions.json) committed into the dbpod
// repository and embedded into the binary.
//
// Version discovery is a GENERATION step: the tool traverses the PGDG apt
// and yum repositories (the authoritative version history) and probes the
// EDB portable zips for win/macOS. The runtime never probes versions; it
// only resolves linux downloads at install time.
//
// Usage:
//
//	go run ./cmd/pg-metadata-gen [-out internal/providers/postgres] [-c 8]
//
// Commit the updated versions.json afterwards.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/dbpod-io/dbpod/internal/metadata"
	"github.com/dbpod-io/dbpod/internal/pgdg"
)

func main() {
	out := flag.String("out", "internal/providers/postgres", "output directory inside the repository")
	concurrency := flag.Int("c", 8, "concurrent EDB probe workers")
	flag.Parse()

	dir := *out
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		fmt.Fprintf(os.Stderr, "error: output directory %q not found (run from the repository root)\n", dir)
		os.Exit(1)
	}
	if err := generate(dir, *concurrency, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stdout, "updated %s/versions.json\n", dir)
}

// generate builds the version index: every PG version PGDG carries, with
// the EDB win/macOS zip URLs probed and recorded per version. Every run
// bumps the content revision so `registry update` can detect freshness.
func generate(dir string, concurrency int, out io.Writer) error {
	ix, err := traverse(out)
	if err != nil {
		return err
	}
	if err := probeEDBEntries(ix, concurrency, out); err != nil {
		return err
	}
	ix.Revision = bumpVersion(currentRevision(dir))
	data, err := json.MarshalIndent(ix, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "versions.json"), data, 0o644)
}

// currentRevision reads the content version of the committed index (""
// when absent).
func currentRevision(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "versions.json"))
	if err != nil {
		return ""
	}
	var ix metadata.Index
	if err := json.Unmarshal(data, &ix); err != nil {
		return ""
	}
	return ix.Revision
}

// bumpVersion returns the next major.minor content version ("0.1" first).
func bumpVersion(v string) string {
	major, minor := 0, 0
	if p := strings.SplitN(v, ".", 2); len(p) == 2 {
		major, _ = strconv.Atoi(p[0])
		minor, _ = strconv.Atoi(p[1])
	}
	return fmt.Sprintf("%d.%d", major, minor+1)
}

// traverse collects every PG version from the PGDG repositories.
func traverse(out io.Writer) (*metadata.Index, error) {
	ix := &metadata.Index{
		Engine:   "postgres",
		Versions: map[string]*metadata.VersionInfo{},
	}
	seen := map[string]bool{}

	add := func(v string) {
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		ix.Versions[v] = &metadata.VersionInfo{
			Version:         v,
			Series:          pgdg.MajorOf(v),
			PackagesFetched: true,
		}
	}

	for _, dir := range pgdg.YumBaselineDirs() {
		for maj := 9; maj <= 18; maj++ {
			versions, err := pgdg.YumSeriesVersions(dir, fmt.Sprint(maj))
			if err != nil {
				continue // baseline/major absent: skip
			}
			fmt.Fprintf(out, "yum %s/%d: %d versions\n", dir, maj, len(versions))
			for _, v := range versions {
				add(v)
			}
		}
	}
	for _, codename := range pgdg.AptCodenames() {
		versions, err := pgdg.AptSeriesVersions(codename)
		if err != nil {
			continue
		}
		fmt.Fprintf(out, "apt %s: %d versions\n", codename, len(versions))
		for _, v := range versions {
			add(v)
		}
	}
	if len(ix.Versions) == 0 {
		return nil, fmt.Errorf("no postgres versions discovered in PGDG repositories")
	}
	fmt.Fprintf(out, "%d versions total\n", len(ix.Versions))
	return ix, nil
}

// probeEDBEntries records the EDB win/macOS zip of every version (HEAD
// probe; URLs are baked into the index so the runtime never probes).
func probeEDBEntries(ix *metadata.Index, concurrency int, out io.Writer) error {
	if concurrency <= 0 {
		concurrency = 8
	}
	type job struct {
		version string
		goos    string
		goarch  string
	}
	versions := make([]string, 0, len(ix.Versions))
	for v := range ix.Versions {
		versions = append(versions, v)
	}
	jobs := make(chan job)
	errs := make(chan error, len(versions)*3)
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				url, ok := pgdg.ProbeEDB(j.version, j.goos, j.goarch)
				if !ok {
					errs <- fmt.Errorf("%s/%s %s: no EDB zip", j.version, j.goos, j.goarch)
					continue
				}
				vi := ix.Versions[j.version]
				vi.Packages = append(vi.Packages, metadata.Package{
					Filename: fmt.Sprintf("postgresql-%s-%s-%s-binaries.zip", j.version, j.goos, j.goarch),
					URL:      url,
					Kind:     "zip",
					OS:       j.goos,
					Arch:     j.goarch,
				})
			}
		}()
	}
	go func() {
		for _, v := range versions {
			for _, plat := range pgdg.EDBPlatformKeys() {
				jobs <- job{v, plat[0], plat[1]}
			}
		}
		close(jobs)
	}()
	wg.Wait()
	close(errs)

	var missing int
	for range errs {
		missing++ // probes failing for some versions/platforms are expected (EDB does not ship everything)
	}
	fmt.Fprintf(out, "EDB probes done, %d platform build(s) not published\n", missing)
	return nil
}
