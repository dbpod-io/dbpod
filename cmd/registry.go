package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dbpod-io/dbpod/internal/dist"
	"github.com/dbpod-io/dbpod/internal/external"
	"github.com/dbpod-io/dbpod/internal/fetch"
	"github.com/dbpod-io/dbpod/internal/globalconfig"
	"github.com/dbpod-io/dbpod/internal/instance"
	"github.com/dbpod-io/dbpod/internal/metadata"
	"github.com/dbpod-io/dbpod/internal/project"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var registryCmd = &cobra.Command{
	Use:   "registry",
	Short: "Manage config-declared engines (engines.d manifests)",
	Long: `Manage the engine registry: config-declared engines living in
$DBPOD_HOME/engines.d as manifest files (one engine per file).

  dbpod registry add <path|URL>    register a manifest (validated, index verified)
  dbpod registry ls                registered engines and their status
  dbpod registry update [name]...  refresh version indexes (no auto-refresh)
  dbpod registry rm <name>...      unregister (manifest + metadata cache)
  dbpod registry disable <name>    rename to <name>.yaml.disabled: not mounted
  dbpod registry enable <name>     rename back: mounted again at next start

Version indexes are fetched once at registration and then served from the
local cache indefinitely; ` + "`registry update`" + ` refreshes them explicitly.

Built-in engines (mysql, postgres) are not managed here.`,
}

var registryUpdateCmd = &cobra.Command{
	Use:   "update [name]...",
	Short: "Refresh the version index of registered engines",
	Long: `Re-fetch the version index of the named engines (default: all
registered engines) and refresh the local metadata cache.

--index-url temporarily overrides where the index is fetched from (a
moved source, a repaired checkout). For manifest engines it also
rewrites the stored manifest's index_url to the new address.`,
	Args: cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRegistryUpdate(args, updateIndexURL, os.Stdout)
	},
}

// updateIndexURL is the --index-url temporary override.
var updateIndexURL string

var registryAddCmd = &cobra.Command{
	Use:   "add <path|URL>",
	Short: "Register an engine manifest",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRegistryAdd(args[0], os.Stdout)
	},
}

var registryLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List registered engines",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRegistryLs(os.Stdout)
	},
}

var registryRmCmd = &cobra.Command{
	Use:     "rm <name>...",
	Aliases: []string{"remove"},
	Short:   "Unregister engines (manifest + metadata cache)",
	Args:    cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRegistryRm(args, os.Stdout)
	},
}

var registryDisableCmd = &cobra.Command{
	Use:   "disable <name>...",
	Short: "Disable engines (rename to .yaml.disabled: not mounted)",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRegistryToggle(args, os.Stdout, true)
	},
}

var registryEnableCmd = &cobra.Command{
	Use:   "enable <name>...",
	Short: "Re-enable disabled engines",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRegistryToggle(args, os.Stdout, false)
	},
}

// builtinEngines are the compiled-in engines whose metadata is a registry
// entry of its own: seeded from the binary into the config directory on
// first use, refreshable via `registry update`, disable-able — but never
// removable. The value is the canonical upstream URL `registry update`
// fetches from.
var builtinEngines = map[string]string{
	"mysql":    "https://cdn.jsdelivr.net/gh/dbpod-io/dbpod@master/internal/metadata/data/mysql.json",
	"postgres": "https://cdn.jsdelivr.net/gh/dbpod-io/dbpod@master/internal/providers/postgres/versions.json",
}

func isBuiltin(name string) bool {
	_, ok := builtinEngines[name]
	return ok
}

func init() {
	registryCmd.AddCommand(registryAddCmd, registryLsCmd, registryUpdateCmd, registryRmCmd, registryDisableCmd, registryEnableCmd)
	registryUpdateCmd.Flags().StringVar(&updateIndexURL, "index-url", "", "temporarily fetch the index from this URL instead (manifest engines also persist it)")
	rootCmd.AddCommand(registryCmd)
}

