package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/akhenakh/qmd/internal/config"
	"github.com/akhenakh/qmd/internal/llm"
	"github.com/akhenakh/qmd/internal/store"
	"github.com/tmc/langchaingo/textsplitter"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type Server struct {
	store  *store.Store
	llm    llm.Embedder
	mcp    *server.MCPServer
	config *config.Config

	// Internal storage to allow local execution (Chat loop)
	toolDefs     []mcp.Tool
	toolHandlers map[string]server.ToolHandlerFunc
}

// Internal structures for JSON responses
type searchResultJSON struct {
	Filepath string   `json:"filepath"`
	Title    string   `json:"title"`
	Score    float64  `json:"score"`
	Snippet  string   `json:"snippet,omitempty"`
	Matches  []string `json:"matches,omitempty"`
}

type statusJSON struct {
	TotalDocuments int `json:"total_documents"`
	Collections    int `json:"collections"`
	Embeddings     int `json:"embeddings"`
}

func NewServer(s *store.Store, l llm.Embedder, cfg *config.Config) *Server {
	mcpServer := server.NewMCPServer(
		"qmd",
		"1.0.0",
		server.WithToolCapabilities(true),
		server.WithResourceCapabilities(true, false), // Subscribe disabled
		server.WithLogging(),
	)

	srv := &Server{
		store:        s,
		llm:          l,
		mcp:          mcpServer,
		config:       cfg,
		toolHandlers: make(map[string]server.ToolHandlerFunc),
		toolDefs:     make([]mcp.Tool, 0),
	}

	srv.registerTools()
	srv.registerResources()
	return srv
}

func (s *Server) Start() error {
	// Serve via Stdio by default for local agent integration
	return server.ServeStdio(s.mcp)
}

// GetTools returns the list of registered tools for the Chat loop
func (s *Server) GetTools() []mcp.Tool {
	return s.toolDefs
}

// CallTool executes a tool locally by name
func (s *Server) CallTool(ctx context.Context, name string, args map[string]interface{}) (*mcp.CallToolResult, error) {
	handler, ok := s.toolHandlers[name]
	if !ok {
		return nil, fmt.Errorf("tool not found: %s", name)
	}

	// Construct the request object expected by the mcp-go handler
	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      name,
			Arguments: args,
		},
	}

	return handler(ctx, req)
}

func (s *Server) addTool(tool mcp.Tool, handler server.ToolHandlerFunc) {
	// Register with the actual MCP server for external agents
	s.mcp.AddTool(tool, handler)
	// Store locally for internal chat
	s.toolDefs = append(s.toolDefs, tool)
	s.toolHandlers[tool.Name] = handler
}

