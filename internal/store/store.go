package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/akhenakh/qmd/internal/util"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"
)

type Store struct {
	DB *sql.DB
}

func NewStore() (*Store, error) {
	sqlite_vec.Auto() // Load sqlite-vec extension

	cacheDir, _ := os.UserCacheDir()
	// Allow overriding cache dir via env for testing
	if envCache := os.Getenv("XDG_CACHE_HOME"); envCache != "" {
		cacheDir = envCache
	}

	dbPath := filepath.Join(cacheDir, "qmd", "index.sqlite")

	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}

	// WAL mode for concurrency
	if _, err := db.Exec("PRAGMA journal_mode = WAL; PRAGMA foreign_keys = ON;"); err != nil {
		return nil, err
	}

	s := &Store{DB: db}
	if err := s.initSchema(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) initSchema() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS content (
			hash TEXT PRIMARY KEY,
			doc TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS documents (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			collection TEXT NOT NULL,
			path TEXT NOT NULL,
			title TEXT NOT NULL,
			hash TEXT NOT NULL,
			active INTEGER NOT NULL DEFAULT 1,
			modified_at TEXT NOT NULL,
			FOREIGN KEY (hash) REFERENCES content(hash) ON DELETE CASCADE,
			UNIQUE(collection, path)
		)`,
		// FTS5 Table
		// Note: tokenize='porter unicode61' is standard.
		// If Porter fails in some builds, 'unicode61' alone is a safe fallback.
		`CREATE VIRTUAL TABLE IF NOT EXISTS documents_fts USING fts5(
			filepath, title, body,
			tokenize='porter unicode61'
		)`,
		// Triggers for FTS sync
		`CREATE TRIGGER IF NOT EXISTS documents_ai AFTER INSERT ON documents
		 BEGIN
			INSERT INTO documents_fts(rowid, filepath, title, body)
			SELECT new.id, new.collection || '/' || new.path, new.title, 
			(SELECT doc FROM content WHERE hash = new.hash);
		 END`,
		`CREATE TRIGGER IF NOT EXISTS documents_ad AFTER DELETE ON documents
		 BEGIN
			DELETE FROM documents_fts WHERE rowid = old.id;
		 END`,
		`CREATE TRIGGER IF NOT EXISTS documents_au AFTER UPDATE ON documents
		 BEGIN
			DELETE FROM documents_fts WHERE rowid = old.id;
			INSERT INTO documents_fts(rowid, filepath, title, body)
			SELECT new.id, new.collection || '/' || new.path, new.title, 
			(SELECT doc FROM content WHERE hash = new.hash);
		 END`,
		// Vector Table
		`CREATE VIRTUAL TABLE IF NOT EXISTS vectors_vec USING vec0(
			hash_seq TEXT PRIMARY KEY,
			embedding float[768] distance_metric=cosine
		)`,
		`CREATE TABLE IF NOT EXISTS content_vectors (
			hash TEXT NOT NULL,
			seq INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (hash, seq)
		)`,
	}

	for _, q := range queries {
		if _, err := s.DB.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) IndexDocument(colName, path, content string) error {
	hash := util.HashContent(content)
	now := time.Now().Format(time.RFC3339)
	title := util.ExtractTitle(content, path)

	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(`INSERT OR IGNORE INTO content (hash, doc, created_at) VALUES (?, ?, ?)`, hash, content, now)
	if err != nil {
		return err
	}

	_, err = tx.Exec(`
		INSERT INTO documents (collection, path, title, hash, modified_at, active)
		VALUES (?, ?, ?, ?, ?, 1)
		ON CONFLICT(collection, path) DO UPDATE SET
			title=excluded.title,
			hash=excluded.hash,
			modified_at=excluded.modified_at,
			active=1
	`, colName, path, title, hash, now)
	if err != nil {
		return err
	}

	return tx.Commit()
}

type SearchResult struct {
	DocID    int64
	Filepath string
	Title    string
	Snippet  string
	Score    float64
	Matches  []string
}

func (s *Store) SearchFTS(query string, limit int, contextLines int, findAll bool) ([]SearchResult, error) {
	// Robust FTS5 query construction
	query = strings.TrimSpace(query)
	// Remove double quotes to prevent syntax errors in construction
	cleanQuery := strings.ReplaceAll(query, "\"", "")

	// Use Phrase Prefix search: "query"*
	// This works reliably with Porter stemmer for both complete words ("architecture")
	// and partial words ("archi"), as well as phrases ("project al").
	ftsQuery := fmt.Sprintf(`"%s"*`, cleanQuery)

	rows, err := s.DB.Query(`
		SELECT 
			rowid, 
			filepath, 
			title, 
			body,
			offsets(documents_fts),
			bm25(documents_fts) as rank
		FROM documents_fts 
		WHERE documents_fts MATCH ? 
		ORDER BY rank 
		LIMIT ?`, ftsQuery, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var body string
		var offsets string

		if err := rows.Scan(&r.DocID, &r.Filepath, &r.Title, &body, &offsets, &r.Score); err != nil {
			return nil, err
		}

		r.Matches = extractMatches(body, offsets, contextLines, findAll)

		if len(r.Matches) == 0 {
			// Fallback snippet if matches were in title or path, not body
			// Or if context extraction failed for some reason
			if len(body) > 200 {
				r.Snippet = body[:200] + "..."
			} else {
				r.Snippet = body
			}
		} else {
			r.Snippet = r.Matches[0]
		}

		results = append(results, r)
	}
	return results, nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func extractMatches(body string, offsetsStr string, n int, findAll bool) []string {
	parts := strings.Fields(offsetsStr)
	// FTS5 offsets: colNum, termNum, byteOffset, size

	type match struct {
		start int
		end   int
	}
	var matches []match

	for i := 0; i < len(parts); i += 4 {
		col, _ := strconv.Atoi(parts[i])

		// 0=filepath, 1=title, 2=body
		if col != 2 {
			continue
		}

		offset, _ := strconv.Atoi(parts[i+2])
		size, _ := strconv.Atoi(parts[i+3])
		matches = append(matches, match{start: offset, end: offset + size})

		if !findAll && len(matches) > 0 {
			break
		}
	}

	if len(matches) == 0 {
		return nil
	}

	lines := strings.Split(body, "\n")

	// Pre-calculate line start byte offsets
	lineOffsets := make([]int, len(lines)+1)
	currentOffset := 0
	for i, line := range lines {
		lineOffsets[i] = currentOffset
		currentOffset += len(line) + 1 // +1 for newline
	}
	lineOffsets[len(lines)] = currentOffset

	var results []string

	for _, m := range matches {
		// Identify line number
		lineIdx := 0
		for i := 0; i < len(lines); i++ {
			// Check if match start is within this line's range
			if m.start >= lineOffsets[i] && m.start < lineOffsets[i+1] {
				lineIdx = i
				break
			}
		}

		startLine := max(lineIdx-n, 0)
		endLine := lineIdx + n
		if endLine >= len(lines) {
			endLine = len(lines) - 1
		}

		var sb strings.Builder
		for i := startLine; i <= endLine; i++ {
			prefix := "   "
			if i == lineIdx {
				prefix = "-> "
			}
			sb.WriteString(fmt.Sprintf("%s%s\n", prefix, lines[i]))
		}
		results = append(results, strings.TrimRight(sb.String(), "\n"))
	}

	return results
}

func (s *Store) SaveEmbedding(hash string, seq int, vec []float32) error {
	blob, err := sqlite_vec.SerializeFloat32(vec)
	if err != nil {
		return err
	}

	key := fmt.Sprintf("%s_%d", hash, seq)

	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(`INSERT OR IGNORE INTO content_vectors (hash, seq) VALUES (?, ?)`, hash, seq)
	if err != nil {
		return err
	}

	_, err = tx.Exec(`INSERT OR REPLACE INTO vectors_vec (hash_seq, embedding) VALUES (?, ?)`, key, blob)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (s *Store) SearchVec(queryVec []float32, limit int) ([]SearchResult, error) {
	queryBlob, err := sqlite_vec.SerializeFloat32(queryVec)
	if err != nil {
		return nil, err
	}

	rows, err := s.DB.Query(`
		SELECT
			v.distance,
			d.collection || '/' || d.path,
			d.title,
			substr(c.doc, 1, 200)
		FROM vectors_vec v
		JOIN content_vectors cv ON v.hash_seq = cv.hash || '_' || cv.seq
		JOIN documents d ON d.hash = cv.hash
		JOIN content c ON c.hash = d.hash
		WHERE v.embedding MATCH ?
		AND k = ?
		ORDER BY v.distance
	`, queryBlob, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.Score, &r.Filepath, &r.Title, &r.Snippet); err != nil {
			return nil, err
		}
		r.Score = 1.0 - r.Score
		results = append(results, r)
	}
	return results, nil
}

func (s *Store) GetPendingEmbeddings() (map[string]string, error) {
	rows, err := s.DB.Query(`
		SELECT DISTINCT d.hash, c.doc
		FROM documents d
		JOIN content c ON d.hash = c.hash
		LEFT JOIN content_vectors cv ON d.hash = cv.hash
		WHERE cv.hash IS NULL
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	res := make(map[string]string)
	for rows.Next() {
		var hash, body string
		if err := rows.Scan(&hash, &body); err != nil {
			return nil, err
		}
		res[hash] = body
	}
	return res, nil
}

func (s *Store) GetDocument(collection, path string) (string, error) {
	var content string
	err := s.DB.QueryRow(`
		SELECT c.doc 
		FROM documents d 
		JOIN content c ON d.hash = c.hash 
		WHERE d.collection = ? AND d.path = ?
	`, collection, path).Scan(&content)

	if err == sql.ErrNoRows {
		return "", fmt.Errorf("document not found: %s/%s", collection, path)
	}
	if err != nil {
		return "", err
	}
	return content, nil
}

type Stats struct {
	TotalDocuments int
	Collections    int
	Embeddings     int
}

func (s *Store) GetStats() (*Stats, error) {
	stats := &Stats{}

	err := s.DB.QueryRow("SELECT COUNT(*) FROM documents WHERE active=1").Scan(&stats.TotalDocuments)
	if err != nil {
		return nil, err
	}

	err = s.DB.QueryRow("SELECT COUNT(DISTINCT collection) FROM documents WHERE active=1").Scan(&stats.Collections)
	if err != nil {
		return nil, err
	}

	err = s.DB.QueryRow("SELECT COUNT(*) FROM vectors_vec").Scan(&stats.Embeddings)
	if err != nil {
		return nil, err
	}

	return stats, nil
}