// runRegistryUpdate refreshes the named engines (default: all — builtins
// and registered manifests). Manifest engines have two independent refresh
// lines: the manifest definition (from its recorded source) and the
// version index (from index_url), each gated by its own revision. Builtins
// refresh their metadata from the canonical upstream URL.
func runRegistryUpdate(names []string, indexOverride string, stdout io.Writer) error {
	if len(names) == 0 {
		for name := range builtinEngines {
			names = append(names, name)
		}
		manifests, merrs := globalconfig.LoadEngines()
		for _, me := range merrs {
			fmt.Fprintf(stdout, "note: %v\n", me)
		}
		for _, m := range manifests {
			names = append(names, m.Name)
		}
	}
	var errs []error
	for _, name := range names {
		if isBuiltin(name) {
			url := builtinEngines[name]
			if indexOverride != "" {
				url = indexOverride
			}
			if err := refreshIndex(name, url, stdout); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
			}
			continue
		}
		// manifest engine
		local, err := loadStoredManifest(name)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		indexURL := local.IndexURL
		if indexOverride != "" {
			// repair path: re-point the stored manifest's index source
			local.IndexURL = indexOverride
			if err := persistManifest(local); err != nil {
				errs = append(errs, err)
				continue
			}
			indexURL = indexOverride
			fmt.Fprintf(stdout, "re-pointed %s index to %s\n", name, indexOverride)
		}
		if local.Source != "" {
			changed, uerr := updateManifestFromSource(local, stdout)
			if uerr != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, uerr))
				continue
			}
			if changed {
				// the replaced manifest may carry a new frozen index_url
				if nl, lerr := loadStoredManifest(name); lerr == nil {
					indexURL = nl.IndexURL
				}
			}
		}
		if err := refreshIndex(name, indexURL, stdout); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

// persistManifest writes a manifest (with the managed header) back to its
// engines.d location.
func persistManifest(m *globalconfig.EngineManifest) error {
	out, err := yaml.Marshal(*m)
	if err != nil {
		return err
	}
	path, disabled, err := manifestPath(m.Name)
	if err != nil {
		return err
	}
	if disabled {
		return fmt.Errorf("%s is disabled (dbpod registry enable %s first)", m.Name, m.Name)
	}
	return os.WriteFile(path, append([]byte(managedHeader(m.Name)), out...), 0o644)
}

// refreshIndex fetches a version index, compares its content revision
// against the local metadata file, and saves when newer. Equal revisions
// are kept; older ones are never a downgrade.
func refreshIndex(name, indexURL string, stdout io.Writer) error {
	if metadata.Disabled(name) {
		return fmt.Errorf("disabled (dbpod registry enable %s first)", name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), indexFetchTimeout)
	defer cancel()
	tmp, err := os.CreateTemp("", "dbpod-index-refresh-*")
	if err != nil {
		return err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	if _, err := fetch.Fetch(ctx, indexURL, tmp.Name()); err != nil {
		if u, perr := url.Parse(indexURL); perr == nil && u.Scheme == "file" {
			return fmt.Errorf("index source missing: %s — re-add the engine or fix index_url (dbpod registry update %s --index-url <url>)", indexURL, name)
		}
		return fmt.Errorf("fetch %s: %w (check proxy, or re-point with: dbpod registry update %s --index-url <url>)", indexURL, err, name)
	}
	data, err := os.ReadFile(tmp.Name())
	if err != nil {
		return err
	}
	var ix metadata.Index
	if err := json.Unmarshal(data, &ix); err != nil || len(ix.Versions) == 0 {
		return fmt.Errorf("%s is not a valid version index", indexURL)
	}
	if ix.Engine != name {
		return fmt.Errorf("index declares engine %q, want %q", ix.Engine, name)
	}
	ix.FetchedAt = time.Now()

	local, _ := metadata.Load(name)
	localVer := ""
	if local != nil {
		localVer = local.Revision
	}
	switch versionCompare(ix.Revision, localVer) {
	case 1:
		if err := metadata.Save(name, &ix); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "index updated (v%s → v%s, %d versions)\n", localVer, ix.Revision, len(ix.Versions))
	case 0:
		fmt.Fprintf(stdout, "index up to date (v%s, %d versions)\n", localVer, len(ix.Versions))
	default:
		fmt.Fprintf(stdout, "kept local index (v%s newer than upstream v%s)\n", localVer, ix.Revision)
	}
	return nil
}

