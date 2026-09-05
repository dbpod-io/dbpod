package dist

import (
	"fmt"
	"github.com/shapled/dbpod/internal/metadata"
	"os"
	"sort"
)

// SourceSpec describes the data source of an engine: the metadata/index
// location, the package base URL and authentication material.
type SourceSpec struct {
	Index string   // metadata/index URL ("" = engine's built-in default)
	Base  string   // package base URL ("" = engine's official default)
	Auth  AuthSpec // credentials for network fetchers
}

// AuthSpec carries source credentials. Secrets are preferably referenced
// via environment variables (PasswordEnv) instead of stored values.
type AuthSpec struct {
	User        string `yaml:"user,omitempty"`
	PasswordEnv string `yaml:"password_env,omitempty"`
	Password    string `yaml:"password,omitempty"`
}

// Secret resolves the password (env reference wins over direct value).
func (a AuthSpec) Secret() string {
	if a.PasswordEnv != "" {
		if v, ok := os.LookupEnv(a.PasswordEnv); ok {
			return v
		}
	}
	return a.Password
}

// Provider is a per-engine distribution provider: the version list comes
// from the provider's source and each version resolves to an installable
// package for the current platform.
type Provider interface {
	Engine() string

	// SeriesOf returns the series a version belongs to (see engine impls;
	// the newest calendar release represents "innovation").
	SeriesOf(version string, lts, isLatest bool) []string

	// EnsureVersions returns the version index, refreshing when stale.
	EnsureVersions() (*metadata.Index, error)

	// ResolveVersion maps a possibly-series version ("8.0") to a full
	// version ("8.0.46"), preferring locally installed matches.
	ResolveVersion(version, mirror string) (string, error)

	// ResolveDownload returns the package of version for the platform:
	// main archive plus companion dependency archives.
	ResolveDownload(version, goos, goarch string) (DownloadPlan, error)
}

// DownloadPlan is everything needed to fetch an engine distribution.
type DownloadPlan struct {
	Version string
	Main    DownloadFile
	Deps    []DownloadFile
}

// DownloadFile is one archive to download (with checksum when published).
type DownloadFile struct {
	URL          string
	FallbackURL  string      `json:"fallback_url,omitempty"`
	SHA256       string      `json:"sha256,omitempty"`
	MD5          string      `json:"md5,omitempty"`
	Size         int64       `json:"size,omitempty"`
	Kind         string      `json:"kind"` // tar.gz | tar.xz | zip | deb | rpm
	RootDir      string      `json:"root_dir,omitempty"`
	ExtractRules [][2]string `json:"extract_rules,omitempty"`
}

var providers = map[string]Provider{}

// RegisterProvider adds a provider for its engine.
func RegisterProvider(p Provider) { providers[p.Engine()] = p }

// ProviderFor returns the provider of the named engine.
func ProviderFor(engine string) (Provider, error) {
	p, ok := providers[engine]
	if !ok {
		return nil, fmt.Errorf("unknown engine %q", engine)
	}
	return p, nil
}

// Providers returns all registered providers sorted by engine name.
func Providers() []Provider {
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]Provider, 0, len(names))
	for _, n := range names {
		out = append(out, providers[n])
	}
	return out
}

// ResolveVersion resolves a version for the named engine (series forms
// allowed).
func ResolveVersion(engine, version, mirror string) (string, error) {
	p, err := ProviderFor(engine)
	if err != nil {
		return "", err
	}
	return p.ResolveVersion(version, mirror)
}

var _ = metadata.Package{}
