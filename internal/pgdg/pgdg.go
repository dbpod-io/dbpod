// Package pgdg reads the PostgreSQL upstream repositories: PGDG apt/yum
// (version lists and linux server/client package resolution) and EDB
// portable zips (win/macOS). It is shared by the runtime download
// resolution and the metadata generator (cmd/pg-metadata-gen) — it
// contains no generation logic itself.
package pgdg

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
	"github.com/ulikunitz/xz"
)

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

// YumBaselineDirs returns the EL baselines, lowest glibc first.
func YumBaselineDirs() []string {
	dirs := make([]string, 0, len(yumBaselines))
	for _, b := range yumBaselines {
		dirs = append(dirs, b.Dir)
	}
	return dirs
}

// AptCodenames returns the Debian/Ubuntu baselines.
func AptCodenames() []string {
	names := make([]string, 0, len(aptBaselines))
	for _, b := range aptBaselines {
		names = append(names, b.Codename)
	}
	return names
}

// PGVersion strips the Debian revision from a package version:
// "17.11-1.pgdg12+2" → "17.11".
func PGVersion(pkgVersion string) string {
	if i := strings.Index(pkgVersion, "-"); i > 0 {
		return pkgVersion[:i]
	}
	return pkgVersion
}

// MajorOf returns the major of a dotted version ("17.11" → "17").
func MajorOf(version string) string {
	if i := strings.Index(version, "."); i > 0 {
		return version[:i]
	}
	return version
}

// Resolve returns the linux DownloadPlan of a PG version, choosing the
// baseline with the lowest glibc that carries the version.
func Resolve(version string) (dist.DownloadPlan, error) {
	major := MajorOf(version)

	// yum baselines first (lowest glibc: el7 → el8 → el9)
	for _, b := range yumBaselines {
		refs, err := YumResolve(major, b.Dir)
		if err != nil || len(refs) == 0 {
			continue
		}
		return archivePackage(version, refs, rpmExtractRules(major))
	}

	// apt baselines as fallback (bookworm → noble)
	for _, b := range aptBaselines {
		refs, err := AptResolve(major, b.Codename)
		if err != nil || len(refs) == 0 {
			continue
		}
		return archivePackage(version, refs, debExtractRules(major))
	}
	return dist.DownloadPlan{}, fmt.Errorf("no PGDG baseline carries postgres %s", major)
}

// archivePackage assembles the linux DownloadPlan for a set of archive
// downloads (.deb or .rpm); refs[0] is the main archive, the rest ride
// along as dependencies.
func archivePackage(version string, refs []Ref, rules [][2]string) (dist.DownloadPlan, error) {
	plan := dist.DownloadPlan{Version: version}
	for i, d := range refs {
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

// Ref is one .deb/.rpm resolved from a repository index.
type Ref struct {
	URL string
}

// AptResolve resolves the server+client .deb URLs of a PG major in the
// given codename baseline.
func AptResolve(major, codename string) ([]Ref, error) {
	data, err := fetchAndDecompress(fmt.Sprintf("%s/dists/%s-pgdg/main/binary-amd64/Packages.gz", pgdgBase, codename))
	if err != nil {
		return nil, err
	}
	server := fmt.Sprintf("postgresql-%s", major)
	client := fmt.Sprintf("postgresql-client-%s", major)
	want := map[string]bool{server: true, client: true}
	found := map[string]Ref{}

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
			found[name] = Ref{URL: pgdgBase + "/" + file}
		}
	}
	var out []Ref
	for _, n := range []string{server, client} {
		ref, ok := found[n]
		if !ok {
			return nil, fmt.Errorf("package %s not found in %s-pgdg", n, codename)
		}
		out = append(out, ref)
	}
	return out, nil
}

// AptSeriesVersions lists the PG versions (major.minor) of a PG major in a
// codename baseline, newest first.
func AptSeriesVersions(codename string) ([]string, error) {
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
		v := PGVersion(ver)
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

// YumResolve resolves the server+client rpm URLs of a PG major in an EL
// baseline (e.g. "el7").
func YumResolve(major, baseline string) ([]Ref, error) {
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
	var out []Ref
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
		out = append(out, Ref{URL: loc})
	}
	return out, nil
}

// YumSeriesVersions lists the PG versions (major.minor) of a PG major in an
// EL baseline, newest first.
func YumSeriesVersions(baseline, major string) ([]string, error) {
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
		v := PGVersion(p.Version)
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out, nil
}

// YumHasMajor reports whether the baseline carries the PG major.
func YumHasMajor(baseline, major string) bool {
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
// xz when compiled in), returning plain text/bytes.
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

// --- EDB (win/macOS portable zips) -------------------------------------------

// EDBPlatformKeys are the non-linux platforms EDB ships portable zips for.
func EDBPlatformKeys() [][2]string {
	return [][2]string{
		{"darwin", "arm64"},
		{"darwin", "amd64"},
		{"windows", "amd64"},
	}
}

// edbOSName maps GOOS to the EDB download naming.
func edbOSName(goos string) string {
	if goos == "darwin" {
		return "osx"
	}
	return goos
}

// edbArchName maps GOARCH to the EDB download naming.
func edbArchName(goarch string) string {
	if goarch == "amd64" {
		return "x64"
	}
	return goarch
}

// ProbeEDB checks whether the EDB portable zip of a PG version exists for
// the platform (HEAD probe of the constructed URL). Returns the URL and
// whether it exists.
func ProbeEDB(version, goos, goarch string) (string, bool) {
	var url string
	if goos == "darwin" {
		url = fmt.Sprintf("https://get.enterprisedb.com/postgresql/postgresql-%s-1-osx-binaries.zip", version)
	} else {
		url = fmt.Sprintf("https://get.enterprisedb.com/postgresql/postgresql-%s-1-%s-%s-binaries.zip",
			version, edbOSName(goos), edbArchName(goarch))
	}
	req, err := http.NewRequest(http.MethodHead, url, nil)
	if err != nil {
		return url, false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36")
	resp, err := httpClient.Do(req)
	if err != nil {
		return url, false
	}
	resp.Body.Close()
	return url, resp.StatusCode == 200
}