// loadStoredManifest reads the stored manifest of a registered engine.
func loadStoredManifest(name string) (*globalconfig.EngineManifest, error) {
	path, disabled, err := manifestPath(name)
	if err != nil {
		return nil, err
	}
	if disabled {
		return nil, fmt.Errorf("disabled (dbpod registry enable %s first)", name)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return globalconfig.ParseManifest(data)
}

// updateManifestFromSource re-fetches the manifest's source and replaces
// the local copy when the source revision is newer. Reports whether the
// local manifest changed.
func updateManifestFromSource(local *globalconfig.EngineManifest, stdout io.Writer) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), indexFetchTimeout)
	defer cancel()
	tmp, err := os.CreateTemp("", "dbpod-manifest-update-*")
	if err != nil {
		return false, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	if _, err := fetch.Fetch(ctx, local.Source, tmp.Name()); err != nil {
		return false, fmt.Errorf("fetch %s: %w", local.Source, err)
	}
	data, err := os.ReadFile(tmp.Name())
	if err != nil {
		return false, err
	}
	upstream, err := globalconfig.ParseManifest(data)
	if err != nil {
		return false, fmt.Errorf("source manifest invalid: %w", err)
	}
	switch {
	case upstream.Version > local.Version:
		// freeze a relative index_url against the source location
		if upstream.IndexURL != "" && !strings.Contains(upstream.IndexURL, "://") {
			resolved, rerr := resolveRelativeIndexURL(local.Source, upstream.IndexURL)
			if rerr != nil {
				return false, fmt.Errorf("resolve index_url %q: %w", upstream.IndexURL, rerr)
			}
			upstream.IndexURL = resolved
		}
		upstream.Source = local.Source
		out, err := yaml.Marshal(upstream)
		if err != nil {
			return false, err
		}
		out = append([]byte(managedHeader(local.Name)), out...)
		path, _, _ := manifestPath(local.Name)
		if err := os.WriteFile(path, out, 0o644); err != nil {
			return false, err
		}
		fmt.Fprintf(stdout, "updated %s (v%s → v%s, %d versions in index)\n", local.Name, local.Version, upstream.Version, len(upstream.IndexURL))
		return true, nil
	case upstream.Version == local.Version:
		fmt.Fprintf(stdout, "%s is up to date (v%s)\n", local.Name, local.Version)
		return false, nil
	default:
		fmt.Fprintf(stdout, "kept local %s (v%s newer than upstream v%s)\n", local.Name, local.Version, upstream.Version)
		return false, nil
	}
}

// manifestPath returns the engines.d file of an engine: the active
// manifest, or the disabled form when only that exists.
func manifestPath(name string) (path string, disabled bool, err error) {
	dir, err := globalconfig.EnginesDir()
	if err != nil {
		return "", false, err
	}
	active := filepath.Join(dir, name+".yaml")
	if _, err := os.Stat(active); err == nil {
		return active, false, nil
	}
	inactive := filepath.Join(dir, name+".yaml.disabled")
	if _, err := os.Stat(inactive); err == nil {
		return inactive, true, nil
	}
	return "", false, fmt.Errorf("%q is not a registered engine (see: dbpod registry ls)", name)
}

// guardNoInstances refuses engine-level operations while instances of the
// engine exist (they would lose stop/exec capability).
func guardNoInstances(names []string) error {
	records, err := instance.List()
	if err != nil {
		return nil // best effort: never block registry work on list errors
	}
	var users []string
	for _, r := range records {
		for _, n := range names {
			if r.Engine == n {
				users = append(users, fmt.Sprintf("%s (uses %s)", r.Name, n))
			}
		}
	}
	if len(users) > 0 {
		return fmt.Errorf("engine(s) still used by instance(s): %s — remove them first: dbpod rm <name>", strings.Join(users, ", "))
	}
	return nil
}

// indexFetchTimeout bounds the index verification at registry add.
const indexFetchTimeout = 10 * time.Second

