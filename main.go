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
	// HTTP flags
	ollamaURL string
	modelName string
	embedDim  int

	// Local flags
	localMode      bool
	localModelPath string
	localLibPath   string

	// Search flags
	contextLines int
	findAll      bool

	// App Config
	cfg *config.Config
)

// Helper to get the appropriate embedder based on final config
func getEmbedder() (llm.Embedder, error) {
	// Check if local mode is enabled either via config or flag (merged into cfg.UseLocal)
	if cfg.UseLocal {
		if cfg.LocalModelPath == "" {
			return nil, fmt.Errorf("local mode enabled but local_model_path is missing")
		}

		// Fallback for LibPath to Env Var if config/flag is empty
		if cfg.LocalLibPath == "" && os.Getenv("YZMA_LIB") != "" {
			cfg.LocalLibPath = os.Getenv("YZMA_LIB")
		}

		if cfg.LocalLibPath == "" {
			return nil, fmt.Errorf("local mode enabled but local_lib_path is missing (and YZMA_LIB env var not set)")
		}

		fmt.Printf("Loading local model: %s\n", cfg.LocalModelPath)
		return llm.NewLocalClient(cfg.LocalModelPath, cfg.LocalLibPath)
	}

	return llm.NewHTTPClient(cfg.OllamaURL, cfg.ModelName), nil
}

