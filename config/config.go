package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the root of the configuration file.
type Config struct {
	Server    Server            `yaml:"server"`
	Providers []Provider        `yaml:"providers"`
	Routes    map[string][]string `yaml:"routes"` // model -> ordered provider names (fallback in order)
}

// Server holds the gateway's own listen/auth configuration.
type Server struct {
	Listen  string `yaml:"listen"`
	AuthKey string `yaml:"auth_key"`
}

// Provider is an upstream API backend. Keys is an ordered primary/fallback list (index 0 first).
type Provider struct {
	Name    string   `yaml:"name"`
	BaseURL string   `yaml:"base_url"`
	Type    string   `yaml:"type"` // "openai" | "anthropic"
	Keys    []string `yaml:"keys"`
}

// Load reads and parses the YAML config at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) validate() error {
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

// FindProvider returns the provider with the given name, and whether it was found.
func (c *Config) FindProvider(name string) (Provider, bool) {
	for _, p := range c.Providers {
		if p.Name == name {
			return p, true
		}
	}
	return Provider{}, false
}