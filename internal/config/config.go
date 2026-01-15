package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Collection struct {
	Path    string            `yaml:"path"`
	Pattern string            `yaml:"pattern"`
	Context map[string]string `yaml:"context"`
}

type Config struct {
	GlobalContext string                `yaml:"global_context"`
	Collections   map[string]Collection `yaml:"collections"`
}

func GetConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "qmd"), nil
}

func Load() (*Config, error) {
	dir, err := GetConfigDir()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(filepath.Join(dir, "index.yml"))
	if os.IsNotExist(err) {
		return &Config{Collections: make(map[string]Collection)}, nil
	}
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.Collections == nil {
		cfg.Collections = make(map[string]Collection)
	}
	return &cfg, nil
}

func Save(cfg *Config) error {
	dir, err := GetConfigDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "index.yml"), data, 0644)
}
