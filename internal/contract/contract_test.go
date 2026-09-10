package contract

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/dbpod-io/dbpod/internal/engine"
	"github.com/dbpod-io/dbpod/internal/metadata"
)

// The fixtures pin the wire shapes helpers must emit: they decode into the
// core models exactly as an external helper's output would.
func TestFixturesDecode(t *testing.T) {
	t.Run("versions", func(t *testing.T) {
		var ix metadata.Index
		load(t, "testdata/versions.json", &ix)
		if ix.Engine != "mariadb" {
			t.Errorf("engine = %q", ix.Engine)
		}
		v := ix.Version("11.4.5")
		if v == nil || v.Series != "11.4" {
			t.Fatalf("version 11.4.5 = %+v", v)
		}
	})
	t.Run("resolve-download", func(t *testing.T) {
		var plan engine.DownloadPlan
		load(t, "testdata/plan.json", &plan)
		if plan.Main.Kind != "tar.gz" || plan.Main.URL == "" {
			t.Errorf("plan = %+v", plan)
		}
	})
	t.Run("profile", func(t *testing.T) {
		var p EngineProfile
		load(t, "testdata/profile.json", &p)
		if err := p.Validate(); err != nil {
			t.Errorf("fixture profile invalid: %v", err)
		}
	})
}

func load(t *testing.T, path string, into any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, into); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

func TestValidateRejects(t *testing.T) {
	valid := func() *EngineProfile {
		var p EngineProfile
		data, _ := os.ReadFile("testdata/profile.json")
		json.Unmarshal(data, &p)
		return &p
	}()

	cases := map[string]func(*EngineProfile){
		"contract version": func(p *EngineProfile) { p.ContractVersion = 99 },
		"empty engine":     func(p *EngineProfile) { p.Engine = "" },
		"empty init":       func(p *EngineProfile) { p.Init = " " },
		"binary path sep":  func(p *EngineProfile) { p.Start = "../evil/mariadbd --x" },
		"abs path literal": func(p *EngineProfile) { p.Shutdown = "mariadb-admin -S /etc/nope shutdown" },
		"empty config":     func(p *EngineProfile) { p.Config = "" },
	}
	for name, mutate := range cases {
		p := *valid // shallow copy; mutations below replace fields wholesale
		mutate(&p)
		if err := p.Validate(); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
}
