package postgres

import (
	"bytes"
	"compress/gzip"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/dbpod-io/dbpod/internal/dist"
	"github.com/dbpod-io/dbpod/internal/metadata"
	"github.com/ulikunitz/xz"
)

// pgdg.go: PGDG repository traversal — the authoritative version list and
// the linux package pipeline (server/client/dependency .debs extracted into
// a portable engine directory).

const (
	pgdgBase = "https://apt.postgresql.org/pub/repos/apt"
	yumBase  = "https://download.postgresql.org/pub/repos/yum"
)

// httpClient is the shared client for repository access.
var httpClient = &http.Client{Timeout: 60 * time.Second}

// aptBaseline is a Debian/Ubuntu release baseline of the PGDG apt repo.
type aptBaseline struct {
	Codename string // e.g. "bookworm"
	LibICU   string // e.g. "libicu72" — the icu package of this baseline
}

// yumBaseline is an EL release baseline of the PGDG yum repo.
type yumBaseline struct {
	Dir   string // e.g. "el7"
	Glibc string // informational: the baseline glibc version
}

// Baselines, ordered by ascending glibc: the extraction pipeline picks the
// FIRST baseline that provides the requested PG major, maximizing host
// compatibility (low glibc binaries run everywhere newer).
var yumBaselines = []yumBaseline{
	{Dir: "el7", Glibc: "2.17"},
	{Dir: "el8", Glibc: "2.28"},
	{Dir: "el9", Glibc: "2.34"},
}

var aptBaselines = []aptBaseline{
	{Codename: "bookworm", LibICU: "libicu72"},
	{Codename: "noble", LibICU: "libicu74"},
}

// pgVersion strips the Debian revision from a package version:
// "17.11-1.pgdg12+2" → "17.11".
func pgVersion(pkgVersion string) string {
	if i := strings.Index(pkgVersion, "-"); i > 0 {
		return pkgVersion[:i]
	}
	return pkgVersion
}

func majorOf(version string) string {
	if i := strings.Index(version, "."); i > 0 {
		return version[:i]
	}
	return version
}

// resolvePGDG returns the linux DownloadPlan of a PG version, choosing the
// baseline with the lowest glibc that carries the version.
func resolvePGDG(version string) (dist.DownloadPlan, error) {
	major := majorOf(version)

	// yum baselines first (lowest glibc: el7 → el8 → el9)
	for _, b := range yumBaselines {
		debs, err := yumResolve(major, b.Dir)
		if err != nil || len(debs) == 0 {
			continue
		}
		return archivePackage(version, b.Dir, debs, rpmExtractRules(major))
	}

	// apt baselines as fallback (bookworm → noble)
	for _, b := range aptBaselines {
		debs, err := aptResolve(major, b.Codename)
		if err != nil || len(debs) == 0 {
			continue
		}
		return archivePackage(version, b.Codename, debs, debExtractRules(major))
	}
	return dist.DownloadPlan{}, fmt.Errorf("no PGDG baseline carries postgres %s", major)
}

// pickBaseline selects the lowest-glibc baseline carrying the PG major.
func pickBaseline(version string) (*yumBaseline, error) {
	major := majorOf(version)
	for _, b := range yumBaselines {
		if yumHasMajor(b.Dir, major) {
			return &b, nil
		}
	}
	return nil, fmt.Errorf("no PGDG yum baseline carries postgres %s", major)
}

// archivePackage assembles the linux DownloadPlan for a set of archive
// downloads (.deb or .rpm); debs[0] is the main archive, the rest ride
// along as dependencies.
func archivePackage(version, baseline string, debs []archiveRef, rules [][2]string) (dist.DownloadPlan, error) {
	plan := dist.DownloadPlan{Version: version}
	for i, d := range debs {
		f := dist.DownloadFile{
			URL:  d.URL,
			Kind: "deb",
		}
		if i == 0 {
			f.ExtractRules = rules
		} else {
			plan.Deps = append(plan.Deps, f)
		}
	}
	return plan, nil
}

// debExtractRules maps Debian paths into the portable engine layout.
func debExtractRules(major string) [][2]string {
	lib := "usr/lib/postgresql/" + major
	share := "usr/share/postgresql/" + major
	return [][2]string{
		{lib + "/bin", "bin"},
		{lib + "/lib", "lib"},
		{share, "share"},
	}
}

// rpmExtractRules maps RHEL paths into the portable engine layout.
func rpmExtractRules(major string) [][2]string {
	root := "usr/pgsql-" + major
	return [][2]string{
		{root + "/bin", "bin"},
		{root + "/lib", "lib"},
		{root + "/share", "share"},
	}
}

