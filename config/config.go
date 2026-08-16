package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is the root of the configuration file.
type Config struct {
	Server    Server              `yaml:"server" json:"server"`
	Admin     *Admin              `yaml:"admin,omitempty" json:"admin,omitempty"`
	Providers []Provider          `yaml:"providers" json:"providers"`
	Routes    map[string][]string `yaml:"routes" json:"routes"` // model -> ordered provider names (fallback in order)
}

// Server holds the gateway's own listen/auth configuration.
type Server struct {
	Listen  string `yaml:"listen" json:"listen"`
	AuthKey string `yaml:"auth_key" json:"auth_key"`
}

// Admin holds the optional web admin panel configuration. The panel is only
// started when Listen is non-empty; login reuses server.auth_key.
type Admin struct {
	Listen string `yaml:"listen,omitempty" json:"listen,omitempty"`
}

// Provider is an upstream API backend. Keys is an ordered primary/fallback list (index 0 first).
type Provider struct {
	Name    string   `yaml:"name" json:"name"`
	BaseURL string   `yaml:"base_url" json:"base_url"`
	Type    string   `yaml:"type" json:"type"` // "openai" | "anthropic"
	Keys    []string `yaml:"keys" json:"keys"`
}

// Load reads and parses the YAML config at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(data)
}

// Parse parses and validates YAML config bytes.
func Parse(data []byte) (*Config, error) {
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Validate checks the config for required fields and cross-references.
func (c *Config) Validate() error {
	if c.Server.Listen == "" {
		return fmt.Errorf("server.listen is required")
	}
	if c.Server.AuthKey == "" {
		return fmt.Errorf("server.auth_key is required")
	}
	if len(c.Providers) == 0 {
		return fmt.Errorf("at least one provider is required")
	}
	names := make(map[string]bool, len(c.Providers))
	for i, p := range c.Providers {
		if p.Name == "" {
			return fmt.Errorf("provider[%d].name is required", i)
		}
		if names[p.Name] {
			return fmt.Errorf("duplicate provider name %q", p.Name)
		}
		names[p.Name] = true
		if p.BaseURL == "" {
			return fmt.Errorf("provider %q: base_url is required", p.Name)
		}
		if p.Type != "openai" && p.Type != "anthropic" {
			return fmt.Errorf("provider %q: type must be openai or anthropic, got %q", p.Name, p.Type)
		}
		if len(p.Keys) == 0 {
			return fmt.Errorf("provider %q: at least one key is required", p.Name)
		}
	}
	for model, pnames := range c.Routes {
		if len(pnames) == 0 {
			return fmt.Errorf("route %q: at least one provider is required", model)
		}
		for _, pname := range pnames {
			if !names[pname] {
				return fmt.Errorf("route %q -> unknown provider %q", model, pname)
			}
		}
	}
	return nil
}

// Marshal serializes the config back to canonical YAML.
func (c *Config) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(c); err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}
	return buf.Bytes(), nil
}

// Save atomically writes the config to path (temp file + rename) so a
// concurrent hot-reload never observes a half-written file.
func (c *Config) Save(path string) error {
	data, err := c.Marshal()
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-*.yaml.tmp")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("sync temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename config: %w", err)
	}
	return nil
}

// FindProvider returns the provider with the given name, and whether it was found.
func (c *Config) FindProvider(name string) (Provider, bool) {
	for _, p := range c.Providers {
		if p.Name == name {
			return p, true
		}
	}
	return Provider{}, false
}