func (s *Server) registerTools() {
	searchTool := mcp.NewTool("search",
		mcp.WithDescription("Full text search using BM25. Returns a JSON list of matches. Use context_lines to see surrounding text."),
		mcp.WithString("query", mcp.Required(), mcp.Description("The search query")),
		mcp.WithNumber("limit", mcp.DefaultNumber(10), mcp.Description("Max number of documents to return")),
		mcp.WithNumber("context_lines", mcp.DefaultNumber(1), mcp.Description("Number of lines to show before and after a match")),
		mcp.WithBoolean("find_all", mcp.DefaultBool(false), mcp.Description("If true, returns all matches in a file instead of just the first one")),
	)

	s.addTool(searchTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, _ := request.RequireString("query")
		limit := request.GetInt("limit", 10)
		contextLines := request.GetInt("context_lines", 1)
		findAll := request.GetBool("find_all", false)

		results, err := s.store.SearchFTS(query, limit, contextLines, findAll)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Search failed: %v", err)), nil
		}

		resp := make([]searchResultJSON, len(results))
		for i, r := range results {
			resp[i] = searchResultJSON{
				Filepath: r.Filepath,
				Title:    r.Title,
				Score:    r.Score,
				Snippet:  r.Snippet,
				Matches:  r.Matches,
			}
		}

		jsonBytes, err := json.Marshal(resp)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("JSON marshal failed: %v", err)), nil
		}

		return mcp.NewToolResultText(string(jsonBytes)), nil
	})

	// Vector Search Tool
	vsearchTool := mcp.NewTool("vsearch",
		mcp.WithDescription("Semantic search using vector embeddings. Returns the matched text chunk. Use context_lines to include surrounding text from the document."),
		mcp.WithString("query", mcp.Required(), mcp.Description("The search query")),
		mcp.WithNumber("limit", mcp.DefaultNumber(10), mcp.Description("Max number of results")),
		mcp.WithNumber("context_lines", mcp.DefaultNumber(0), mcp.Description("Number of lines to show before and after the matched chunk")),
	)

	s.addTool(vsearchTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, _ := request.RequireString("query")
		limit := request.GetInt("limit", 10)
		contextLines := request.GetInt("context_lines", 0)

		// Generate embedding
		vec, err := s.llm.Embed(query, true)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Embedding generation failed: %v", err)), nil
		}

		results, err := s.store.SearchVec(vec, limit)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Vector search failed: %v", err)), nil
		}

		// Initialize splitter to reconstruct chunks
		splitter := textsplitter.NewMarkdownTextSplitter(
			textsplitter.WithChunkSize(s.config.ChunkSize),
			textsplitter.WithChunkOverlap(s.config.ChunkOverlap),
			textsplitter.WithHeadingHierarchy(true),
		)

		resp := make([]searchResultJSON, len(results))
		for i, r := range results {
			// Re-split logic to find the specific chunk
			snippet := r.Snippet // Default to beginning/summary if splitting fails
			contentToSplit := r.Body
			titleHeader := fmt.Sprintf("# %s", r.Title)
			if !strings.Contains(r.Body, titleHeader) {
				contentToSplit = fmt.Sprintf("%s\n\n%s", titleHeader, r.Body)
			}

			chunks, err := splitter.SplitText(contentToSplit)
			if err == nil && r.Seq < len(chunks) {
				chunkText := chunks[r.Seq]
				if contextLines > 0 {
					// Expand context around the chunk
					snippet = extractContext(r.Body, chunkText, contextLines)
				} else {
					snippet = chunkText
				}
			}

			resp[i] = searchResultJSON{
				Filepath: r.Filepath,
				Title:    r.Title,
				Score:    r.Score,
				Snippet:  snippet,
			}
		}

		jsonBytes, err := json.Marshal(resp)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("JSON marshal failed: %v", err)), nil
		}

		return mcp.NewToolResultText(string(jsonBytes)), nil
	})

	// Hybrid Query Tool
	queryTool := mcp.NewTool("query",
		mcp.WithDescription("Hybrid search using both keywords and semantic meaning (RRF). Best for most queries. Supports context_lines to see surrounding text."),
		mcp.WithString("query", mcp.Required(), mcp.Description("The search query")),
		mcp.WithNumber("limit", mcp.DefaultNumber(10), mcp.Description("Max number of results")),
		mcp.WithNumber("context_lines", mcp.DefaultNumber(1), mcp.Description("Number of lines to show before and after the match")),
	)

	s.addTool(queryTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if s.llm == nil {
			return mcp.NewToolResultError("Embeddings are not configured. Hybrid search unavailable."), nil
		}

		query, _ := request.RequireString("query")
		limit := request.GetInt("limit", 10)
		contextLines := request.GetInt("context_lines", 1)

		// Generate embedding
		vec, err := s.llm.Embed(query, true)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Embedding generation failed: %v", err)), nil
		}

		// Pass contextLines to hybrid search
		results, err := s.store.SearchHybrid(query, vec, limit, contextLines)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Hybrid search failed: %v", err)), nil
		}

		// Initialize splitter for vector fallback cases
		splitter := textsplitter.NewMarkdownTextSplitter(
			textsplitter.WithChunkSize(s.config.ChunkSize),
			textsplitter.WithChunkOverlap(s.config.ChunkOverlap),
			textsplitter.WithHeadingHierarchy(true),
		)

		resp := make([]searchResultJSON, len(results))
		for i, r := range results {
			// If we have matches (from FTS), the context is already handled by SearchFTS.
			// If no matches, it implies this result came primarily from Vector search,
			// so we need to generate the snippet from the chunk + context.
			finalSnippet := r.Snippet

			if len(r.Matches) == 0 && r.Body != "" {
				contentToSplit := r.Body
				titleHeader := fmt.Sprintf("# %s", r.Title)
				if !strings.Contains(r.Body, titleHeader) {
					contentToSplit = fmt.Sprintf("%s\n\n%s", titleHeader, r.Body)
				}
				chunks, err := splitter.SplitText(contentToSplit)
				if err == nil && r.Seq < len(chunks) {
					chunkText := chunks[r.Seq]
					if contextLines > 0 {
						finalSnippet = extractContext(r.Body, chunkText, contextLines)
					} else {
						finalSnippet = chunkText
					}
				}
			} else if len(r.Matches) > 0 {
				// If we have FTS matches, use the first match which contains context
				finalSnippet = r.Matches[0]
			}

			resp[i] = searchResultJSON{
				Filepath: r.Filepath,
				Title:    r.Title,
				Score:    r.Score,
				Snippet:  finalSnippet,
				Matches:  r.Matches,
			}
		}

		jsonBytes, err := json.Marshal(resp)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("JSON marshal failed: %v", err)), nil
		}

		return mcp.NewToolResultText(string(jsonBytes)), nil
	})

	// Get Document Tool
	getTool := mcp.NewTool("get_document",
		mcp.WithDescription("Retrieve the full content of a specific document"),
		mcp.WithString("path", mcp.Required(), mcp.Description("The document path (e.g., 'notes/meeting.md')")),
	)

	s.addTool(getTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		pathStr, _ := request.RequireString("path")

		// Path format expected: collection/path/to/file.md
		parts := strings.SplitN(pathStr, "/", 2)
		if len(parts) < 2 {
			return mcp.NewToolResultError("Invalid path format. Expected 'collection/path'"), nil
		}
		collection, relPath := parts[0], parts[1]

		content, err := s.store.GetDocument(collection, relPath)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Failed to get document: %v", err)), nil
		}

		return mcp.NewToolResultText(content), nil
	})

	// Status Tool
	// Added dummy 'details' parameter to satisfy llama.cpp schema requirements (non-empty properties)
	statusTool := mcp.NewTool("status",
		mcp.WithDescription("Get the status of the qmd index in JSON format"),
		mcp.WithBoolean("details", mcp.DefaultBool(false), mcp.Description("Include detailed stats")),
	)

	s.addTool(statusTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		stats, err := s.store.GetStats()
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Failed to get stats: %v", err)), nil
		}

		sJSON := statusJSON{
			TotalDocuments: stats.TotalDocuments,
			Collections:    stats.Collections,
			Embeddings:     stats.Embeddings,
		}

		jsonBytes, err := json.Marshal(sJSON)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("JSON marshal failed: %v", err)), nil
		}

		return mcp.NewToolResultText(string(jsonBytes)), nil
	})
}

