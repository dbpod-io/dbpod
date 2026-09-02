package cmd

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/shapled/dbpod/internal/dist"
	"github.com/shapled/dbpod/internal/instance"
	"github.com/shapled/dbpod/internal/metadata"
	"github.com/spf13/cobra"
)

var (
	engineLsAll       bool
	engineLsInstalled bool
	engineLsLts       bool
	engineLsSeries    bool
	engineLsPath      bool
)

var engineCmd = &cobra.Command{
	Use:   "engine",
	Short: "Manage database engine binaries",
}

var engineLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List engine versions (default: --installed --series; flags are filters combined as a union)",
	Long: `List engine versions.

All flags are FILTERS combined as a UNION: an entry is listed when it matches
ANY given flag. With no flag the default is --installed --series.

Entry types:
  version  a single engine version, matched by --installed, --all, --lts
  series   stands for the latest version of a release series (e.g. 8.0),
           matched by --series and displayed as "<series> (<latest>)".
           Calendar-versioned non-LTS releases collapse into a single
           "innovation" series; LTS calendar releases keep their own series.

The union is deduplicated: when a version equals a series' latest, only the
series entry is shown.

Columns are always: ENGINE VERSION LTS STATUS SIZE.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEngineLs()
	},
}

// lsEntry is one version in the engine-ls universe (merged from the metadata
// index and local installs).
type lsEntry struct {
	Version   string
	LTS       bool
	Installed bool
	Available bool // has an installable package for the current platform
}

// lsRow is one computed output row of engine ls.
type lsRow struct {
	Label     string // version or "series (latest)"
	SortVer   string // underlying version: ordering, status and size key
	LTS       bool
	Status    string // "installed", "unavailable" or "" (installable)
	Installed bool
}

// status derives the STATUS column: installed wins; a version without a
// current-platform package is marked unavailable (kept visible for future
// third-party package channels).
func statusOf(installed, available bool) string {
	switch {
	case installed:
		return "installed"
	case !available:
		return "unavailable"
	default:
		return ""
	}
}

// engineLsFlags resolves the effective filter set: with no flag given the
// default is --installed --series.
func engineLsFlags() (wantInstalled, wantAll, wantLts, wantSeries bool) {
	anyFlag := engineLsAll || engineLsInstalled || engineLsLts || engineLsSeries
	return engineLsInstalled || !anyFlag,
		engineLsAll,
		engineLsLts,
		engineLsSeries || !anyFlag
}

// buildLsRows applies the (unioned, deduplicated) filter logic of engine ls.
// Series entries stand for the latest version of their series; a version
// entry equal to a series' latest is not listed twice — the series wins.
func buildLsRows(entries []lsEntry, wantInstalled, wantAll, wantLts, wantSeries bool) []lsRow {
	matches := func(version string, lts, installed bool) bool {
		return (wantInstalled && installed) ||
			wantAll ||
			(wantLts && lts)
	}

	var rows []lsRow

	if wantSeries {
		// collapse into series; each series row = its latest version
		type series struct {
			latest    string
			latestLT  bool
			available bool
			inst      bool
		}
		seriesMap := map[string]*series{}
		for _, e := range entries {
			name := seriesNameOf(e.Version, e.LTS)
			b := seriesMap[name]
			if b == nil {
				b = &series{}
				seriesMap[name] = b
			}
			if versionLess(b.latest, e.Version) {
				b.latest, b.latestLT, b.available, b.inst = e.Version, e.LTS, e.Available, e.Installed
			}
		}
		covered := map[string]bool{}
		for name, b := range seriesMap {
			rows = append(rows, lsRow{
				Label: name + " (" + b.latest + ")", SortVer: b.latest,
				LTS: b.latestLT, Status: statusOf(b.inst, b.available), Installed: b.inst,
			})
			covered[b.latest] = true
		}
		for _, e := range entries { // version entries, deduplicated
			if covered[e.Version] {
				continue
			}
			if matches(e.Version, e.LTS, e.Installed) {
				rows = append(rows, lsRow{
					Label: e.Version, SortVer: e.Version, LTS: e.LTS,
					Status: statusOf(e.Installed, e.Available), Installed: e.Installed,
				})
			}
		}
	} else {
		for _, e := range entries {
			if matches(e.Version, e.LTS, e.Installed) {
				rows = append(rows, lsRow{
					Label: e.Version, SortVer: e.Version, LTS: e.LTS,
					Status: statusOf(e.Installed, e.Available), Installed: e.Installed,
				})
			}
		}
	}

	// sort newest first
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if versionLess(rows[i].SortVer, rows[j].SortVer) {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	return rows
}

func runEngineLs() error {
	local, err := dist.ListLocal()
	if err != nil {
		return err
	}
	installedSet := map[string]bool{}
	for _, ref := range local {
		installedSet[ref.Engine+"@"+ref.Version] = true
	}

	ix, err := metadata.EnsureVersions("mysql", mirror)
	if err != nil {
		fmt.Fprintln(os.Stderr, "note: cannot fetch available versions:", err)
	}

	// merge index + local-only installs into the entry universe
	var entries []lsEntry
	seen := map[string]bool{}
	if ix != nil {
		for _, v := range ix.ListVersions() {
			vi := ix.Version(v)
			installed := installedSet["mysql@"+v]
			avail := false
			if vi != nil {
				_, serr := vi.Select(runtime.GOOS, runtime.GOARCH)
				avail = serr == nil
			}
			entries = append(entries, lsEntry{Version: v, LTS: vi.LTS, Installed: installed, Available: avail})
			seen[v] = true
		}
	}
	for _, ref := range local {
		if seen[ref.Version] {
			continue
		}
		entries = append(entries, lsEntry{Version: ref.Version, Installed: true, Available: true})
	}

	wantInstalled, wantAll, wantLts, wantSeries := engineLsFlags()
	rows := buildLsRows(entries, wantInstalled, wantAll, wantLts, wantSeries)

	headers := []string{"ENGINE", "VERSION", "LTS", "STATUS", "SIZE"}
	if engineLsPath {
		headers = append(headers, "PATH")
	}
	tw := newTable(os.Stdout, headers...)
	if len(rows) == 0 {
		fmt.Fprintln(os.Stdout, "no matching versions; run: dbpod engine install mysql@8.0.46")
		return nil
	}
	for _, r := range rows {
		status, size, lts, path := r.Status, "", "", ""
		if r.Installed {
			size = humanSize(dist.Size("mysql", r.SortVer))
			if engineLsPath {
				path = dist.Path("mysql", r.SortVer)
			}
		}
		if r.LTS {
			lts = "yes"
		}
		cells := []string{"mysql", r.Label, lts, status, size}
		if engineLsPath {
			cells = append(cells, path)
		}
		tw.row(cells...)
	}
	return tw.flush()
}

// seriesNameOf maps a version to its series: calendar-versioned non-LTS
// releases collapse into "innovation"; everything else is major.minor.
func seriesNameOf(version string, lts bool) string {
	if calendarVersion(version) && !lts {
		return "innovation"
	}
	return majorMinor(version)
}

// calendarVersion reports whether the version uses calendar versioning
// (major >= 10, e.g. 26.7.0) instead of the classic scheme (5.7 ... 9.7).
func calendarVersion(version string) bool {
	major, _, _ := strings.Cut(version, ".")
	n, err := strconv.Atoi(major)
	return err == nil && n >= 10
}

func majorMinor(version string) string {
	major, rest, _ := strings.Cut(version, ".")
	if rest == "" {
		return major
	}
	minor, _, _ := strings.Cut(rest, ".")
	return major + "." + minor
}

// versionLess compares dotted numeric versions; shorter strings sort first.
func versionLess(a, b string) bool {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		an, bn := numPart(as, i), numPart(bs, i)
		if an != bn {
			return an < bn
		}
	}
	return false
}

func numPart(parts []string, i int) int {
	if i >= len(parts) {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimRight(parts[i], "abcdefghijklmnopqrstuvwxyz"))
	if err != nil {
		return -1
	}
	return n
}

var engineInstallCmd = &cobra.Command{
	Use:   "install <engine>@<version>",
	Short: "Download and install an engine version (e.g. mysql@8.0.35)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ref, err := dist.ParseRef(args[0])
		if err != nil {
			return err
		}
		return dist.Install(ref, mirror, os.Stdout)
	},
}

var engineRmCmd = &cobra.Command{
	Use:     "rm <engine>@<version>...",
	Aliases: []string{"remove"},
	Short:   "Remove the cached binaries of engine versions",
	Args:    cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var refs []dist.PackageRef
		for _, arg := range args {
			ref, err := dist.ParseRef(arg)
			if err != nil {
				return err
			}
			refs = append(refs, ref)
		}
		// validate every target before touching anything: refuse while
		// instances still reference any of the engine versions
		if records, lerr := instance.List(); lerr == nil {
			var users []string
			for _, r := range records {
				for _, ref := range refs {
					if r.Engine == ref.Engine && r.Version == ref.Version {
						users = append(users, fmt.Sprintf("%s (uses %s)", r.Name, ref))
					}
				}
			}
			if len(users) > 0 {
				return fmt.Errorf("engine version(s) used by instance(s): %s — remove them first: dbpod rm <name>", strings.Join(users, ", "))
			}
		}
		var errs []error
		for _, ref := range refs {
			if err := dist.Remove(ref.Engine, ref.Version); err != nil {
				errs = append(errs, err)
				continue
			}
			fmt.Fprintf(os.Stdout, "removed %s\n", ref)
		}
		return errors.Join(errs...)
	},
}

// hiddenProxy builds a hidden docker-compatibility command that delegates to
// an engine subcommand, sharing its flags and argument validation.
func hiddenProxy(use string, target *cobra.Command) *cobra.Command {
	c := &cobra.Command{
		Use:    use,
		Short:  "Docker compatibility for: dbpod engine (" + target.Name() + ")",
		Hidden: true,
		RunE:   target.RunE,
		Args:   target.Args,
	}
	c.Flags().AddFlagSet(target.Flags())
	return c
}

func init() {
	engineLsCmd.Flags().BoolVar(&engineLsInstalled, "installed", false, "list installed versions")
	engineLsCmd.Flags().BoolVar(&engineLsAll, "all", false, "list all available versions")
	engineLsCmd.Flags().BoolVar(&engineLsLts, "lts", false, "list LTS versions")
	engineLsCmd.Flags().BoolVar(&engineLsSeries, "series", false, "collapse versions into release series (5.7, 8.0, innovation, ...)")
	engineLsCmd.Flags().BoolVar(&engineLsPath, "path", false, "show the installation path column")
	engineCmd.AddCommand(engineLsCmd, engineInstallCmd, engineRmCmd)
	rootCmd.AddCommand(engineCmd)

	// docker compatibility (hidden, but fully functional)
	rootCmd.AddCommand(
		hiddenProxy("images", engineLsCmd),
		hiddenProxy("rmi", engineRmCmd),
		hiddenProxy("pull", engineInstallCmd),
	)
}
