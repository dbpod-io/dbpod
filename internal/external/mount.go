package external

import (
	"fmt"
	"io"

	"github.com/dbpod-io/dbpod/internal/contract"
	"github.com/dbpod-io/dbpod/internal/engine"
	"github.com/dbpod-io/dbpod/internal/globalconfig"
	"github.com/dbpod-io/dbpod/internal/providers/mysql"
)

// mountedEngines records the manifest engines mounted in this process,
// so Validate can tell them apart from builtin engines.
var mountedEngines = map[string]bool{}

// Mount registers every config-declared engine from its manifest as one
// engine.Provider (family lifecycle + manifest versions/downloads). A
// broken manifest is reported to warn and skipped — it must not take the
// whole CLI down.
func Mount(manifests []globalconfig.EngineManifest, cfg *globalconfig.Config, warn io.Writer) {
	for _, m := range manifests {
		prov, err := buildProvider(m, cfg)
		if err != nil {
			fmt.Fprintf(warn, "note: engine %q skipped: %v\n", m.Name, err)
			continue
		}
		mountedEngines[m.Name] = true
		engine.Register(prov)
	}
}

// Validate checks a manifest against the mount rules (no builtin
// takeover, valid profile) without registering anything.
func Validate(m globalconfig.EngineManifest, cfg *globalconfig.Config) error {
	if _, err := engine.Get(m.Name); err == nil && !mountedEngines[m.Name] {
		return fmt.Errorf("builtin engine takes precedence")
	}
	_, err := buildProvider(m, cfg)
	return err
}

// buildProvider instantiates the provider of a manifest. Every
// config-declared engine uses the profile-driven family machinery
// (internal/providers/mysql): the manifest fully describes the lifecycle,
// so adding an engine never changes the main project.
func buildProvider(m globalconfig.EngineManifest, cfg *globalconfig.Config) (engine.Provider, error) {
	if _, err := engine.Get(m.Name); err == nil && !mountedEngines[m.Name] {
		return nil, fmt.Errorf("builtin engine takes precedence")
	}
	prof := m.EngineProfile
	prof.Engine = m.Name
	prof.ContractVersion = contract.Version
	if err := prof.Validate(); err != nil {
		return nil, fmt.Errorf("invalid profile: %w", err)
	}
	return &Provider{
		Engine:   mysql.New(prof),
		manifest: &m,
		base:     userSourceBase(m.Name, cfg),
	}, nil
}

// userSourceBase resolves the file base of the engine's user-layer
// settings (config.yaml providers.<name>): the default source's base, or
// the lone source's when no default is named.
func userSourceBase(engine string, cfg *globalconfig.Config) string {
	if cfg == nil {
		return ""
	}
	pc, ok := cfg.Providers[engine]
	if !ok {
		return ""
	}
	return sourceBase(pc.DefaultSource, pc.Sources)
}

// sourceBase resolves the file base of the configured default source; a
// lone source applies even without an explicit default_source.
func sourceBase(defaultSource string, sources map[string]globalconfig.Source) string {
	if s, ok := sources[defaultSource]; ok {
		return s.Base
	}
	if defaultSource == "" && len(sources) == 1 {
		for _, s := range sources {
			return s.Base
		}
	}
	return ""
}