func (s *Server) registerResources() {
	// Template for accessing any document: qmd://{collection}/{path}
	// Note: URI templates in MCP are RFC 6570. {+path} handles slashes.

	s.mcp.AddResourceTemplate(
		mcp.NewResourceTemplate("qmd://{collection}/{+path}", "Document"),
		func(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
			vars := request.Params.Arguments
			collection, ok1 := vars["collection"].(string)

			// Handle potentially array or string depending on implementation
			// Safe casting if mcp-go provides it, but here we assume simple map access
			// Check if it came in as array from mcp-go routing
			if !ok1 {
				if colArr, ok := vars["collection"].([]string); ok && len(colArr) > 0 {
					collection = colArr[0]
				} else {
					return nil, fmt.Errorf("invalid collection argument")
				}
			}

			var path string
			if p, ok := vars["path"].(string); ok {
				path = p
			} else if pArr, ok := vars["path"].([]string); ok && len(pArr) > 0 {
				path = strings.Join(pArr, "/") // Reconstruct path if split
			} else {
				return nil, fmt.Errorf("invalid path argument")
			}

			content, err := s.store.GetDocument(collection, path)
			if err != nil {
				// Resource not found logic
				return nil, fmt.Errorf("document not found: %w", err)
			}

			return []mcp.ResourceContents{
				mcp.TextResourceContents{
					URI:      request.Params.URI,
					MIMEType: "text/markdown",
					Text:     content,
				},
			}, nil
		},
	)
}

// GetUnderlyingServer exposes the raw MCP server instance.
// This is used for creating in-process clients (e.g. for the chat command).
func (s *Server) GetUnderlyingServer() *server.MCPServer {
	return s.mcp
}

// extractContext locates the chunk within the full body and returns the chunk
// extended by n lines before and after.
func extractContext(body string, chunk string, n int) string {
	if n <= 0 {
		return chunk
	}

	// Normalize CRLF to LF for index calculation stability
	body = strings.ReplaceAll(body, "\r\n", "\n")
	chunk = strings.ReplaceAll(chunk, "\r\n", "\n")

	startIdx := strings.Index(body, chunk)
	if startIdx == -1 {
		// Fallback: if exact string match fails (e.g. due to splitter stripping whitespace), just return chunk
		return chunk
	}

	lines := strings.Split(body, "\n")

	// Find which line index startIdx corresponds to
	currentLen := 0
	startLine := 0
	for i, line := range lines {
		// +1 for the newline char
		lineLen := len(line) + 1
		if currentLen+lineLen > startIdx {
			startLine = i
			break
		}
		currentLen += lineLen
	}

	// Calculate end line of the chunk
	chunkLineCount := strings.Count(chunk, "\n")
	endLine := startLine + chunkLineCount

	// Expand context
	printStart := startLine - n
	if printStart < 0 {
		printStart = 0
	}
	printEnd := endLine + n
	if printEnd >= len(lines) {
		printEnd = len(lines) - 1
	}

	var sb strings.Builder
	for i := printStart; i <= printEnd; i++ {
		// Mark the actual chunk lines with a subtle indicator if needed,
		// but standard output usually just dumps text.
		// We'll keep it plain to avoid confusing the LLM with custom markers.
		sb.WriteString(lines[i])
		sb.WriteString("\n")
	}

	return strings.TrimRight(sb.String(), "\n")
}