func runRegistryAdd(src string, stdout io.Writer) error {
	data, err := readManifestSource(src)
	if err != nil {
		return err
	}
	// the user layer of the global config (may carry source settings for
	// this engine later; validation itself needs no base)
	cfg, _ := globalconfig.Load()
	if err != nil {
		return err
	}
	m, err := globalconfig.ParseManifest(data)
	if err != nil {
		return err
	}
	// index_url accepts three forms, frozen into the stored manifest:
	//   - any protocol form ("scheme://...") — used as-is, fetched via
	//     that scheme at runtime
	//   - an absolute local path — converted to a file:// URL
	//   - a relative path — resolved against the source the manifest
	//     came from (local file → its file:// directory, remote → its
	//     URL directory, like a relative link)
	if m.IndexURL != "" && !strings.Contains(m.IndexURL, "://") {
		var resolved string
		if filepath.IsAbs(m.IndexURL) {
			resolved = (&url.URL{Scheme: "file", Path: filepath.ToSlash(m.IndexURL)}).String()
		} else {
			var rerr error
			if resolved, rerr = resolveRelativeIndexURL(src, m.IndexURL); rerr != nil {
				return fmt.Errorf("resolve index_url %q: %w", m.IndexURL, rerr)
			}
		}
		m.IndexURL = resolved
	}
	// record the origin of REMOTE sources so `registry update` can re-fetch
	// and compare manifest revisions. Local sources are not recorded: the
	// engines.d copy is self-contained and must not reference the original
	// path (its index is cached into the config directory at registration).
	if isURL(src) {
		m.Source = src
	}
	if err := external.Validate(*m, cfg); err != nil {
		return fmt.Errorf("manifest invalid: %w", err)
	}
	dir, err := globalconfig.EnginesDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if _, _, err := manifestPath(m.Name); err == nil {
		return fmt.Errorf("%s is already registered; remove it first: dbpod registry rm %s", m.Name, m.Name)
	}
	stored, err := yaml.Marshal(m)
	stored = append([]byte(managedHeader(m.Name)), stored...)
	if err != nil {
		return err
	}
	dst := filepath.Join(dir, m.Name+".yaml")
	if err := os.WriteFile(dst, stored, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "registered %s (family %s) -> %s\n", m.Name, m.Family, dst)
	if m.IndexURL == "" {
		fmt.Fprintf(stdout, "note: no index_url: only exact versions install (e.g. %s@x.y.z)\n", m.Name)
	} else {
		verifyIndex(stdout, m)
	}
	fmt.Fprintf(stdout, "takes effect on the next dbpod invocation; refresh the index later with: dbpod registry update %s\n", m.Name)
	return nil
}

// resolveRelativeIndexURL resolves a relative index_url against the base
// of the manifest source: the manifest's directory for local files (as a
// file:// URL) and the URL directory for remote sources (http(s), webdav, ...).
func resolveRelativeIndexURL(src, index string) (string, error) {
	base, ok := sourceBaseURL(src)
	if !ok {
		return "", fmt.Errorf("cannot resolve relative index_url against source %q", src)
	}
	ref, err := url.Parse(index)
	if err != nil {
		return "", err
	}
	return base.ResolveReference(ref).String(), nil
}

// sourceBaseURL returns the base URL (with trailing slash) a manifest's
// relative references resolve against.
func sourceBaseURL(src string) (*url.URL, bool) {
	if isURL(src) || strings.Contains(src, "://") {
		u, err := url.Parse(src)
		if err != nil {
			return nil, false
		}
		switch u.Scheme {
		case "http", "https", "webdav", "dav", "davs":
			if i := strings.LastIndex(u.Path, "/"); i >= 0 {
				u.Path = u.Path[:i+1] // directory of the manifest URL
			}
			return u, true
		case "file", "cp":
			dir := filepath.ToSlash(filepath.Dir(u.Path)) + "/"
			return &url.URL{Scheme: "file", Path: dir}, true
		default:
			return nil, false
		}
	}
	abs, err := filepath.Abs(filepath.Dir(src))
	if err != nil {
		return nil, false
	}
	return &url.URL{Scheme: "file", Path: filepath.ToSlash(abs) + "/"}, true
}

