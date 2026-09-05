// Package globalconfig loads the dbpod-wide configuration file
// ($DBPOD_HOME/config.yaml): proxy settings, named sources and their
// credentials.
package globalconfig

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/dbpod-io/dbpod/internal/fetch"
	"github.com/dbpod-io/dbpod/internal/project"
	"gopkg.in/yaml.v3"
)

// Config is the global configuration document.
type Config struct {
	// Proxy is the global default, applied to every provider that does not
	// declare its own override.
	Proxy ProxyConfig `yaml:"proxy"`

	// Providers holds per-engine-plugin settings: sources (with inline
	// auth) and an optional proxy override.
	Providers map[string]ProviderConfig `yaml:"providers"`
}

// ProxyConfig is the transport-level proxy configuration.
type ProxyConfig struct {
	HTTP    string   `yaml:"http"`
	Socks5  string   `yaml:"socks5"`
	NoProxy []string `yaml:"no_proxy"`
}

// ProviderConfig is the per-engine-plugin configuration block.
type ProviderConfig struct {
	DefaultSource string            `yaml:"default_source,omitempty"`
	Sources       map[string]Source `yaml:"sources,omitempty"`
	Proxy         *ProxyConfig      `yaml:"proxy,omitempty"`
}

// Source is a named data source: metadata/base URLs plus inline
// authentication (secrets preferably via env references).
type Source struct {
	Index string     `yaml:"index"`
	Base  string     `yaml:"base"`
	Auth  SourceAuth `yaml:"auth,omitempty"`
}

// SourceAuth is the inline authentication of a source.
type SourceAuth struct {
	User        string `yaml:"user,omitempty"`
	PasswordEnv string `yaml:"password_env,omitempty"`
	Password    string `yaml:"password,omitempty"`
}

// Path returns the global config file location.
func Path() (string, error) {
	h, err := project.HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, "config.yaml"), nil
}

// Load reads the global config; a missing file yields the zero Config.
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	cfg := &Config{}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// Apply pushes the loaded settings into the fetch layer: the global
// default proxy, plus per-provider clients for providers that declare
// their own proxy override.
func (c *Config) Apply() {
	fetch.SetProxy(fetch.Proxy{
		HTTP:    c.Proxy.HTTP,
		Socks5:  c.Proxy.Socks5,
		NoProxy: c.Proxy.NoProxy,
	})
	for name, pc := range c.Providers {
		if pc.Proxy != nil && (pc.Proxy.HTTP != "" || pc.Proxy.Socks5 != "") {
			fetch.RegisterClient(name, fetch.NewClient(fetch.Proxy{
				HTTP:   pc.Proxy.HTTP,
				Socks5: pc.Proxy.Socks5,
			}))
		}
	}
}
