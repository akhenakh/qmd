package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/akhenakh/qmd/internal/llm"
	"github.com/akhenakh/qmd/internal/store"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type Server struct {
	store *store.Store
	llm   llm.Embedder
	mcp   *server.MCPServer
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

func NewServer(s *store.Store, l llm.Embedder) *Server {
	mcpServer := server.NewMCPServer(
		"qmd",
		"1.0.0",
		server.WithToolCapabilities(true),
		server.WithResourceCapabilities(true, false), // Subscribe disabled
		server.WithLogging(),
	)

	srv := &Server{
		store: s,
		llm:   l,
		mcp:   mcpServer,
	}

	srv.registerTools()
	srv.registerResources()
	return srv
}

func (s *Server) Start() error {
	// Serve via Stdio by default for local agent integration
	return server.ServeStdio(s.mcp)
}

func (s *Server) registerTools() {
	searchTool := mcp.NewTool("search",
		mcp.WithDescription("Full text search using BM25. Returns a JSON list of matches."),
		mcp.WithString("query", mcp.Required(), mcp.Description("The search query")),
		mcp.WithNumber("limit", mcp.DefaultNumber(10), mcp.Description("Max number of documents to return")),
		mcp.WithNumber("context_lines", mcp.DefaultNumber(1), mcp.Description("Number of lines to show before and after a match")),
		mcp.WithBoolean("find_all", mcp.DefaultBool(false), mcp.Description("If true, returns all matches in a file instead of just the first one")),
	)

	s.mcp.AddTool(searchTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
		mcp.WithDescription("Semantic search using vector embeddings. Returns a JSON list of matches."),
		mcp.WithString("query", mcp.Required(), mcp.Description("The search query")),
		mcp.WithNumber("limit", mcp.DefaultNumber(10), mcp.Description("Max number of results")),
	)

	s.mcp.AddTool(vsearchTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, _ := request.RequireString("query")
		limit := request.GetInt("limit", 10)

		// Generate embedding
		vec, err := s.llm.Embed(query, true)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Embedding generation failed: %v", err)), nil
		}

		results, err := s.store.SearchVec(vec, limit)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Vector search failed: %v", err)), nil
		}

		resp := make([]searchResultJSON, len(results))
		for i, r := range results {
			resp[i] = searchResultJSON{
				Filepath: r.Filepath,
				Title:    r.Title,
				Score:    r.Score,
				Snippet:  r.Snippet,
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
		mcp.WithDescription("Hybrid search using both keywords and semantic meaning (RRF). Returns a JSON list of matches."),
		mcp.WithString("query", mcp.Required(), mcp.Description("The search query")),
		mcp.WithNumber("limit", mcp.DefaultNumber(10), mcp.Description("Max number of results")),
	)

	s.mcp.AddTool(queryTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if s.llm == nil {
			return mcp.NewToolResultError("Embeddings are not configured. Hybrid search unavailable."), nil
		}

		query, _ := request.RequireString("query")
		limit := request.GetInt("limit", 10)

		// Generate embedding
		vec, err := s.llm.Embed(query, true)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Embedding generation failed: %v", err)), nil
		}

		results, err := s.store.SearchHybrid(query, vec, limit)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Hybrid search failed: %v", err)), nil
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

	// Get Document Tool
	getTool := mcp.NewTool("get_document",
		mcp.WithDescription("Retrieve the full content of a specific document"),
		mcp.WithString("path", mcp.Required(), mcp.Description("The document path (e.g., 'notes/meeting.md')")),
	)

	s.mcp.AddTool(getTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
	statusTool := mcp.NewTool("status",
		mcp.WithDescription("Get the status of the qmd index in JSON format"),
	)

	s.mcp.AddTool(statusTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
