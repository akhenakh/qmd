package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"github.com/akhenakh/qmd/internal/util"
)

type HTTPClient struct {
	BaseURL    string
	Model      string
	TargetDim  int
	HTTPClient *http.Client
}

func NewHTTPClient(baseURL, model string, targetDim int) *HTTPClient {
	return &HTTPClient{
		BaseURL:   baseURL,
		Model:     model,
		TargetDim: targetDim,
		HTTPClient: &http.Client{
			// Increased timeout to 5 minutes for large contexts/slow models
			Timeout: 300 * time.Second,
		},
	}
}

// EmbedRequest follows Ollama API format
type EmbedRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type EmbedResponse struct {
	Embedding []float32 `json:"embedding"`
}

func (c *HTTPClient) Embed(text string, isQuery bool) ([]float32, error) {
	// Simple Nomic/Gemma formatting logic
	prefix := "search_document: "
	if isQuery {
		prefix = "search_query: "
	}
	prompt := prefix + text

	reqBody := EmbedRequest{
		Model:  c.Model,
		Prompt: prompt,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	// Log Raw Request
	util.Debug("LLM [HTTP] Request Payload:\n%s", string(jsonData))

	// Adjust endpoint based on provider (Ollama example)
	resp, err := c.HTTPClient.Post(c.BaseURL+"/api/embeddings", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		util.Debug("LLM [HTTP] Connection Error: %v", err)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		util.Debug("LLM [HTTP] API Status Error: %s", resp.Status)
		return nil, fmt.Errorf("API returned status: %s", resp.Status)
	}

	// Read Body for Logging
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Log Raw Response
	// Note: This can be very large due to vector arrays
	util.Debug("LLM [HTTP] Response Payload:\n%s", string(bodyBytes))

	// Decode
	var result EmbedResponse
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return nil, err
	}

	vec := result.Embedding

	// Handle Matryoshka Truncation
	if c.TargetDim > 0 && len(vec) > c.TargetDim {
		util.Debug("LLM [HTTP] Truncating vector from %d to %d", len(vec), c.TargetDim)
		vec = vec[:c.TargetDim]

		// Re-normalize after truncation
		var sum float64
		for _, v := range vec {
			sum += float64(v * v)
		}
		sum = math.Sqrt(sum)
		if sum > 0 {
			norm := float32(1.0 / sum)
			for i := range vec {
				vec[i] *= norm
			}
		}
	}

	return vec, nil
}

func (c *HTTPClient) Close() error {
	// HTTP client doesn't need specific cleanup
	return nil
}
