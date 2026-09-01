package metadata

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// Generate refreshes the generated metadata file inside the repository
// (dataDir/<engine>.json). This is the ONLY place where the MySQL web pages
// are crawled — a developer runs cmd/metadata-gen occasionally and commits
// the result; CLI users never crawl.
//
// The existing file is treated as immutable history: only versions missing
// from it are crawled and merged in (incremental).
func Generate(dataDir, engine string, concurrency int, stdout io.Writer) error {
	outPath := filepath.Join(dataDir, engine+".json")
	ix, err := loadGenerated(outPath)
	if err != nil {
		return err
	}
	if ix == nil {
		ix = &Index{Engine: engine, Versions: map[string]*VersionInfo{}}
	}

	// discover versions (2 requests)
	opt := FetchOption{}
	ga, err := FetchGAIndexVersions(opt)
	if err != nil {
		return err
	}
	archive, err := FetchArchiveVersions(opt)
	if err != nil {
		return err
	}

	// merge version entries without touching existing ones
	pending := map[string]versionSource{}
	for _, vi := range ga {
		if existing, ok := ix.Versions[vi.Version]; ok {
			existing.Latest, existing.LTS = true, vi.LTS
			continue
		}
		ix.Versions[vi.Version] = &VersionInfo{Version: vi.Version, Series: vi.Series, LTS: vi.LTS, Latest: true}
		pending[vi.Version] = sourceGA
	}
	for _, v := range archive {
		if _, ok := ix.Versions[v]; ok {
			continue
		}
		ix.Versions[v] = &VersionInfo{Version: v, Series: SeriesOf(v)}
		pending[v] = sourceArchive
	}
	fmt.Fprintf(stdout, "%d known version(s), %d new to crawl\n", len(ix.Versions), len(pending))

	// crawl package lists for new versions, bounded concurrency
	if err := crawlPackages(ix, pending, engine, concurrency, stdout); err != nil {
		return err
	}
	ix.FetchedAt = time.Now()
	ix.Engine = engine
	return saveGenerated(outPath, ix)
}

type versionSource int

const (
	sourceGA versionSource = iota
	sourceArchive
)

// pace inserts the politeness delay between crawls (1s + rand[0,1]s).
// Tests override it to avoid sleeping.
var pace = func() {
	time.Sleep(time.Second + time.Duration(rand.Float64()*float64(time.Second)))
}

// crawlPackageOne fetches the package list of one version (3 os requests)
// and returns the number of packages found.
func crawlPackageOne(ix *Index, version string, src versionSource) (int, error) {
	info := ix.Versions[version]
	var pkgs []Package
	var err error
	if src == sourceGA {
		pkgs, err = FetchGAPackages(info.Series, FetchOption{})
	} else {
		pkgs, err = FetchArchivePackages(version, FetchOption{})
	}
	if err != nil {
		return 0, err
	}
	for i := range pkgs {
		// relative URL: resolves against the metadata file's parent
		// directory (official downloads base, or a mirror base)
		pkgs[i].URL = RelURL(info.Series, pkgs[i].Filename)
	}
	info.Packages = pkgs
	info.PackagesFetched = true
	return len(pkgs), nil
}

func crawlPackages(ix *Index, pending map[string]versionSource, engine string, concurrency int, stdout io.Writer) error {
	if concurrency <= 0 {
		concurrency = 8
	}
	type job struct {
		version string
		src     versionSource
	}
	jobs := make(chan job)
	errs := make(chan error, len(pending))
	done := make(chan struct{})
	var wg sync.WaitGroup
	var completed atomic.Int64
	total := int64(len(pending))
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				pace() // politeness delay between versions
				n, err := crawlPackageOne(ix, j.version, j.src)
				done1 := completed.Add(1)
				if err != nil {
					errs <- fmt.Errorf("%s: %w", j.version, err)
					fmt.Fprintf(stdout, "  [%d/%d] %s FAILED: %v\n", done1, total, j.version, err)
				} else {
					fmt.Fprintf(stdout, "  [%d/%d] %s: %d packages\n", done1, total, j.version, n)
				}
			}
		}()
	}
	go func() {
		for v, src := range pending {
			jobs <- job{v, src}
		}
		close(jobs)
		close(done)
	}()
	wg.Wait()
	<-done
	close(errs)

	var failures int
	for err := range errs {
		failures++
		fmt.Fprintf(stdout, "warning: %v\n", err)
	}
	fmt.Fprintf(stdout, "crawled package lists for %d version(s), %d failure(s)\n", len(pending)-failures, failures)
	if failures > 0 {
		return fmt.Errorf("%d version(s) failed to crawl; rerun to retry (existing data is kept)", failures)
	}
	return nil
}

// loadGenerated reads the repository metadata file (nil when absent).
func loadGenerated(path string) (*Index, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ix Index
	if err := json.Unmarshal(data, &ix); err != nil {
		return nil, fmt.Errorf("corrupt generated metadata %s: %w", path, err)
	}
	return &ix, nil
}

func saveGenerated(path string, ix *Index) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(ix, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	// compressed twin for embedding (keeps the binary small)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return os.WriteFile(path+".gz", buf.Bytes(), 0o644)
}