// verifyIndex fetches the index once (bounded by indexFetchTimeout),
// primes dbpod's metadata cache with it, and prints an actionable hint on
// failure — what is missing and where to put it.
func verifyIndex(stdout io.Writer, m *globalconfig.EngineManifest) {
	ctx, cancel := context.WithTimeout(context.Background(), indexFetchTimeout)
	defer cancel()
	tmp, err := os.CreateTemp("", "dbpod-index-verify-*")
	if err != nil {
		return
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	if _, err := fetch.Fetch(ctx, m.IndexURL, tmp.Name()); err != nil {
		if u, perr := url.Parse(m.IndexURL); perr == nil && (u.Scheme == "file" || u.Scheme == "cp") {
			fmt.Fprintf(stdout, "note: version index not found at %s — generate it (e.g. gen.py > mariadb.json) or fix index_url in %s, then re-add or run: dbpod registry update %s\n", m.IndexURL, m.Name, m.Name)
		} else {
			fmt.Fprintf(stdout, "note: version index unreachable at %s (%v) — fix index_url or check proxy settings; retry later with: dbpod registry update %s\n", m.IndexURL, err, m.Name)
		}
		return
	}
	data, err := os.ReadFile(tmp.Name())
	if err != nil {
		return
	}
	var ix metadata.Index
	if err := json.Unmarshal(data, &ix); err != nil || len(ix.Versions) == 0 {
		fmt.Fprintf(stdout, "note: index at %s is not a valid version index (want metadata.Index JSON with versions) — engine ls will fail until fixed\n", m.IndexURL)
		return
	}
	if ix.Engine != m.Name {
		fmt.Fprintf(stdout, "note: index at %s declares engine %q, want %q\n", m.IndexURL, ix.Engine, m.Name)
		return
	}
	ix.FetchedAt = time.Now()
	if err := metadata.Save(m.Name, &ix); err == nil {
		fmt.Fprintf(stdout, "index ok: %d versions (cached; refresh with: dbpod registry update %s)\n", len(ix.Versions), m.Name)
	}
}

// sourceURL renders the origin of a manifest source: URLs pass through,
// local paths freeze into a file:// URL (so `registry update` can re-fetch
// the manifest later).
func sourceURL(src string) string {
	if isURL(src) {
		return src
	}
	if abs, err := filepath.Abs(src); err == nil {
		return (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}).String()
	}
	return src
}

// readManifestSource loads manifest bytes from a local path or URL.
func readManifestSource(src string) ([]byte, error) {
	if isURL(src) {
		tmp, err := os.CreateTemp("", "dbpod-manifest-*")
		if err != nil {
			return nil, err
		}
		tmp.Close()
		defer os.Remove(tmp.Name())
		if _, err := fetch.Fetch(context.Background(), src, tmp.Name()); err != nil {
			return nil, err
		}
		return os.ReadFile(tmp.Name())
	}
	return os.ReadFile(src)
}

