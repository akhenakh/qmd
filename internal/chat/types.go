package chat

// Message represents a chat message in the format expected by Ollama/OpenAI
type Message struct {
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Images  []string `json:"images,omitempty"`

	// ToolCalls fields (for role: assistant)
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`

	// ToolResult fields (for role: tool)
	ToolCallID string `json:"tool_call_id,omitempty"`
	Name       string `json:"name,omitempty"`
}

type ToolCall struct {
	Function FunctionCall `json:"function"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
}

type FunctionCall struct {
	Name      string      `json:"name"`
	Arguments interface{} `json:"arguments"`
}

// ToolExecutionLog contains details about a tool execution for UI display
type ToolExecutionLog struct {
	Name string
	Args map[string]interface{}
}

type ChatRequest struct {
	Model    string                 `json:"model"`
	Messages []Message              `json:"messages"`
	Stream   bool                   `json:"stream"`
	Tools    []ToolDef              `json:"tools,omitempty"`
	Options  map[string]interface{} `json:"options,omitempty"`
}

type ToolDef struct {
	Type     string   `json:"type"`
	Function ToolFunc `json:"function"`
}

type ToolFunc struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}

type ChatResponse struct {
	Model   string  `json:"model"`
	Message Message `json:"message"`
	Done    bool    `json:"done"`

	// Support for OpenAI-compatible responses (used by some llama-server versions)
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
}
