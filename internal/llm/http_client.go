package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type HTTPClient struct {
	BaseURL    string
	Model      string
	HTTPClient *http.Client
}

func NewHTTPClient(baseURL, model string) *HTTPClient {
	return &HTTPClient{
		BaseURL: baseURL,
		Model:   model,
		HTTPClient: &http.Client{
			Timeout: 60 * time.Second,
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

	reqBody := EmbedRequest{
		Model:  c.Model,
		Prompt: prefix + text,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	// Adjust endpoint based on provider (Ollama example)
	resp, err := c.HTTPClient.Post(c.BaseURL+"/api/embeddings", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("API returned status: %s", resp.Status)
	}

	var result EmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	return result.Embedding, nil
}

func (c *HTTPClient) Close() error {
	// HTTP client doesn't need specific cleanup
	return nil
}
