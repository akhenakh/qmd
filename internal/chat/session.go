package chat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/akhenakh/qmd/internal/mcpserver"
	"github.com/akhenakh/qmd/internal/store"
	"github.com/mark3labs/mcp-go/mcp"
)

type Session struct {
	OllamaURL string
	Model     string
	Store     *store.Store
	MCPServer *mcpserver.Server
	History   []OllamaMessage
}

type OllamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ToolCalls must be omitted if empty/nil for non-assistant roles
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Images    []string   `json:"images,omitempty"`

	// ToolCallID is required for tool messages (role: tool) to link back to the assistant's call
	ToolCallID string `json:"tool_call_id,omitempty"`
	Name       string `json:"name,omitempty"`
}

type ToolCall struct {
	Function FunctionCall `json:"function"`
	Type     string       `json:"type,omitempty"`
	ID       string       `json:"id,omitempty"`
}

type FunctionCall struct {
	Name string `json:"name"`
	// Arguments can be a map (Ollama) or a JSON string (OpenAI/llama-server)
	Arguments interface{} `json:"arguments"`
}

type ChatRequest struct {
	Model    string           `json:"model"`
	Messages []OllamaMessage  `json:"messages"`
	Stream   bool             `json:"stream"`
	Tools    []ToolDefinition `json:"tools,omitempty"`
}

type ToolDefinition struct {
	Type     string      `json:"type"`
	Function ToolDetails `json:"function"`
}

type ToolDetails struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}

type ChatResponse struct {
	// Ollama standard format
	Model   string        `json:"model"`
	Message OllamaMessage `json:"message"`
	Done    bool          `json:"done"`

	// OpenAI / llama-server format
	Choices []struct {
		Message OllamaMessage `json:"message"`
	} `json:"choices"`
}

func NewSession(url, model string, s *store.Store, mcp *mcpserver.Server) (*Session, error) {
	return &Session{
		OllamaURL: url,
		Model:     model,
		Store:     s,
		MCPServer: mcp,
		History: []OllamaMessage{
			{
				Role:    "system",
				Content: "You are a helpful assistant with access to a database of markdown notes. When asked to search, prefer using the 'vsearch' tool for semantic understanding, or the 'query' tool for hybrid search. Always try to verify facts using the provided tools before answering.",
			},
		},
	}, nil
}

func (s *Session) Start(ctx context.Context) error {
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("Chat session started. Type 'exit' or 'quit' to end.")

	// Get tools from MCP server wrapper
	mcpTools := s.MCPServer.GetTools()
	var ollamaTools []ToolDefinition
	for _, t := range mcpTools {
		ollamaTools = append(ollamaTools, ToolDefinition{
			Type: "function",
			Function: ToolDetails{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}

	for {
		fmt.Print("\n> ")
		input, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		input = strings.TrimSpace(input)

		if input == "exit" || input == "quit" {
			break
		}
		if input == "" {
			continue
		}

		s.History = append(s.History, OllamaMessage{
			Role:    "user",
			Content: input,
		})

		// Process turn
		if err := s.processTurn(ctx, ollamaTools); err != nil {
			fmt.Printf("Error: %v\n", err)
			// Remove the stranded user message on error
			if len(s.History) > 0 && s.History[len(s.History)-1].Role == "user" {
				s.History = s.History[:len(s.History)-1]
			}
		} else {
			// If processTurn returned nil but didn't append an Assistant message
			if len(s.History) > 0 && s.History[len(s.History)-1].Role == "user" {
				s.History = s.History[:len(s.History)-1]
			}
		}
	}
	return nil
}

func (s *Session) processTurn(ctx context.Context, tools []ToolDefinition) error {
	for {
		req := ChatRequest{
			Model:    s.Model,
			Messages: s.History,
			Stream:   false,
			Tools:    tools,
		}

		body, err := json.Marshal(req)
		if err != nil {
			return err
		}

		resp, err := http.Post(s.OllamaURL+"/api/chat", "application/json", bytes.NewBuffer(body))
		if err != nil {
			return err
		}

		bodyBytes, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return err
		}

		if resp.StatusCode != 200 {
			var errResp struct {
				Error string `json:"error"`
			}
			json.Unmarshal(bodyBytes, &errResp)
			errMsg := errResp.Error
			if errMsg == "" {
				errMsg = "Unknown server error"
			}
			return fmt.Errorf("server error %d %s: %s", resp.StatusCode, resp.Status, errMsg)
		}

		var chatResp ChatResponse
		if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
			return fmt.Errorf("failed to unmarshal response: %v\nRaw: %s", err, string(bodyBytes))
		}

		// Normalize Message: handle OpenAI format (choices) vs Ollama format (message)
		var msg OllamaMessage
		if len(chatResp.Choices) > 0 {
			msg = chatResp.Choices[0].Message
		} else {
			msg = chatResp.Message
		}

		// If empty content/tools, break to cleanup user message
		if msg.Content == "" && len(msg.ToolCalls) == 0 {
			fmt.Printf("(No content returned from AI). Raw response: %s\n", string(bodyBytes))
			break
		}

		s.History = append(s.History, msg)

		if len(msg.ToolCalls) > 0 {
			fmt.Println("\n[AI is using tools...]")
			for _, toolCall := range msg.ToolCalls {
				fmt.Printf("  -> Executing %s...\n", toolCall.Function.Name)

				// Parse Arguments: handle string vs map
				var args map[string]interface{}
				switch v := toolCall.Function.Arguments.(type) {
				case string:
					if err := json.Unmarshal([]byte(v), &args); err != nil {
						fmt.Printf("    Error parsing JSON string arguments: %v\n", err)
						// Append error result so model knows it failed
						s.History = append(s.History, OllamaMessage{
							Role:       "tool",
							Content:    fmt.Sprintf("Error parsing arguments: %v", err),
							Name:       toolCall.Function.Name,
							ToolCallID: toolCall.ID,
						})
						continue
					}
				case map[string]interface{}:
					args = v
				default:
					fmt.Printf("    Unknown arguments type: %T\n", v)
					continue
				}

				// Execute Tool
				res, err := s.MCPServer.CallTool(ctx, toolCall.Function.Name, args)

				var contentStr string
				if err != nil {
					contentStr = fmt.Sprintf("Error: %v", err)
				} else {
					if len(res.Content) > 0 {
						if textContent, ok := res.Content[0].(mcp.TextContent); ok {
							contentStr = textContent.Text
						} else {
							raw, _ := json.Marshal(res.Content)
							contentStr = string(raw)
						}
					}
				}

				if contentStr == "" {
					contentStr = "No output"
				}

				// Append result to history
				s.History = append(s.History, OllamaMessage{
					Role:       "tool",
					Content:    contentStr,
					Name:       toolCall.Function.Name,
					ToolCallID: toolCall.ID,
				})
			}
			// Loop back to send tool results to LLM
			continue
		}

		// No tool calls, print response
		fmt.Printf("\n%s\n", msg.Content)
		break
	}
	return nil
}