func isURL(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func runRegistryLs(stdout io.Writer) error {
	manifests, merrs := globalconfig.LoadEngines()
	tw := newTable(stdout, "NAME", "FAMILY", "VERSION", "INDEX", "STATUS")

	// builtin engines: metadata seeded from the binary, updatable, not
	// removable; enabled is the default and shown as blank
	names := make([]string, 0, len(builtinEngines))
	for name := range builtinEngines {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		status := ""
		if metadata.Disabled(name) {
			status = "disabled"
		}
		ver := ""
		if ix, err := metadata.Load(name); err == nil && ix != nil {
			ver = ix.Revision
		}
		tw.row(name, name, manifestVersion(ver), "builtin", status)
	}

	for _, m := range manifests {
		tw.row(m.Name, m.Family, manifestVersion(m.Version), indexOwnership(m.IndexURL), "")
	}
	// disabled manifests are not part of LoadEngines output (they are
	// skipped at mount time) — scan the disabled files directly
	if dir, err := globalconfig.EnginesDir(); err == nil {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || (!strings.HasSuffix(name, ".yaml.disabled") && !strings.HasSuffix(name, ".yml.disabled")) {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			m, perr := globalconfig.ParseManifest(data)
			if perr != nil {
				tw.row(strings.TrimSuffix(name, ".disabled"), "-", "-", "-", "invalid: "+perr.Error())
				continue
			}
			tw.row(m.Name, m.Family, manifestVersion(m.Version), indexOwnership(m.IndexURL), "disabled")
		}
	}
	for _, me := range merrs {
		tw.row(strings.TrimSuffix(me.File, filepath.Ext(me.File)), "-", "-", "-", "invalid: "+me.Err.Error())
	}
	tw.flush()
	return nil
}

// manifestVersion renders a manifest version for display ("-" when unset).
func manifestVersion(v string) string {
	if v == "" {
		return "-"
	}
	return v
}

// indexOwnership describes who owns the index location: a local snapshot
// under dbpod's own config directory (managed — survives the source being
// deleted) or a remote URL (`registry update` can refresh from it).
func indexOwnership(indexURL string) string {
	switch {
	case indexURL == "":
		return "-"
	case isURL(indexURL):
		return indexURL
	default:
		return "managed"
	}
}

func runRegistryRm(names []string, stdout io.Writer) error {
	if err := guardNoInstances(names); err != nil {
		return err
	}
	var errs []error
	for _, name := range names {
		if isBuiltin(name) {
			errs = append(errs, fmt.Errorf("%s is a built-in engine: its metadata can be updated or disabled, but not removed", name))
			continue
		}
		path, _, err := manifestPath(name)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := os.Remove(path); err != nil {
			errs = append(errs, err)
			continue
		}
		if dir, err := project.MetadataDir(); err == nil {
			_ = os.Remove(filepath.Join(dir, name+".json")) // best effort
		}
		fmt.Fprintf(stdout, "unregistered %s\n", name)
		if n := countInstalledVersions(name); n > 0 {
			fmt.Fprintf(stdout, "note: %d installed version(s) remain (dbpod engine rm %s@<version>)\n", n, name)
		}
	}
	return errors.Join(errs...)
}

func runRegistryToggle(names []string, stdout io.Writer, disable bool) error {
	var errs []error
	for _, name := range names {
		if isBuiltin(name) {
			// builtins toggle their metadata file; the engine itself stays
			// compiled in, so no instance guard is needed
			if err := metadata.SetDisabled(name, disable); err != nil {
				errs = append(errs, err)
				continue
			}
			if disable {
				fmt.Fprintf(stdout, "disabled %s (hidden from engine ls; dbpod registry enable %s to restore)\n", name, name)
			} else {
				fmt.Fprintf(stdout, "enabled %s\n", name)
			}
			continue
		}
		path, disabled, err := manifestPath(name)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if disable {
			if err := guardNoInstances([]string{name}); err != nil {
				errs = append(errs, err)
				continue
			}
			if disabled {
				errs = append(errs, fmt.Errorf("%s is already disabled", name))
				continue
			}
			if err := os.Rename(path, path+".disabled"); err != nil {
				errs = append(errs, err)
				continue
			}
			fmt.Fprintf(stdout, "disabled %s\n", name)
		} else {
			if !disabled {
				errs = append(errs, fmt.Errorf("%s is not disabled", name))
				continue
			}
			if err := os.Rename(path, strings.TrimSuffix(path, ".disabled")); err != nil {
				errs = append(errs, err)
				continue
			}
			fmt.Fprintf(stdout, "enabled %s\n", name)
		}
	}
	return errors.Join(errs...)
}

// countInstalledVersions counts locally installed versions of an engine.
func countInstalledVersions(engine string) int {
	n := 0
	if refs, err := dist.ListLocal(); err == nil {
		for _, ref := range refs {
			if ref.Engine == engine {
				n++
			}
		}
	}
	return n
}

// managedHeader is prepended to every manifest stored in engines.d: the
// file is a managed artifact (replaced wholesale by `registry update`),
// not a hand-edited config.
func managedHeader(name string) string {
	return "# managed by dbpod registry — do not edit; this file is replaced by\n" +
		"# `dbpod registry update " + name + "`. Engine settings go here:\n" +
		"# https://github.com/dbpod-io/dbpod\n"
}

// versionCompare compares two major.minor version strings ("0.1"): 1 when
// a is newer, -1 when b is newer, 0 when equal. Unset ("") counts as "0.0".
func versionCompare(a, b string) int {
	if a == "" {
		a = "0.0"
	}
	if b == "" {
		b = "0.0"
	}
	as, bs := strings.SplitN(a, ".", 2), strings.SplitN(b, ".", 2)
	for i := 0; i < 2; i++ {
		x, y := part(as, i), part(bs, i)
		if x != y {
			if x > y {
				return 1
			}
			return -1
		}
	}
	return 0
}

func part(parts []string, i int) int {
	if i >= len(parts) {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(parts[i]))
	if err != nil {
		return 0
	}
	return n
}
