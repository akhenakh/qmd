package llm

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/akhenakh/qmd/internal/util"
	"github.com/hybridgroup/yzma/pkg/llama"
)

type LocalClient struct {
	ModelFile string
	LibPath   string
	Context   llama.Context
	Model     llama.Model
	UseEncode bool
	MaxTokens int
	TargetDim int
}

func NewLocalClient(modelFile, libPath string, targetDim int) (*LocalClient, error) {
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

	// Determine if we should use Encode (BERT/Nomic) or Decode (Llama)
	useEncode := false

	// Fetch architecture metadata
	val, ok := llama.ModelMetaValStr(model, "general.architecture")
	if ok {
		if strings.Contains(val, "bert") {
			useEncode = true
		}
	} else {
		// Fallback: checks filename if metadata lookup fails
		lowerName := strings.ToLower(modelFile)
		if strings.Contains(lowerName, "bert") || strings.Contains(lowerName, "nomic-embed") {
			useEncode = true
		}
	}

	// Determine max context length to configure batch sizes correctly
	// Nomic Embed v1.5 typically has 2048 context
	maxTokens := 2048

	// Try to read context length from metadata
	if useEncode {
		if sVal, ok := llama.ModelMetaValStr(model, "nomic-bert.context_length"); ok {
			if v, err := strconv.Atoi(sVal); err == nil && v > 0 {
				maxTokens = v
			}
		}
	} else {
		if sVal, ok := llama.ModelMetaValStr(model, "llama.context_length"); ok {
			if v, err := strconv.Atoi(sVal); err == nil && v > 0 {
				maxTokens = v
			}
		} else if sVal, ok := llama.ModelMetaValStr(model, "general.context_length"); ok {
			if v, err := strconv.Atoi(sVal); err == nil && v > 0 {
				maxTokens = v
			}
		}
	}

	// Initialize Context with batch sizes matching the context limit
	// This prevents "encoder requires n_ubatch >= n_tokens" assertion failures
	ctxParams := llama.ContextDefaultParams()
	ctxParams.NCtx = uint32(maxTokens)
	ctxParams.NBatch = uint32(maxTokens)
	ctxParams.NUbatch = uint32(maxTokens)
	ctxParams.Embeddings = 1
	ctxParams.PoolingType = llama.PoolingTypeMean

	lctx, err := llama.InitFromModel(model, ctxParams)
	if err != nil {
		llama.ModelFree(model)
		return nil, fmt.Errorf("unable to initialize context: %v", err)
	}

	util.Debug("LLM [Local] Initialized. Model: %s, UseEncode: %v, MaxTokens: %d", modelFile, useEncode, maxTokens)

	return &LocalClient{
		ModelFile: modelFile,
		LibPath:   libPath,
		Model:     model,
		Context:   lctx,
		UseEncode: useEncode,
		MaxTokens: maxTokens,
		TargetDim: targetDim,
	}, nil
}

func (c *LocalClient) Embed(text string, isQuery bool) ([]float32, error) {
	// Only apply Nomic formatting if the model appears to be Nomic.
	// Qwen and others typically expect raw text or different templates.
	prompt := text
	if strings.Contains(strings.ToLower(c.ModelFile), "nomic") {
		prefix := "search_document: "
		if isQuery {
			prefix = "search_query: "
		}
		prompt = prefix + text
	}

	util.Debug("LLM [Local] Embed Request. IsQuery: %v, TextLen: %d", isQuery, len(text))

	vocab := llama.ModelGetVocab(c.Model)

	// Tokenize (true for add_bos, true for special tokens)
	tokens := llama.Tokenize(vocab, prompt, true, true)

	// SAFETY: Truncate tokens to MaxTokens to prevent llama.cpp assertion crash.
	// The assertion `GGML_ASSERT(n_ubatch >= n_tokens)` fails if input is too long.
	if len(tokens) > c.MaxTokens {
		util.Debug("LLM [Local] Truncating tokens from %d to %d", len(tokens), c.MaxTokens)
		tokens = tokens[:c.MaxTokens]
	}

	// Create batch
	batch := llama.BatchGetOne(tokens)

	// Clear the KV cache. This is crucial for embeddings generation using Decode (decoder-only models like Qwen/Llama).
	// Without clearing, the context fills up with tokens from previous documents, causing "failed to find a memory slot" errors.
	mem, _ := llama.GetMemory(c.Context)
	llama.MemoryClear(mem, true)

	var ret int32
	var err error

	// Use appropriate processing function based on architecture
	if c.UseEncode {
		ret, err = llama.Encode(c.Context, batch)
	} else {
		ret, err = llama.Decode(c.Context, batch)
	}

	if err != nil {
		util.Debug("LLM [Local] Error processing: %v", err)
		return nil, fmt.Errorf("llama processing failed: %w", err)
	}
	if ret != 0 {
		util.Debug("LLM [Local] Error processing code: %d", ret)
		return nil, fmt.Errorf("llama processing failed with code %d", ret)
	}

	// Get Embeddings
	nEmbd := llama.ModelNEmbd(c.Model)
	// For pooling type Mean, we usually look at the sequence.
	// GetEmbeddingsSeq returns the embedding for the sequence 0.
	vec, err := llama.GetEmbeddingsSeq(c.Context, 0, nEmbd)
	if err != nil {
		return nil, fmt.Errorf("failed to get embeddings: %w", err)
	}

	// Handle Matryoshka Truncation (if target dim is smaller than model output)
	if c.TargetDim > 0 && len(vec) > c.TargetDim {
		util.Debug("LLM [Local] Truncating vector from %d to %d", len(vec), c.TargetDim)
		vec = vec[:c.TargetDim]
	}

	// Normalize (Cosine Similarity requires normalized vectors)
	var sum float64
	for _, v := range vec {
		sum += float64(v * v)
	}
	sum = math.Sqrt(sum)
	if sum == 0 {
		return vec, nil
	}
	norm := float32(1.0 / sum)

	normalized := make([]float32, len(vec))
	for i, v := range vec {
		normalized[i] = v * norm
	}

	util.Debug("LLM [Local] Generated vector. Dim: %d", len(normalized))
	return normalized, nil
}

func (c *LocalClient) Close() error {
	if c.Context != 0 {
		llama.Free(c.Context)
	}
	if c.Model != 0 {
		llama.ModelFree(c.Model)
	}
	return nil
}
