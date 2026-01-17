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

// IsExcluded checks if a given path matches any of the glob patterns.
// It returns true if excluded, and the matching pattern.
func IsExcluded(path string, excludePatterns []string) (bool, string) {
	if len(excludePatterns) == 0 {
		return false, ""
	}
	// Use ToSlash for consistent matching across OSes
	pathToCheck := filepath.ToSlash(path)
	baseName := filepath.Base(pathToCheck)

	for _, pattern := range excludePatterns {
		// Clean the pattern
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}

		// Match against the full relative path
		matched, err := filepath.Match(pattern, pathToCheck)
		if err != nil {
			// Ignore invalid patterns
			continue
		}
		if matched {
			return true, pattern
		}

		// Git behavior - if pattern contains no slash (e.g. "*.log" or "node_modules"),
		// it matches the file/dir name anywhere in the tree.
		if !strings.Contains(pattern, "/") {
			matchedBase, _ := filepath.Match(pattern, baseName)
			if matchedBase {
				return true, pattern
			}
		}

		// Handle patterns ending in slash (e.g. "dist/") by matching directory name
		if strings.HasSuffix(pattern, "/") {
			cleanPattern := strings.TrimSuffix(pattern, "/")
			if matched, _ := filepath.Match(cleanPattern, pathToCheck); matched {
				return true, pattern
			}
			if !strings.Contains(cleanPattern, "/") {
				if matchedBase, _ := filepath.Match(cleanPattern, baseName); matchedBase {
					return true, pattern
				}
			}
		}
	}
	return false, ""
}
