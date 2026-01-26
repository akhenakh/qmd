package ingest

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/akhenakh/qmd/internal/store"
	"github.com/klauspost/compress/zstd"
)

// ProcessZstdBundle reads a compressed file containing concatenated markdown code blocks
func ProcessZstdBundle(s *store.Store, archivePath string, collectionName string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	decoder, err := zstd.NewReader(f)
	if err != nil {
		return fmt.Errorf("failed to create zstd reader: %w", err)
	}
	defer decoder.Close()

	fmt.Printf("Indexing archive '%s' into collection '%s'...\n", filepath.Base(archivePath), collectionName)

	scanner := bufio.NewScanner(decoder)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024) // Increase buffer to 10MB just in case

	// Capture the fence (3 or more backticks) and the file path
	// Group 1: Backticks
	// Group 2: Path
	headerRegex := regexp.MustCompile("^(`{3,})\\s*markdown\\s+(.+)$")

	var (
		currentPath    string
		currentContent strings.Builder
		currentFence   string // The fence used to open the current block
		inBlock        bool
		count          int
	)

	for scanner.Scan() {
		line := scanner.Text()

		// Detect Header: ```markdown path/file.md OR ````markdown path/file.md
		if match := headerRegex.FindStringSubmatch(line); len(match) > 2 {
			// Save previous file if exists
			if inBlock && currentPath != "" {
				if err := saveDoc(s, collectionName, currentPath, currentContent.String()); err != nil {
					fmt.Printf("Error indexing %s: %v\n", currentPath, err)
				} else {
					count++
				}
			}

			// Start new file
			currentFence = match[1]
			currentPath = strings.TrimSpace(match[2])
			currentContent.Reset()
			inBlock = true
			continue
		}

		// Detect Footer: Must match the opening fence exactly (e.g. ``` or ````)
		trimLine := strings.TrimSpace(line)
		if inBlock && trimLine == currentFence {
			if currentPath != "" {
				if err := saveDoc(s, collectionName, currentPath, currentContent.String()); err != nil {
					fmt.Printf("Error indexing %s: %v\n", currentPath, err)
				} else {
					count++
				}
			}
			inBlock = false
			currentPath = ""
			currentContent.Reset()
			currentFence = ""
			continue
		}

		// Capture Content
		if inBlock {
			currentContent.WriteString(line)
			currentContent.WriteString("\n")
		}
	}

	// Save last file if EOF reached without closing fence
	if inBlock && currentPath != "" {
		if err := saveDoc(s, collectionName, currentPath, currentContent.String()); err != nil {
			fmt.Printf("Error indexing %s: %v\n", currentPath, err)
		} else {
			count++
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading archive: %w", err)
	}

	if count == 0 {
		fmt.Println("Warning: Archive processed but 0 documents found. Check if the format matches: ```markdown path/to/file.md")
	} else {
		fmt.Printf("Success: Indexed %d documents from archive.\n", count)
	}

	return nil
}

func saveDoc(s *store.Store, col, path, content string) error {
	return s.IndexDocument(col, path, content)
}
