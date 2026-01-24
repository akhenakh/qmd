package chat

import (
	"encoding/json"
	"fmt"
)

// OllamaRequest represents the payload sent to Ollama
type OllamaRequest struct {
	Model    string         `json:"model"`
	Messages []Message      `json:"messages"`
	Tools    []ToolDef      `json:"tools,omitempty"`
	Stream   bool           `json:"stream"`
	Options  map[string]any `json:"options,omitempty"`
}

// Message represents a chat message
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"` // For role: tool response linkage
}

// ToolDef represents a tool definition passed to Ollama
type ToolDef struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type ToolCallFunc struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// UnmarshalJSON implements custom unmarshaling to handle both string and object formats for arguments
func (t *ToolCallFunc) UnmarshalJSON(data []byte) error {
	// Define a shadow struct to avoid recursion
	type Alias ToolCallFunc
	aux := &struct {
		Arguments interface{} `json:"arguments"`
		*Alias
	}{
		Alias: (*Alias)(t),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	// Handle Arguments based on type
	switch v := aux.Arguments.(type) {
	case string:
		// OpenAI style: JSON string encoded inside the field
		if v == "" {
			t.Arguments = make(map[string]any)
			return nil
		}
		if err := json.Unmarshal([]byte(v), &t.Arguments); err != nil {
			return fmt.Errorf("failed to parse tool arguments string: %w", err)
		}
	case map[string]interface{}:
		// Ollama style: JSON object
		t.Arguments = v
	default:
		// nil or other, initialize empty
		t.Arguments = make(map[string]any)
	}

	return nil
}

// OllamaResponse represents the response from Ollama
type OllamaResponse struct {
	Model     string  `json:"model"`
	CreatedAt string  `json:"created_at"`
	Message   Message `json:"message"`
	Done      bool    `json:"done"`
}
