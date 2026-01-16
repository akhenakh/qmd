package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/akhenakh/qmd/internal/config"
	"github.com/akhenakh/qmd/internal/llm"
	"github.com/akhenakh/qmd/internal/mcpserver"
	"github.com/akhenakh/qmd/internal/store"

	"github.com/spf13/cobra"
	"github.com/tmc/langchaingo/textsplitter"
)

var (
	// Flags
	dbPath    string
	ollamaURL string
	modelName string
	embedDim  int

	localMode      bool
	localModelPath string
	localLibPath   string

	contextLines int
	findAll      bool

	// Global instances
	globalStore  *store.Store
	globalConfig *config.Config
)

func getEmbedder() (llm.Embedder, error) {
	if globalConfig.UseLocal {
		if globalConfig.LocalModelPath == "" {
			return nil, fmt.Errorf("local mode enabled but local_model_path is missing")
		}
		if globalConfig.LocalLibPath == "" && os.Getenv("YZMA_LIB") != "" {
			globalConfig.LocalLibPath = os.Getenv("YZMA_LIB")
		}
		if globalConfig.LocalLibPath == "" {
			return nil, fmt.Errorf("local mode enabled but local_lib_path is missing")
		}
		fmt.Printf("Loading local model: %s\n", globalConfig.LocalModelPath)
		return llm.NewLocalClient(globalConfig.LocalModelPath, globalConfig.LocalLibPath, globalConfig.EmbedDimensions)
	}
	return llm.NewHTTPClient(globalConfig.OllamaURL, globalConfig.ModelName, globalConfig.EmbedDimensions), nil
}

func generateEmbeddings() {
	embedder, err := getEmbedder()
	if err != nil {
		log.Fatal(err)
	}
	defer embedder.Close()

	pending, err := globalStore.GetPendingEmbeddings()
	if err != nil {
		log.Fatal(err)
	}
	if len(pending) == 0 {
		fmt.Println("No pending embeddings.")
		return
	}

	fmt.Printf("Generating embeddings for %d documents (Dim: %d)...\n", len(pending), globalConfig.EmbedDimensions)

	splitter := textsplitter.NewMarkdownTextSplitter(
		textsplitter.WithChunkSize(globalConfig.ChunkSize),
		textsplitter.WithChunkOverlap(globalConfig.ChunkOverlap),
	)

	for hash, content := range pending {
		chunks, err := splitter.SplitText(content)
		if err != nil {
			log.Printf("Error splitting: %v", err)
			continue
		}

		for i, chunk := range chunks {
			vec, err := embedder.Embed(chunk, false)
			if err != nil {
				log.Printf("Error embedding: %v", err)
				continue
			}
			if err := globalStore.SaveEmbedding(hash, i, vec); err != nil {
				log.Fatal(err)
			}
		}
		fmt.Print(".")
	}
	fmt.Println("\nDone.")
}

