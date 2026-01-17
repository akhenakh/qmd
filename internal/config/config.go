package config

type Collection struct {
	Name    string            `json:"name"`
	Path    string            `json:"path"`
	Pattern string            `json:"pattern"`
	Exclude []string          `json:"exclude"`
	Context map[string]string `json:"context"`
}

type Config struct {
	// LLM / Embedding Settings
	OllamaURL       string `json:"ollama_url"`
	ModelName       string `json:"model_name"`
	EmbedDimensions int    `json:"embed_dimensions"`

	// Local Inference Settings
	UseLocal       bool   `json:"use_local"`
	LocalModelPath string `json:"local_model_path"`
	LocalLibPath   string `json:"local_lib_path"`

	// Chunking Settings
	ChunkSize    int `json:"chunk_size"`
	ChunkOverlap int `json:"chunk_overlap"`

	// State
	EmbeddingsConfigured bool `json:"embeddings_configured"`

	// Data
	Collections []Collection `json:"collections"`
}

// Default settings
func Default() *Config {
	return &Config{
		OllamaURL:            "http://localhost:11434",
		ModelName:            "nomic-embed-text",
		EmbedDimensions:      768,
		ChunkSize:            1000,
		ChunkOverlap:         200,
		Collections:          make([]Collection, 0),
		UseLocal:             false,
		EmbeddingsConfigured: false,
	}
}
