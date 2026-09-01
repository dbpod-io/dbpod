package cmd

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/shapled/dbpod/internal/dist"
	"github.com/shapled/dbpod/internal/metadata"
	"github.com/spf13/cobra"
)

var (
	engineLsAll       bool
	engineLsInstalled bool
	engineLsLts       bool
	engineLsBranch    bool
	engineLsPath      bool
)

var engineCmd = &cobra.Command{
	Use:   "engine",
	Short: "Manage database engine binaries",
}

var engineLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List engine versions (default: --installed --branch; flags are filters combined as a union)",
	Long: `List engine versions.

All flags are FILTERS combined as a UNION: an entry is listed when it matches
ANY given flag. With no flag the default is --installed --branch.

Entry types:
  version  a single engine version, matched by --installed, --all, --lts
  branch   stands for the latest version of a release branch, matched by
           --branch and displayed as "<branch> (<latest>)". Calendar-versioned
           non-LTS releases collapse into a single "innovation" branch;
           LTS calendar releases keep their own branch.

The union is deduplicated: when a version equals a branch's latest, only the
branch entry is shown.

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
}

// lsRow is one computed output row of engine ls.
type lsRow struct {
	Label     string // version or "branch (latest)"
	SortVer   string // underlying version: ordering, status and size key
	LTS       bool
	Installed bool
}

// engineLsFlags resolves the effective filter set: with no flag given the
// default is --installed --branch.
func engineLsFlags() (wantInstalled, wantAll, wantLts, wantBranch bool) {
	anyFlag := engineLsAll || engineLsInstalled || engineLsLts || engineLsBranch
	return engineLsInstalled || !anyFlag,
		engineLsAll,
		engineLsLts,
		engineLsBranch || !anyFlag
}

// buildLsRows applies the (unioned, deduplicated) filter logic of engine ls.
// Branch entries stand for the latest version of their branch; a version
// entry equal to a branch's latest is not listed twice — the branch wins.
func buildLsRows(entries []lsEntry, wantInstalled, wantAll, wantLts, wantBranch bool) []lsRow {
	matches := func(version string, lts, installed bool) bool {
		return (wantInstalled && installed) ||
			wantAll ||
			(wantLts && lts)
	}

	var rows []lsRow

	if wantBranch {
		// collapse into branches; each branch row = its latest version
		type branch struct {
			latest   string
			latestLT bool
			inst     bool
		}
		branches := map[string]*branch{}
		for _, e := range entries {
			name := branchNameOf(e.Version, e.LTS)
			b := branches[name]
			if b == nil {
				b = &branch{}
				branches[name] = b
			}
			if versionLess(b.latest, e.Version) {
				b.latest, b.latestLT, b.inst = e.Version, e.LTS, e.Installed
			}
		}
		covered := map[string]bool{}
		for name, b := range branches {
			rows = append(rows, lsRow{
				Label: name + " (" + b.latest + ")", SortVer: b.latest,
				LTS: b.latestLT, Installed: b.inst,
			})
			covered[b.latest] = true
		}
		for _, e := range entries { // version entries, deduplicated
			if covered[e.Version] {
				continue
			}
			if matches(e.Version, e.LTS, e.Installed) {
				rows = append(rows, lsRow{Label: e.Version, SortVer: e.Version, LTS: e.LTS, Installed: e.Installed})
			}
		}
	} else {
		for _, e := range entries {
			if matches(e.Version, e.LTS, e.Installed) {
				rows = append(rows, lsRow{Label: e.Version, SortVer: e.Version, LTS: e.LTS, Installed: e.Installed})
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
			entries = append(entries, lsEntry{Version: v, LTS: vi.LTS, Installed: installedSet["mysql@"+v]})
			seen[v] = true
		}
	}
	for _, ref := range local {
		if seen[ref.Version] {
			continue
		}
		entries = append(entries, lsEntry{Version: ref.Version, Installed: true})
	}

	wantInstalled, wantAll, wantLts, wantBranch := engineLsFlags()
	rows := buildLsRows(entries, wantInstalled, wantAll, wantLts, wantBranch)

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
		status, size, lts, path := "", "", "", ""
		if r.Installed {
			status = "installed"
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

// branchNameOf maps a version to its branch: calendar-versioned non-LTS
// releases collapse into "innovation"; everything else is major.minor.
func branchNameOf(version string, lts bool) string {
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
	Use:     "rm <engine>@<version>",
	Aliases: []string{"remove"},
	Short:   "Remove the cached binary of an engine version",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ref, err := dist.ParseRef(args[0])
		if err != nil {
			return err
		}
		return dist.Remove(ref.Engine, ref.Version)
	},
}

func init() {
	engineCmd.GroupID = "global"
	engineLsCmd.Flags().BoolVar(&engineLsInstalled, "installed", false, "list installed versions")
	engineLsCmd.Flags().BoolVar(&engineLsAll, "all", false, "list all available versions")
	engineLsCmd.Flags().BoolVar(&engineLsLts, "lts", false, "list LTS versions")
	engineLsCmd.Flags().BoolVar(&engineLsBranch, "branch", false, "collapse versions into branches")
	engineLsCmd.Flags().BoolVar(&engineLsPath, "path", false, "show the installation path column")
	engineCmd.AddCommand(engineLsCmd, engineInstallCmd, engineRmCmd)
	rootCmd.AddCommand(engineCmd)
}
