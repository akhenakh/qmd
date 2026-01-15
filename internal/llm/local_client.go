package llm

import (
	"fmt"
	"math"
	"os"

	"github.com/hybridgroup/yzma/pkg/llama"
)

type LocalClient struct {
	ModelFile string
	LibPath   string
	Context   llama.Context
	Model     llama.Model
}

func NewLocalClient(modelFile, libPath string) (*LocalClient, error) {
	if _, err := os.Stat(modelFile); os.IsNotExist(err) {
		return nil, fmt.Errorf("model file not found: %s", modelFile)
	}

	// Load the shared library (llama.cpp)
	if err := llama.Load(libPath); err != nil {
		return nil, fmt.Errorf("unable to load llama library from %s: %w", libPath, err)
	}

	// Initialize backend
	llama.Init()

	// Load Model
	model, err := llama.ModelLoadFromFile(modelFile, llama.ModelDefaultParams())
	if err != nil {
		return nil, fmt.Errorf("unable to load model: %v", err)
	}
	// Note: yzma uses typed handles, but checking for 0 is still valid for uintptr-based types if they are defined as such.
	// However, the provided error check above is usually sufficient.

	// Initialize Context
	ctxParams := llama.ContextDefaultParams()
	ctxParams.NCtx = 4096 // Default context window
	// Enable embeddings - Embeddings field is uint8 (bool)
	ctxParams.Embeddings = 1
	ctxParams.PoolingType = llama.PoolingTypeMean

	lctx, err := llama.InitFromModel(model, ctxParams)
	if err != nil {
		llama.ModelFree(model)
		return nil, fmt.Errorf("unable to initialize context: %v", err)
	}

	return &LocalClient{
		ModelFile: modelFile,
		LibPath:   libPath,
		Model:     model,
		Context:   lctx,
	}, nil
}

func (c *LocalClient) Embed(text string, isQuery bool) ([]float32, error) {
	// Nomic formatting
	prefix := "search_document: "
	if isQuery {
		prefix = "search_query: "
	}
	prompt := prefix + text

	vocab := llama.ModelGetVocab(c.Model)

	// Tokenize (true for add_bos, true for special tokens)
	tokens := llama.Tokenize(vocab, prompt, true, true)

	// Create batch
	batch := llama.BatchGetOne(tokens)

	// Decode
	// yzma.Decode returns (int32, error) based on the provided definition
	ret, err := llama.Decode(c.Context, batch)
	if err != nil {
		return nil, fmt.Errorf("llama_decode failed: %w", err)
	}
	if ret != 0 {
		return nil, fmt.Errorf("llama_decode failed with code %d", ret)
	}

	// Get Embeddings
	nEmbd := llama.ModelNEmbd(c.Model)
	// For pooling type Mean, we usually look at the sequence.
	// GetEmbeddingsSeq returns the embedding for the sequence 0.
	vec, err := llama.GetEmbeddingsSeq(c.Context, 0, nEmbd)
	if err != nil {
		return nil, fmt.Errorf("failed to get embeddings: %w", err)
	}

	// Normalize (Cosine Similarity requires normalized vectors)
	var sum float64
	for _, v := range vec {
		sum += float64(v * v)
	}
	sum = math.Sqrt(sum)
	norm := float32(1.0 / sum)

	normalized := make([]float32, len(vec))
	for i, v := range vec {
		normalized[i] = v * norm
	}

	return normalized, nil
}

func (c *LocalClient) Close() error {
	// Assuming llama.Free takes Context and llama.ModelFree takes Model
	if c.Context != 0 {
		llama.Free(c.Context)
	}
	if c.Model != 0 {
		llama.ModelFree(c.Model)
	}
	return nil
}