func main() {
	var rootCmd = &cobra.Command{
		Use: "qmd",
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			var err error

			// Open Store
			globalStore, err = store.NewStore(dbPath)
			if err != nil {
				log.Fatalf("Failed to open store at %s: %v", dbPath, err)
			}

			// Load Config
			globalConfig, err = globalStore.LoadConfig()
			if err != nil {
				log.Printf("Warning: Could not load config from DB: %v", err)
				globalConfig = config.Default()
			}

			// Ensure Schema for Vectors matches config ONLY IF CONFIGURED
			if globalConfig.EmbeddingsConfigured {
				if err := globalStore.EnsureVectorTable(globalConfig.EmbedDimensions); err != nil {
					log.Fatalf("Failed to ensure vector table: %v", err)
				}
			}
		},
		PersistentPostRun: func(cmd *cobra.Command, args []string) {
			if globalStore != nil && globalStore.DB != nil {
				globalStore.DB.Close()
			}
		},
	}

	rootCmd.PersistentFlags().StringVar(&dbPath, "db", "./qmd.sqlite", "Path to SQLite database")

	var cmdInfo = &cobra.Command{
		Use:   "info",
		Short: "Show index information and configuration",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("=== Configuration ===")
			fmt.Printf("Database Path:    %s\n", globalStore.DBPath)

			if globalConfig.EmbeddingsConfigured {
				fmt.Printf("Model Name:       %s\n", globalConfig.ModelName)
				fmt.Printf("Dimensions:       %d\n", globalConfig.EmbedDimensions)
				fmt.Printf("Chunk Size:       %d\n", globalConfig.ChunkSize)
				fmt.Printf("Chunk Overlap:    %d\n", globalConfig.ChunkOverlap)

				if globalConfig.UseLocal {
					fmt.Println("Mode:             Local (llama.cpp)")
					fmt.Printf("Local Model:      %s\n", globalConfig.LocalModelPath)
					fmt.Printf("Local Lib:        %s\n", globalConfig.LocalLibPath)
				} else {
					fmt.Println("Mode:             Ollama Server")
					fmt.Printf("Ollama URL:       %s\n", globalConfig.OllamaURL)
				}
			} else {
				fmt.Println("Embedding:        Not configured (run 'qmd embed' to setup)")
			}
			fmt.Println()

			fmt.Println("=== Collections ===")
			if len(globalConfig.Collections) == 0 {
				fmt.Println("  (No collections added)")
			} else {
				for _, col := range globalConfig.Collections {
					fmt.Printf("- %s\n  Path: %s\n  Pattern: %s\n", col.Name, col.Path, col.Pattern)
				}
			}
			fmt.Println()

			stats, err := globalStore.GetStats()
			if err != nil {
				log.Printf("Error fetching stats: %v", err)
				return
			}

			fmt.Println("=== Index Stats ===")
			fmt.Printf("Total Documents:  %d\n", stats.TotalDocuments)
			fmt.Printf("Vector Count:     %d\n", stats.Embeddings)

			if globalConfig.EmbeddingsConfigured && stats.Embeddings > 0 {
				fmt.Printf("Embeddings:       Present\n")
			} else if globalConfig.EmbeddingsConfigured {
				fmt.Printf("Embeddings:       Configured but empty\n")
			} else {
				fmt.Println("Embeddings:       Not generated")
			}
		},
	}

	var cmdAdd = &cobra.Command{
		Use:   "add [path...]",
		Short: "Add one or more folders as collections",
		Args:  cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			var added []config.Collection

			for _, arg := range args {
				absPath, err := filepath.Abs(arg)
				if err != nil {
					log.Printf("Error resolving path %s: %v", arg, err)
					continue
				}
				name := filepath.Base(absPath)

				// Check if already exists
				exists := false
				for _, c := range globalConfig.Collections {
					if c.Path == absPath {
						exists = true
						break
					}
				}

				if !exists {
					newCol := config.Collection{
						Name:    name,
						Path:    absPath,
						Pattern: "**/*.md",
					}
					globalConfig.Collections = append(globalConfig.Collections, newCol)
					added = append(added, newCol)
					fmt.Printf("Added collection '%s' at %s\n", name, absPath)
				} else {
					fmt.Printf("Collection already exists: %s\n", absPath)
				}
			}

			if len(added) > 0 {
				if err := globalStore.SaveConfig(globalConfig); err != nil {
					log.Fatal(err)
				}
				// Reindex newly added collections
				for _, col := range added {
					reindex(col.Name, col.Path)
				}
			}
		},
	}

	var cmdUpdate = &cobra.Command{
		Use:   "update",
		Short: "Update index",
		Run: func(cmd *cobra.Command, args []string) {
			for _, col := range globalConfig.Collections {
				reindex(col.Name, col.Path)
			}

			// Only update embeddings if configured
			if globalConfig.EmbeddingsConfigured {
				generateEmbeddings()
			}
		},
	}

	var cmdEmbed = &cobra.Command{
		Use:   "embed",
		Short: "Generate missing embeddings (and configure model settings)",
		Run: func(cmd *cobra.Command, args []string) {
			// Update config from flags if provided
			if cmd.Flags().Changed("url") {
				globalConfig.OllamaURL = ollamaURL
			}
			if cmd.Flags().Changed("model") {
				globalConfig.ModelName = modelName
			}
			if cmd.Flags().Changed("dim") {
				globalConfig.EmbedDimensions = embedDim
			}
			if cmd.Flags().Changed("local") {
				globalConfig.UseLocal = localMode
			}
			if cmd.Flags().Changed("model-path") {
				globalConfig.LocalModelPath = localModelPath
			}
			if cmd.Flags().Changed("lib-path") {
				globalConfig.LocalLibPath = localLibPath
			}

			// If local mode is active and no explicit model name provided,
			// use the filename from the path as the model name.
			if globalConfig.UseLocal && globalConfig.LocalModelPath != "" && !cmd.Flags().Changed("model") {
				globalConfig.ModelName = filepath.Base(globalConfig.LocalModelPath)
			}

			// Mark as configured
			globalConfig.EmbeddingsConfigured = true

			// Save updated config
			if err := globalStore.SaveConfig(globalConfig); err != nil {
				log.Fatal(err)
			}

			// Ensure Vector Table matches dimensions
			if err := globalStore.EnsureVectorTable(globalConfig.EmbedDimensions); err != nil {
				log.Fatal(err)
			}

			generateEmbeddings()
		},
	}

	// Attach embedding-specific flags only to embed command
	cmdEmbed.Flags().StringVar(&ollamaURL, "url", "", "Ollama API URL")
	cmdEmbed.Flags().StringVar(&modelName, "model", "", "Embedding model name")
	cmdEmbed.Flags().IntVar(&embedDim, "dim", 0, "Embedding vector dimensions")
	cmdEmbed.Flags().BoolVar(&localMode, "local", false, "Use local llama.cpp inference")
	cmdEmbed.Flags().StringVar(&localModelPath, "model-path", "", "Path to GGUF model file")
	cmdEmbed.Flags().StringVar(&localLibPath, "lib-path", "", "Path to llama.cpp shared library")

	var cmdSearch = &cobra.Command{
		Use:   "search [query]",
		Short: "Full text search",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			results, err := globalStore.SearchFTS(args[0], 10, contextLines, findAll)
			if err != nil {
				log.Fatal(err)
			}
			for _, r := range results {
				fmt.Printf("\033[1;36m[%s] %s\033[0m\n", r.Filepath, r.Title)
				if len(r.Matches) > 0 {
					for _, match := range r.Matches {
						fmt.Printf("%s\n\n", match)
					}
				} else {
					fmt.Printf("   %s\n\n", strings.ReplaceAll(r.Snippet, "\n", " "))
				}
			}
		},
	}
	cmdSearch.Flags().IntVarP(&contextLines, "context", "C", 0, "Context lines")
	cmdSearch.Flags().BoolVarP(&findAll, "all", "a", false, "Show all matches")

	var cmdVSearch = &cobra.Command{
		Use:   "vsearch [query]",
		Short: "Vector semantic search",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			if !globalConfig.EmbeddingsConfigured {
				log.Fatal("Embeddings not configured. Run 'qmd embed' first.")
			}
			embedder, err := getEmbedder()
			if err != nil {
				log.Fatal(err)
			}
			defer embedder.Close()

			qVec, err := embedder.Embed(args[0], true)
			if err != nil {
				log.Fatal(err)
			}

			results, err := globalStore.SearchVec(qVec, 10)
			if err != nil {
				log.Fatal(err)
			}

			for _, r := range results {
				fmt.Printf("[%.4f] %s - %s\n", r.Score, r.Filepath, r.Title)
			}
		},
	}

	var cmdServer = &cobra.Command{
		Use:   "server",
		Short: "Start MCP server",
		Run: func(cmd *cobra.Command, args []string) {
			var embedder llm.Embedder
			var err error

			if globalConfig.EmbeddingsConfigured {
				embedder, err = getEmbedder()
				if err != nil {
					log.Printf("Warning: Failed to initialize embedder: %v. Vector search will be unavailable.", err)
				} else {
					defer embedder.Close()
				}
			} else {
				log.Println("Embeddings not configured. Vector search unavailable.")
			}

			mcpSrv := mcpserver.NewServer(globalStore, embedder)

			log.SetOutput(os.Stderr)
			if err := mcpSrv.Start(); err != nil {
				log.Fatal(err)
			}
		},
	}

	// --- Hybrid Query Command ---
	var cmdQuery = &cobra.Command{
		Use:   "query [query]",
		Short: "Hybrid search (BM25 + Vector + RRF)",
		Long:  "Performs a hybrid search combining Full-Text Search and Vector Search, ranking results using Reciprocal Rank Fusion.",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			// 1. Validation
			if !globalConfig.EmbeddingsConfigured {
				log.Fatal("Embeddings not configured. Run 'qmd embed' first.")
			}

			// 2. Initialize Store & Embedder
			if globalStore == nil {
				// Should be initialized by PersistentPreRun, but just in case
				var err error
				globalStore, err = store.NewStore(dbPath)
				if err != nil {
					log.Fatal(err)
				}
			}
			defer globalStore.DB.Close()

			embedder, err := getEmbedder()
			if err != nil {
				log.Fatal(err)
			}
			defer embedder.Close()

			query := args[0]

			// 3. Generate Embedding for the query
			// Note: We perform this synchronously. In a more advanced version with query expansion,
			// we would generate multiple variations here.
			fmt.Printf("Analyzing query: %q...\n", query)
			qVec, err := embedder.Embed(query, true)
			if err != nil {
				log.Fatal(err)
			}

			// 4. Perform Hybrid Search
			results, err := globalStore.SearchHybrid(query, qVec, 10)
			if err != nil {
				log.Fatal(err)
			}

			// 5. Output Results
			if len(results) == 0 {
				fmt.Println("No results found.")
				return
			}

			fmt.Println("\nHybrid Search Results (RRF):")
			for i, r := range results {
				// Visual separator
				fmt.Printf("\n%d. \033[1;36m%s\033[0m (Score: %.4f)\n", i+1, r.Filepath, r.Score)
				fmt.Printf("   Title: %s\n", r.Title)

				// Prefer showing specific matches if available (from FTS), otherwise snippet
				if len(r.Matches) > 0 {
					for _, match := range r.Matches {
						fmt.Printf("   %s\n", match)
					}
				} else {
					// Clean up newlines for cleaner output
					snippet := strings.ReplaceAll(r.Snippet, "\n", " ")
					if len(snippet) > 150 {
						snippet = snippet[:150] + "..."
					}
					fmt.Printf("   %s\n", snippet)
				}
			}
		},
	}

	rootCmd.AddCommand(cmdAdd, cmdUpdate, cmdInfo, cmdEmbed, cmdSearch, cmdVSearch, cmdQuery, cmdServer)
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func reindex(colName, rootPath string) {
	fmt.Printf("Indexing %s...\n", colName)
	err := filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".md") {
			relPath, _ := filepath.Rel(rootPath, path)
			relPath = filepath.ToSlash(relPath)
			content, _ := os.ReadFile(path)
			if err := globalStore.IndexDocument(colName, relPath, string(content)); err != nil {
				log.Printf("Error indexing %s: %v", relPath, err)
			}
		}
		return nil
	})
	if err != nil {
		log.Printf("Error walking path %s: %v", rootPath, err)
	}
}
