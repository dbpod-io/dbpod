package external

import (
	"fmt"
	"io"

	"github.com/dbpod-io/dbpod/internal/contract"
	"github.com/dbpod-io/dbpod/internal/dist"
	"github.com/dbpod-io/dbpod/internal/engine"
	"github.com/dbpod-io/dbpod/internal/engine/mysql"
	"github.com/dbpod-io/dbpod/internal/globalconfig"
)

// families maps a declared family to its profile factory. MySQL-family
// engines (MariaDB, Percona ...) reuse the mysql lifecycle machinery.
var families = map[string]func(contract.EngineProfile) (engine.Engine, error){
	"mysql": func(p contract.EngineProfile) (engine.Engine, error) { return mysql.New(p), nil },
}

// mountedEngines records the manifest engines mounted in this process,
// so Validate can tell them apart from built-in engines.
var mountedEngines = map[string]bool{}

// Mount registers every config-declared engine from its manifest. User
// settings (mirrors, sources) come from the user layer — the
// providers.<name> block of the global config. A broken manifest is
// reported to warn and skipped — it must not take the whole CLI down.
func Mount(manifests []globalconfig.EngineManifest, cfg *globalconfig.Config, warn io.Writer) {
	for _, m := range manifests {
		eng, prov, err := buildEngine(m, cfg)
		if err != nil {
			fmt.Fprintf(warn, "note: engine %q skipped: %v\n", m.Name, err)
			continue
		}
		mountedEngines[m.Name] = true
		engine.Register(eng)
		dist.RegisterProvider(prov)
	}
}

// Validate checks a manifest against the mount rules (known family,
// valid profile) without registering anything.
func Validate(m globalconfig.EngineManifest) error {
	if _, err := dist.ProviderFor(m.Name); err == nil && !mountedEngines[m.Name] {
		return fmt.Errorf("built-in engine takes precedence")
	}
	if _, ok := families[m.Family]; !ok {
		return fmt.Errorf("unknown family %q (supported: mysql)", m.Family)
	}
	prof := m.EngineProfile
	prof.Engine = m.Name
	prof.ContractVersion = contract.Version
	return prof.Validate()
}

// buildEngine instantiates the engine and provider of a manifest.
func buildEngine(m globalconfig.EngineManifest, cfg *globalconfig.Config) (engine.Engine, dist.Provider, error) {
	if _, err := dist.ProviderFor(m.Name); err == nil {
		return nil, nil, fmt.Errorf("built-in engine takes precedence")
	}
	build, ok := families[m.Family]
	if !ok {
		return nil, nil, fmt.Errorf("unknown family %q (supported: mysql)", m.Family)
	}
	prof := m.EngineProfile
	prof.Engine = m.Name
	prof.ContractVersion = contract.Version
	if err := prof.Validate(); err != nil {
		return nil, nil, fmt.Errorf("invalid profile: %w", err)
	}
	eng, err := build(prof)
	if err != nil {
		return nil, nil, err
	}
	prov := &Provider{
		manifest: &m,
		base:     userSourceBase(m.Name, cfg),
	}
	return eng, prov, nil
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