// --- apt (Packages.gz) ------------------------------------------------------

// debRef is one .deb resolved from a Packages index.
type archiveRef struct {
	URL string
}

// aptResolve resolves the server+client .deb URLs of a PG major in the
// given codename baseline.
func aptResolve(major, codename string) ([]archiveRef, error) {
	data, err := fetchAndDecompress(fmt.Sprintf("%s/dists/%s-pgdg/main/binary-amd64/Packages.gz", pgdgBase, codename))
	if err != nil {
		return nil, err
	}
	server := fmt.Sprintf("postgresql-%s", major)
	client := fmt.Sprintf("postgresql-client-%s", major)
	want := map[string]bool{server: true, client: true}
	found := map[string]archiveRef{}

	for _, block := range strings.Split(string(data), "\n\n") {
		name, file := "", ""
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "Package: "):
				name = strings.TrimPrefix(line, "Package: ")
			case strings.HasPrefix(line, "Filename: "):
				file = strings.TrimPrefix(line, "Filename: ")
			}
		}
		// exact server/client package names only — extension packages
		// (postgresql-16-pgvector, ...) share the prefix and must not match
		if !want[name] || file == "" {
			continue
		}
		if found[name].URL == "" {
			found[name] = archiveRef{URL: pgdgBase + "/" + file}
		}
	}
	var out []archiveRef
	for _, n := range []string{server, client} {
		ref, ok := found[n]
		if !ok {
			return nil, fmt.Errorf("package %s not found in %s-pgdg", n, codename)
		}
		out = append(out, ref)
	}
	return out, nil
}

// aptSeriesVersions lists the PG versions (major.minor) of a PG major in a
// codename baseline, newest first.
func aptSeriesVersions(codename string) ([]string, error) {
	data, err := fetchAndDecompress(fmt.Sprintf("%s/dists/%s-pgdg/main/binary-amd64/Packages.gz", pgdgBase, codename))
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, block := range strings.Split(string(data), "\n\n") {
		name, ver := "", ""
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "Package: "):
				name = strings.TrimPrefix(line, "Package: ")
			case strings.HasPrefix(line, "Version: "):
				ver = strings.TrimPrefix(line, "Version: ")
			}
		}
		// server packages only: postgresql-<major> (extensions like
		// postgresql-16-pgvector carry their own version numbers and would
		// pollute the list)
		if !isServerPkgName(name) || ver == "" {
			continue
		}
		v := pgVersion(ver)
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out, nil
}

