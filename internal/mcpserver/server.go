package mcpserver

import (
	"context"
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
	// --- Search Tool ---
	searchTool := mcp.NewTool("search",
		mcp.WithDescription("Full text search using BM25. Use this for specific keywords."),
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

		// Fixed: Passing all 4 arguments to SearchFTS
		results, err := s.store.SearchFTS(query, limit, contextLines, findAll)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Search failed: %v", err)), nil
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Found %d results:\n\n", len(results)))
		for _, r := range results {
			sb.WriteString(fmt.Sprintf("## %s\n", r.Filepath))
			if len(r.Matches) > 0 {
				for _, match := range r.Matches {
					sb.WriteString(fmt.Sprintf("%s\n...\n", match))
				}
			} else {
				sb.WriteString(fmt.Sprintf("%s\n", r.Snippet))
			}
			sb.WriteString("\n")
		}

		return mcp.NewToolResultText(sb.String()), nil
	})

	// --- Vector Search Tool ---
	vsearchTool := mcp.NewTool("vsearch",
		mcp.WithDescription("Semantic search using vector embeddings. Use this for concept search or when keywords miss."),
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

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Found %d results:\n\n", len(results)))
		for _, r := range results {
			// Score is similarity (0-1)
			sb.WriteString(fmt.Sprintf("## %s (Score: %.2f)\n%s\n\n", r.Filepath, r.Score, r.Snippet))
		}

		return mcp.NewToolResultText(sb.String()), nil
	})

	// --- Get Document Tool ---
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

	// --- Status Tool ---
	statusTool := mcp.NewTool("status",
		mcp.WithDescription("Get the status of the qmd index"),
	)

	s.mcp.AddTool(statusTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		stats, err := s.store.GetStats()
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Failed to get stats: %v", err)), nil
		}

		info := fmt.Sprintf("Total Documents: %d\nCollections: %d\nEmbeddings: %d",
			stats.TotalDocuments, stats.Collections, stats.Embeddings)

		return mcp.NewToolResultText(info), nil
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
