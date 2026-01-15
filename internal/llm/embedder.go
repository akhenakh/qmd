package llm

// Embedder defines the interface for generating embeddings
type Embedder interface {
	Embed(text string, isQuery bool) ([]float32, error)
	Close() error
}
