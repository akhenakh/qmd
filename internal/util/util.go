package util

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"regexp"
	"strings"
)

func HashContent(content string) string {
	hash := sha256.Sum256([]byte(content))
	return hex.EncodeToString(hash[:])
}

func ExtractTitle(content, filename string) string {
	// Look for first H1
	re := regexp.MustCompile(`(?m)^#\s+(.+)$`)
	match := re.FindStringSubmatch(content)
	if len(match) > 1 {
		return strings.TrimSpace(match[1])
	}
	// Look for H2
	re2 := regexp.MustCompile(`(?m)^##\s+(.+)$`)
	match2 := re2.FindStringSubmatch(content)
	if len(match2) > 1 {
		return strings.TrimSpace(match2[1])
	}

	base := filepath.Base(filename)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// Naive chunking for demonstration (character based)
func ChunkText(text string, chunkSize int) []string {
	var chunks []string
	runes := []rune(text)
	for i := 0; i < len(runes); i += chunkSize {
		end := i + chunkSize
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[i:end]))
	}
	return chunks
}