func main() {
	// 1. Load config at startup
	var err error
	cfg, err = config.Load()
	if err != nil {
		log.Printf("Warning: Could not load config: %v", err)
		cfg = config.Default()
	}

	var rootCmd = &cobra.Command{
		Use: "qmd",
		// 2. PersistentPreRun executes after flags are parsed but before the command runs.
		// We use this to merge CLI flags into the Config object.
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			// HTTP / General Overrides
			if ollamaURL != "" {
				cfg.OllamaURL = ollamaURL
			}
			if modelName != "" {
				cfg.ModelName = modelName
			}
			if embedDim != 0 {
				cfg.EmbedDimensions = embedDim
			}

			// Local Inference Overrides
			// Only toggle UseLocal if the flag was explicitly set (true).
			// If flag is false (default), respect the config file.
			if cmd.Flags().Changed("local") {
				cfg.UseLocal = localMode
			}

			if localModelPath != "" {
				cfg.LocalModelPath = localModelPath
			}
			if localLibPath != "" {
				cfg.LocalLibPath = localLibPath
			}
		},
	}

	// Global flags
	rootCmd.PersistentFlags().StringVar(&ollamaURL, "url", "", fmt.Sprintf("Ollama API URL (default %s)", cfg.OllamaURL))
	rootCmd.PersistentFlags().StringVar(&modelName, "model", "", fmt.Sprintf("Embedding model name (default %s)", cfg.ModelName))
	rootCmd.PersistentFlags().IntVar(&embedDim, "dim", 0, fmt.Sprintf("Embedding vector dimensions (default %d)", cfg.EmbedDimensions))

	// Local inference flags
	rootCmd.PersistentFlags().BoolVar(&localMode, "local", false, "Use local llama.cpp inference instead of HTTP")
	rootCmd.PersistentFlags().StringVar(&localModelPath, "model-path", "", "Path to GGUF model file")
	rootCmd.PersistentFlags().StringVar(&localLibPath, "lib-path", "", "Path to llama.cpp shared library")

	// --- Collection Add ---
	var cmdAdd = &cobra.Command{
		Use:   "add [path]",
		Short: "Add a folder as a collection",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			absPath, _ := filepath.Abs(args[0])
			name := filepath.Base(absPath)

			cfg.Collections[name] = config.Collection{
				Path:    absPath,
				Pattern: "**/*.md",
			}

			if err := config.Save(cfg); err != nil {
				log.Fatal(err)
			}
			fmt.Printf("Added collection '%s' at %s\n", name, absPath)

			reindex(name, absPath, cfg.EmbedDimensions)
		},
	}

	// --- Update/Reindex ---
	var cmdUpdate = &cobra.Command{
		Use:   "update",
		Short: "Update index",
		Run: func(cmd *cobra.Command, args []string) {
			for name, col := range cfg.Collections {
				reindex(name, col.Path, cfg.EmbedDimensions)
			}
		},
	}

	// --- Embed ---
	var cmdEmbed = &cobra.Command{
		Use:   "embed",
		Short: "Generate missing embeddings",
		Run: func(cmd *cobra.Command, args []string) {
			s, err := store.NewStore(cfg.EmbedDimensions)
			if err != nil {
				log.Fatal(err)
			}
			defer s.DB.Close()

			embedder, err := getEmbedder()
			if err != nil {
				log.Fatal(err)
			}
			defer embedder.Close()

			pending, err := s.GetPendingEmbeddings()
			if err != nil {
				log.Fatal(err)
			}
			fmt.Printf("Generating embeddings for %d documents (Dim: %d, Chunk: %d)...\n",
				len(pending), cfg.EmbedDimensions, cfg.ChunkSize)

			splitter := textsplitter.NewMarkdownTextSplitter(
				textsplitter.WithChunkSize(cfg.ChunkSize),
				textsplitter.WithChunkOverlap(cfg.ChunkOverlap),
			)

			for hash, content := range pending {
				chunks, err := splitter.SplitText(content)
				if err != nil {
					log.Printf("Error splitting document %s: %v", hash, err)
					continue
				}

				for i, chunk := range chunks {
					vec, err := embedder.Embed(chunk, false)
					if err != nil {
						log.Printf("Error embedding %s (chunk %d): %v", hash, i, err)
						continue
					}

					if len(vec) != cfg.EmbedDimensions {
						log.Printf("Warning: Model returned %d dimensions, config/flag expects %d", len(vec), cfg.EmbedDimensions)
					}

					if err := s.SaveEmbedding(hash, i, vec); err != nil {
						log.Fatal(err)
					}
				}
				fmt.Print(".")
			}
			fmt.Println("\nDone.")
		},
	}

	// --- Search ---
	var cmdSearch = &cobra.Command{
		Use:   "search [query]",
		Short: "Full text search",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			s, err := store.NewStore(cfg.EmbedDimensions)
			if err != nil {
				log.Fatal(err)
			}
			defer s.DB.Close()

			results, err := s.SearchFTS(args[0], 10, contextLines, findAll)
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
	cmdSearch.Flags().IntVarP(&contextLines, "context", "C", 0, "Number of context lines to show before and after the match")
	cmdSearch.Flags().BoolVarP(&findAll, "all", "a", false, "Show all matches in the file instead of just the first one")

	// --- Vector Search ---
	var cmdVSearch = &cobra.Command{
		Use:   "vsearch [query]",
		Short: "Vector semantic search",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			s, err := store.NewStore(cfg.EmbedDimensions)
			if err != nil {
				log.Fatal(err)
			}
			defer s.DB.Close()

			embedder, err := getEmbedder()
			if err != nil {
				log.Fatal(err)
			}
			defer embedder.Close()

			fmt.Println("Embedding query...")
			qVec, err := embedder.Embed(args[0], true)
			if err != nil {
				log.Fatal(err)
			}

			results, err := s.SearchVec(qVec, 10)
			if err != nil {
				log.Fatal(err)
			}

			for _, r := range results {
				fmt.Printf("[%.4f] %s - %s\n", r.Score, r.Filepath, r.Title)
			}
		},
	}

	// --- Server (MCP) ---
	var cmdServer = &cobra.Command{
		Use:   "server",
		Short: "Start MCP server",
		Run: func(cmd *cobra.Command, args []string) {
			s, err := store.NewStore(cfg.EmbedDimensions)
			if err != nil {
				log.Fatalf("Failed to initialize store: %v", err)
			}
			defer s.DB.Close()

			embedder, err := getEmbedder()
			if err != nil {
				log.Fatal(err)
			}
			defer embedder.Close()

			mcpSrv := mcpserver.NewServer(s, embedder)

			log.SetOutput(os.Stderr)
			log.Println("Starting MCP server...")

			if err := mcpSrv.Start(); err != nil {
				log.Fatal(err)
			}
		},
	}

	rootCmd.AddCommand(cmdAdd, cmdUpdate, cmdEmbed, cmdSearch, cmdVSearch, cmdServer)
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func reindex(colName, rootPath string, dim int) {
	s, err := store.NewStore(dim)
	if err != nil {
		log.Fatal(err)
	}
	defer s.DB.Close()

	fmt.Printf("Indexing %s...\n", colName)
	err = filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".md") {
			relPath, _ := filepath.Rel(rootPath, path)
			relPath = filepath.ToSlash(relPath)

			content, _ := os.ReadFile(path)
			if err := s.IndexDocument(colName, relPath, string(content)); err != nil {
				log.Printf("Error indexing %s: %v", relPath, err)
			}
		}
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}
}
