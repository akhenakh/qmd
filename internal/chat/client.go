package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/akhenakh/qmd/internal/mcpserver"
	"github.com/mark3labs/mcp-go/mcp"
)

type OllamaClient struct {
	BaseURL  string
	Model    string
	MCP      *mcpserver.Server
	Messages []Message
	Tools    []ToolDef
	Client   *http.Client
}

func NewOllamaClient(url, model string, mcp *mcpserver.Server) *OllamaClient {
	var ollamaTools []ToolDef
	for _, t := range mcp.GetTools() {
		ollamaTools = append(ollamaTools, ToolDef{
			Type: "function",
			Function: ToolFunc{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}

	return &OllamaClient{
		BaseURL: url,
		Model:   model,
		MCP:     mcp,
		Tools:   ollamaTools,
		Client:  &http.Client{Timeout: 300 * time.Second},
		Messages: []Message{
			{Role: "system", Content: "You are a helpful assistant with access to a knowledge base of markdown notes. Use 'query' for most questions. Always answer based on the retrieved context."},
		},
	}
}

func (c *OllamaClient) Chat(userPrompt string) (string, error) {
	// 1. Append User Message
	c.Messages = append(c.Messages, Message{Role: "user", Content: userPrompt})

	var finalContent string
	var toolCallsMade bool

	// Max turns loop
	for i := 0; i < 5; i++ {
		reqBody := ChatRequest{
			Model:    c.Model,
			Messages: c.Messages,
			Stream:   false,
			Tools:    c.Tools,
			Options: map[string]interface{}{
				"num_ctx": 8192,
			},
		}

		jsonBody, err := json.Marshal(reqBody)
		if err != nil {
			return "", fmt.Errorf("marshal error: %w", err)
		}

		resp, err := c.Client.Post(c.BaseURL+"/api/chat", "application/json", bytes.NewBuffer(jsonBody))
		if err != nil {
			return "", fmt.Errorf("network error: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			return "", fmt.Errorf("ollama API error: %s", resp.Status)
		}

		var chatResp ChatResponse
		if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
			return "", fmt.Errorf("decode error: %w", err)
		}

		// Normalize Response: Handle OpenAI-style 'choices' vs Ollama-style 'message'
		var respMessage Message
		if len(chatResp.Choices) > 0 {
			respMessage = chatResp.Choices[0].Message
		} else {
			respMessage = chatResp.Message
		}

		// 2. Append Assistant Response (stores arguments exactly as received)
		c.Messages = append(c.Messages, respMessage)

		// Check if we are done (no tool calls)
		if len(respMessage.ToolCalls) == 0 {
			finalContent = respMessage.Content

			// Fix empty content issues
			if finalContent == "" {
				if toolCallsMade {
					finalContent = "(Model finished execution but returned no summary text)"
				} else {
					finalContent = "(Model returned empty response)"
				}
				// Update the history so next turn sees content
				c.Messages[len(c.Messages)-1].Content = finalContent
			}

			return finalContent, nil
		}

		// 3. Handle Tool Calls
		toolCallsMade = true
		for _, tc := range respMessage.ToolCalls {
			// Dynamic Argument Parsing
			var args map[string]interface{}
			switch v := tc.Function.Arguments.(type) {
			case string:
				if err := json.Unmarshal([]byte(v), &args); err != nil {
					errMsg := fmt.Sprintf("Error parsing arguments JSON: %v", err)
					c.Messages = append(c.Messages, Message{
						Role:       "tool",
						Content:    errMsg,
						Name:       tc.Function.Name,
						ToolCallID: tc.ID,
					})
					continue
				}
			case map[string]interface{}:
				args = v
			default:
				args = make(map[string]interface{})
			}

			// Execute Tool
			res, err := c.MCP.CallTool(context.Background(), tc.Function.Name, args)

			content := ""
			if err != nil {
				content = fmt.Sprintf("Error executing tool %s: %v", tc.Function.Name, err)
			} else {
				for _, r := range res.Content {
					if txt, ok := r.(mcp.TextContent); ok {
						content += txt.Text
					}
				}
			}

			if content == "" {
				content = "Tool executed successfully but returned no output."
			}

			// Append Tool Result
			c.Messages = append(c.Messages, Message{
				Role:       "tool",
				Content:    content,
				Name:       tc.Function.Name,
				ToolCallID: tc.ID,
			})
		}
	}

	// 5. Cleanup if max turns reached
	if len(c.Messages) > 0 && c.Messages[len(c.Messages)-1].Role == "tool" {
		fallback := "(Conversation turn limit reached)"
		c.Messages = append(c.Messages, Message{Role: "assistant", Content: fallback})
		return fallback, fmt.Errorf("max conversation turns exceeded")
	}

	return "", fmt.Errorf("unexpected chat state")
}
