// Package config reads and writes the declarative dbpod.yaml.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// FileName is the declarative config file at the project root.
const FileName = "dbpod.yaml"

// Config mirrors dbpod.yaml:
//
//	engine: mysql
//	version: "8.0.35"
//	port: 3306
//	init-sql:
//	  - ./scripts/schema.sql
//	  - ./scripts/seed.sql
type Config struct {
	Name    string   `yaml:"name,omitempty"` // project/data env name; defaults to dir name
	Engine  string   `yaml:"engine"`         // e.g. mysql
	Version string   `yaml:"version"`        // full version or series
	Port    int      `yaml:"port"`           // server port
	InitSQL []string `yaml:"init-sql"`       // SQL files run on first initialization
}

// Dir returns the directory containing dbpod.yaml, searching upward from cwd.
// Returns "" when not found.
func Dir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, FileName)); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

// Load reads dbpod.yaml from dir.
func Load(dir string) (*Config, error) {
	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", FileName, err)
	}
	if c.Engine == "" {
		return nil, fmt.Errorf("%s: engine is required", FileName)
	}
	if c.Version == "" {
		return nil, fmt.Errorf("%s: version is required", FileName)
	}
	if c.Port == 0 {
		c.Port = 3306
	}
	return &c, nil
}

// Save writes dbpod.yaml into dir.
func Save(dir string, c *Config) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	header := "# dbpod declarative database environment\n# docs: https://github.com/shapled/dbpod\n"
	return os.WriteFile(filepath.Join(dir, FileName), append([]byte(header), data...), 0o644)
}

// ResolveInitSQL resolves init-sql entries relative to dir and checks existence.
func (c *Config) ResolveInitSQL(dir string) ([]string, error) {
	var out []string
	for _, rel := range c.InitSQL {
		abs := rel
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(dir, rel)
		}
		if _, err := os.Stat(abs); err != nil {
			return nil, fmt.Errorf("init-sql %q not found: %w", rel, err)
		}
		out = append(out, abs)
	}
	return out, nil
}