// isServerPkgName reports whether name is exactly "postgresql-<major>"
// (a PG server package, not an extension/variant).
func isServerPkgName(name string) bool {
	if !strings.HasPrefix(name, "postgresql-") {
		return false
	}
	rest := strings.TrimPrefix(name, "postgresql-")
	if rest == "" {
		return false
	}
	for _, c := range rest {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// --- yum (primary.xml.gz) ----------------------------------------------------

// rpmPkg is one package parsed from a yum primary index.
type rpmPkg struct {
	Name     string `xml:"name"`
	Version  string `xml:"version"`
	Location string `xml:"location"`
}

type primaryIndex struct {
	Packages []rpmPkg `xml:"package"`
}

// yumResolve resolves the server+client rpm URLs of a PG major in an EL
// baseline (e.g. "el7").
func yumResolve(major, baseline string) ([]archiveRef, error) {
	server := fmt.Sprintf("postgresql-%s", major)
	client := fmt.Sprintf("postgresql-client-%s", major)

	rpms, err := yumPrimaryPackages(baseline, major)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{server: true, client: true}
	found := map[string]rpmPkg{}
	for _, p := range rpms {
		if want[p.Name] && found[p.Name].Location == "" {
			found[p.Name] = p
		}
	}
	var out []archiveRef
	for _, n := range []string{server, client} {
		p, ok := found[n]
		if !ok {
			return nil, fmt.Errorf("package %s not found in yum %s", n, baseline)
		}
		loc := p.Location
		if !strings.HasPrefix(loc, "http") {
			loc = yumBase + "/" + strings.TrimPrefix(loc, "../")
			loc = strings.Replace(loc, "/redhat/../", "/", 1)
		}
		out = append(out, archiveRef{URL: loc})
	}
	return out, nil
}

// yumSeriesVersions lists the PG versions (major.minor) of a PG major in an
// EL baseline, newest first.
func yumSeriesVersions(baseline, major string) ([]string, error) {
	rpms, err := yumPrimaryPackages(baseline, major)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range rpms {
		if p.Name != fmt.Sprintf("postgresql-%s", major) {
			continue
		}
		v := pgVersion(p.Version)
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out, nil
}

// yumHasMajor reports whether the baseline carries the PG major.
func yumHasMajor(baseline, major string) bool {
	paths := []string{
		fmt.Sprintf("%s/%s/redhat/%s/repodata/repomd.xml", yumBase, major, baseline),
		fmt.Sprintf("%s/common/redhat/%s/repodata/repomd.xml", yumBase, baseline),
	}
	for _, p := range paths {
		resp, err := httpClient.Head(p)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return true
			}
		}
	}
	return false
}

// yumPrimaryPackages fetches and parses the primary index of the repo that
// carries the PG major for the baseline, returning server/client rpms.
func yumPrimaryPackages(baseline, major string) ([]rpmPkg, error) {
	var lastErr error
	for _, path := range yumRepoPaths(baseline, major) {
		repomdURL := yumBase + "/" + path + "repodata/repomd.xml"
		data, err := fetchAndDecompress(repomdURL)
		if err != nil {
			lastErr = err
			continue
		}
		var repomd struct {
			Data []struct {
				Type     string `xml:"type,attr"`
				Location struct {
					Href string `xml:"href,attr"`
				} `xml:"location"`
			} `xml:"data"`
		}
		if err := xml.Unmarshal(data, &repomd); err != nil {
			lastErr = err
			continue
		}
		primary := ""
		for _, d := range repomd.Data {
			if d.Type == "primary" {
				primary = d.Location.Href
			}
		}
		if primary == "" {
			lastErr = fmt.Errorf("no primary index in %s", repomdURL)
			continue
		}
		pdata, err := fetchAndDecompress(yumBase + "/" + path + primary)
		if err != nil {
			lastErr = err
			continue
		}
		var idx struct {
			Packages []rpmPkg `xml:"package"`
		}
		if err := xml.Unmarshal(pdata, &idx); err != nil {
			return nil, err
		}
		var out []rpmPkg
		for _, p := range idx.Packages {
			if p.Name == fmt.Sprintf("postgresql-%s", major) || p.Name == fmt.Sprintf("postgresql-client-%s", major) {
				out = append(out, p)
			}
		}
		return out, nil
	}
	return nil, lastErr
}

// yumRepoPaths returns the candidate repo paths for major/baseline — el7
// x86_64 lives under common/, others under <major>/redhat/.
func yumRepoPaths(baseline, major string) []string {
	paths := []string{
		fmt.Sprintf("%s/redhat/%s/", major, baseline),
	}
	if baseline == "el7" {
		paths = append([]string{"common/redhat/rhel-7-x86_64/"}, paths...)
	}
	return paths
}

// --- shared fetch/decompress helpers ----------------------------------------

// fetchAndDecompress GETs a URL and transparently decompresses gzip (and
// xz/zstd when compiled in), returning plain text/bytes.
func fetchAndDecompress(url string) ([]byte, error) {
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		return io.ReadAll(gz)
	}
	if len(data) >= 6 && data[0] == 0xfd && data[1] == 0x37 && data[2] == 0x7a && data[3] == 0x58 && data[4] == 0x5a && data[5] == 0x00 {
		xr, err := xz.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		return io.ReadAll(xr)
	}
	return data, nil
}

// traversePGDG builds the version index from the PGDG repositories: apt
// Packages indexes for the apt baselines + yum primary indexes for the yum
// baselines. Versions absent from PGDG are not supported; per-platform
// packages (win/mac EDB) resolve on demand.
func traversePGDG() (*metadata.Index, error) {
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
			Series:          majorOf(v),
			PackagesFetched: true,
		}
	}

	for _, b := range yumBaselines {
		for maj := 9; maj <= 18; maj++ {
			versions, err := yumSeriesVersions(b.Dir, fmt.Sprint(maj))
			if err != nil {
				continue // baseline/major absent: skip
			}
			for _, v := range versions {
				add(v)
			}
		}
	}
	for _, b := range aptBaselines {
		versions, err := aptSeriesVersions(b.Codename)
		if err != nil {
			continue
		}
		for _, v := range versions {
			add(v)
		}
	}
	if len(ix.Versions) == 0 {
		return nil, fmt.Errorf("no postgres versions discovered in PGDG repositories")
	}
	return ix, nil
}
