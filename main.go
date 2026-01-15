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
	"github.com/akhenakh/qmd/internal/util"

	"github.com/spf13/cobra"
)

var (
	ollamaURL string
	modelName string
)

func main() {
	var rootCmd = &cobra.Command{Use: "qmd"}

	rootCmd.PersistentFlags().StringVar(&ollamaURL, "url", "http://localhost:11434", "Ollama API URL")
	rootCmd.PersistentFlags().StringVar(&modelName, "model", "nomic-embed-text", "Embedding model name")

	// --- Collection Add ---
	var cmdAdd = &cobra.Command{
		Use:   "add [path]",
		Short: "Add a folder as a collection",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			absPath, _ := filepath.Abs(args[0])
			name := filepath.Base(absPath)

			cfg, err := config.Load()
			if err != nil {
				log.Fatal(err)
			}

			cfg.Collections[name] = config.Collection{
				Path:    absPath,
				Pattern: "**/*.md",
			}

			if err := config.Save(cfg); err != nil {
				log.Fatal(err)
			}
			fmt.Printf("Added collection '%s' at %s\n", name, absPath)

			// Trigger index
			reindex(name, absPath)
		},
	}

	// --- Update/Reindex ---
	var cmdUpdate = &cobra.Command{
		Use:   "update",
		Short: "Update index",
		Run: func(cmd *cobra.Command, args []string) {
			cfg, _ := config.Load()
			for name, col := range cfg.Collections {
				reindex(name, col.Path)
			}
		},
	}

	// --- Embed ---
	var cmdEmbed = &cobra.Command{
		Use:   "embed",
		Short: "Generate missing embeddings",
		Run: func(cmd *cobra.Command, args []string) {
			s, _ := store.NewStore()
			defer s.DB.Close()
			client := llm.NewClient(ollamaURL, modelName)

			pending, _ := s.GetPendingEmbeddings()
			fmt.Printf("Generating embeddings for %d documents...\n", len(pending))

			for hash, content := range pending {
				// Chunking logic (simplified)
				chunks := util.ChunkText(content, 1000) // ~1000 chars
				for i, chunk := range chunks {
					vec, err := client.Embed(chunk, false)
					if err != nil {
						log.Printf("Error embedding %s: %v", hash, err)
						continue
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
			s, _ := store.NewStore()
			defer s.DB.Close()

			results, err := s.SearchFTS(args[0], 10)
			if err != nil {
				log.Fatal(err)
			}

			for _, r := range results {
				fmt.Printf("[%s] %s\n", r.Filepath, r.Title)
				fmt.Printf("   %s\n\n", strings.ReplaceAll(r.Snippet, "\n", " "))
			}
		},
	}

	// --- Vector Search ---
	var cmdVSearch = &cobra.Command{
		Use:   "vsearch [query]",
		Short: "Vector semantic search",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			s, _ := store.NewStore()
			defer s.DB.Close()
			client := llm.NewClient(ollamaURL, modelName)

			fmt.Println("Embedding query...")
			qVec, err := client.Embed(args[0], true)
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
			s, err := store.NewStore()
			if err != nil {
				log.Fatalf("Failed to initialize store: %v", err)
			}
			defer s.DB.Close()

			client := llm.NewClient(ollamaURL, modelName)

			mcpSrv := mcpserver.NewServer(s, client)

			// Use stderr for logs so stdout remains clean for JSON-RPC
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

func reindex(colName, rootPath string) {
	s, err := store.NewStore()
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
			// Normalize path separators to forward slash
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
