package mllmcli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config represents the CLI configuration.
type Config struct {
	CurrentContext string             `yaml:"currentContext"`
	Contexts       map[string]Context `yaml:"contexts"`
	Metadata       map[string]string  `yaml:"metadata,omitempty"`
}

// Context holds connection settings for an environment.
type Context struct {
	Name      string `yaml:"name"`
	Server    string `yaml:"server"`
	Token     string `yaml:"token"`
	Namespace string `yaml:"namespace"`
}

func LoadConfig(path string) (*Config, error) {
	cfg := &Config{
		Contexts: map[string]Context{},
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return nil, err
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	if cfg.Contexts == nil {
		cfg.Contexts = map[string]Context{}
	}
	return cfg, nil
}

func SaveConfig(cfg *Config, path string) error {
	if cfg.Contexts == nil {
		cfg.Contexts = map[string]Context{}
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func defaultConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "./mllm-config.yaml"
	}
	return filepath.Join(dir, "mllm", "config.yaml")
}

func setContext(cfg *Config, ctx Context, makeCurrent bool) {
	if cfg.Contexts == nil {
		cfg.Contexts = map[string]Context{}
	}
	cfg.Contexts[ctx.Name] = ctx
	if cfg.CurrentContext == "" || makeCurrent {
		cfg.CurrentContext = ctx.Name
	}
}

func ensureContextExists(cfg *Config, name string) error {
	if _, ok := cfg.Contexts[name]; !ok {
		return fmt.Errorf("context %q not found", name)
	}
	return nil
}
