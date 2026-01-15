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
	// LLM / Embedding Settings
	OllamaURL       string `yaml:"ollama_url"`
	ModelName       string `yaml:"model_name"`
	EmbedDimensions int    `yaml:"embed_dimensions"`

	// Local Inference Settings
	UseLocal       bool   `yaml:"use_local"`
	LocalModelPath string `yaml:"local_model_path"`
	LocalLibPath   string `yaml:"local_lib_path"`

	// Chunking Settings
	ChunkSize    int `yaml:"chunk_size"`
	ChunkOverlap int `yaml:"chunk_overlap"`

	// Data
	Collections map[string]Collection `yaml:"collections"`
}

// Default settings
func Default() *Config {
	return &Config{
		OllamaURL:       "http://localhost:11434",
		ModelName:       "nomic-embed-text",
		EmbedDimensions: 768,
		ChunkSize:       1000,
		ChunkOverlap:    200,
		Collections:     make(map[string]Collection),
	}
}

func GetConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "qmd.yml"), nil
}

func Load() (*Config, error) {
	path, err := GetConfigPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Default(), nil
	}
	if err != nil {
		return nil, err
	}

	cfg := Default()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

func Save(cfg *Config) error {
	path, err := GetConfigPath()
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
