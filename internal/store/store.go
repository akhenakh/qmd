package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/akhenakh/qmd/internal/config"
	"github.com/akhenakh/qmd/internal/util"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"
)

type Store struct {
	DB     *sql.DB
	DBPath string
}

func NewStore(dbPath string) (*Store, error) {
	sqlite_vec.Auto() // Load sqlite-vec extension

	// Ensure directory exists if path has one
	dir := filepath.Dir(dbPath)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, err
		}
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}

	// WAL mode for concurrency
	if _, err := db.Exec("PRAGMA journal_mode = WAL; PRAGMA foreign_keys = ON;"); err != nil {
		db.Close()
		return nil, err
	}

	s := &Store{DB: db, DBPath: dbPath}
	if err := s.initBasicSchema(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) initBasicSchema() error {
	queries := []string{
		// Metadata tables
		`CREATE TABLE IF NOT EXISTS config (
			key TEXT PRIMARY KEY,
			value TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS collections (
			path TEXT PRIMARY KEY,
			name TEXT,
			pattern TEXT,
			exclude TEXT, 
			context TEXT
		)`,
		// Content tables
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
		`CREATE VIRTUAL TABLE IF NOT EXISTS documents_fts USING fts5(
			filepath, title, body,
			tokenize='porter unicode61'
		)`,
		// Triggers
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
		// Vector mapping table (vector table created separately based on dim)
		`CREATE TABLE IF NOT EXISTS content_vectors (
			hash TEXT NOT NULL,
			seq INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (hash, seq)
		)`,
	}

	for _, q := range queries {
		if _, err := s.DB.Exec(q); err != nil {
			// Handle schema migration for existing DBs simply by ignoring error or checking column
			// Ideally proper migration, but for this snippet we assume "if not exists" handles creation
			// To support adding column to existing table:
			if strings.Contains(q, "collections") {
				s.DB.Exec("ALTER TABLE collections ADD COLUMN exclude TEXT")
			}
		}
	}
	return nil
}

func (s *Store) EnsureVectorTable(dim int) error {
	query := fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS vectors_vec USING vec0(
		hash_seq TEXT PRIMARY KEY,
		embedding float[%d] distance_metric=cosine
	)`, dim)

	_, err := s.DB.Exec(query)
	return err
}

func (s *Store) LoadConfig() (*config.Config, error) {
	cfg := config.Default()

	// Load Key-Values
	rows, err := s.DB.Query("SELECT key, value FROM config")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	kv := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err == nil {
			kv[k] = v
		}
	}

	if v, ok := kv["ollama_url"]; ok {
		cfg.OllamaURL = v
	}
	if v, ok := kv["model_name"]; ok {
		cfg.ModelName = v
	}
	if v, ok := kv["embed_dimensions"]; ok {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.EmbedDimensions = i
		}
	}
	if v, ok := kv["use_local"]; ok {
		cfg.UseLocal = (v == "true")
	}
	if v, ok := kv["local_model_path"]; ok {
		cfg.LocalModelPath = v
	}
	if v, ok := kv["local_lib_path"]; ok {
		cfg.LocalLibPath = v
	}
	if v, ok := kv["chunk_size"]; ok {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.ChunkSize = i
		}
	}
	if v, ok := kv["chunk_overlap"]; ok {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.ChunkOverlap = i
		}
	}
	if v, ok := kv["embeddings_configured"]; ok {
		cfg.EmbeddingsConfigured = (v == "true")
	}

	// Load Collections
	// We handle the case where exclude column might not exist in older DBs
	// via basic query logic or explicit selection
	cRows, err := s.DB.Query("SELECT path, name, pattern, exclude, context FROM collections")
	if err != nil {
		// Fallback for older schema if migration didn't run via init
		cRows, err = s.DB.Query("SELECT path, name, pattern, '', context FROM collections")
		if err != nil {
			return cfg, nil
		}
	}
	defer cRows.Close()

	for cRows.Next() {
		var path, name, pattern, excludeJSON, contextJSON string
		if err := cRows.Scan(&path, &name, &pattern, &excludeJSON, &contextJSON); err == nil {
			c := config.Collection{
				Path:    path,
				Name:    name,
				Pattern: pattern,
				Context: make(map[string]string),
				Exclude: make([]string, 0),
			}
			if contextJSON != "" {
				json.Unmarshal([]byte(contextJSON), &c.Context)
			}
			if excludeJSON != "" {
				json.Unmarshal([]byte(excludeJSON), &c.Exclude)
			}
			cfg.Collections = append(cfg.Collections, c)
		}
	}

	return cfg, nil
}

func (s *Store) SaveConfig(cfg *config.Config) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	upsert := func(k, v string) error {
		_, err := tx.Exec("INSERT OR REPLACE INTO config (key, value) VALUES (?, ?)", k, v)
		return err
	}

	// Only save embedding parameters if they have been configured (i.e. 'qmd embed' run)
	if cfg.EmbeddingsConfigured {
		if err := upsert("ollama_url", cfg.OllamaURL); err != nil {
			return err
		}
		if err := upsert("model_name", cfg.ModelName); err != nil {
			return err
		}
		if err := upsert("embed_dimensions", strconv.Itoa(cfg.EmbedDimensions)); err != nil {
			return err
		}
		if err := upsert("use_local", fmt.Sprintf("%v", cfg.UseLocal)); err != nil {
			return err
		}
		if err := upsert("local_model_path", cfg.LocalModelPath); err != nil {
			return err
		}
		if err := upsert("local_lib_path", cfg.LocalLibPath); err != nil {
			return err
		}
		if err := upsert("chunk_size", strconv.Itoa(cfg.ChunkSize)); err != nil {
			return err
		}
		if err := upsert("chunk_overlap", strconv.Itoa(cfg.ChunkOverlap)); err != nil {
			return err
		}
	}

	// Always save the configuration state flag
	if err := upsert("embeddings_configured", fmt.Sprintf("%v", cfg.EmbeddingsConfigured)); err != nil {
		return err
	}

	// Overwrite collections
	if _, err := tx.Exec("DELETE FROM collections"); err != nil {
		return err
	}

	for _, c := range cfg.Collections {
		ctxBytes, _ := json.Marshal(c.Context)
		excBytes, _ := json.Marshal(c.Exclude)
		_, err := tx.Exec("INSERT INTO collections (path, name, pattern, exclude, context) VALUES (?, ?, ?, ?, ?)",
			c.Path, c.Name, c.Pattern, string(excBytes), string(ctxBytes))
		if err != nil {
			return err
		}
	}

	return tx.Commit()
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
	query = strings.TrimSpace(query)
	cleanQuery := strings.ReplaceAll(query, "\"", "")
	ftsQuery := fmt.Sprintf(`"%s"`, cleanQuery)

	rows, err := s.DB.Query(`
		SELECT 
			rowid, 
			filepath, 
			title, 
			body,
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

		if err := rows.Scan(&r.DocID, &r.Filepath, &r.Title, &body, &r.Score); err != nil {
			return nil, err
		}

		offsets := extractOffsetsFromBody(body, query)
		r.Matches = extractMatches(body, offsets, contextLines, findAll)

		if len(r.Matches) == 0 {
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

func extractOffsetsFromBody(body string, query string) string {
	cleanQuery := strings.TrimSpace(strings.ToLower(query))
	if cleanQuery == "" {
		return ""
	}

	bodyLower := strings.ToLower(body)
	queryLower := strings.ToLower(cleanQuery)

	var offsetsParts []string
	idx := 0

	for {
		pos := strings.Index(bodyLower[idx:], queryLower)
		if pos == -1 {
			break
		}

		actualPos := idx + pos
		// colNum=2 (body), termNum=0, byteOffset=actualPos, size=len(query)
		offsetsParts = append(offsetsParts,
			fmt.Sprintf("2 0 %d %d", actualPos, len(query)))

		idx = actualPos + 1
	}

	return strings.Join(offsetsParts, " ")
}

func extractMatches(body string, offsetsStr string, n int, findAll bool) []string {
	parts := strings.Fields(offsetsStr)
	type match struct {
		start int
		end   int
	}
	var matches []match

	for i := 0; i < len(parts); i += 4 {
		col, _ := strconv.Atoi(parts[i])
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
	lineOffsets := make([]int, len(lines)+1)
	currentOffset := 0
	for i, line := range lines {
		lineOffsets[i] = currentOffset
		currentOffset += len(line) + 1
	}
	lineOffsets[len(lines)] = currentOffset

	var results []string

	for _, m := range matches {
		lineIdx := 0
		for i := 0; i < len(lines); i++ {
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

type PendingDoc struct {
	Body  string
	Title string
}

func (s *Store) GetPendingEmbeddings() (map[string]PendingDoc, error) {
	// Join with documents table to get the Title
	rows, err := s.DB.Query(`
        SELECT DISTINCT d.hash, d.title, c.doc
        FROM documents d
        JOIN content c ON d.hash = c.hash
        LEFT JOIN content_vectors cv ON d.hash = cv.hash
        WHERE cv.hash IS NULL
    `)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	res := make(map[string]PendingDoc)
	for rows.Next() {
		var hash, title, body string
		if err := rows.Scan(&hash, &title, &body); err != nil {
			return nil, err
		}
		res[hash] = PendingDoc{Body: body, Title: title}
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

	// Safely check if vectors_vec exists before counting
	var exists int
	err = s.DB.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='vectors_vec'").Scan(&exists)
	if err != nil {
		return nil, err
	}

	if exists == 1 {
		err = s.DB.QueryRow("SELECT COUNT(*) FROM vectors_vec").Scan(&stats.Embeddings)
		if err != nil {
			return nil, err
		}
	} else {
		stats.Embeddings = 0
	}
	return stats, nil
}

// SearchHybrid performs both FTS and Vector search and combines them using RRF.
// It fetches more candidates (limit * 2) from each source to ensure good intersection.
func (s *Store) SearchHybrid(textQuery string, queryVec []float32, limit int) ([]SearchResult, error) {
	// Run searches in parallel (mocked here by sequential for simplicity, or use goroutines)
	// We ask for more results (2x limit) from individual engines to improve fusion quality
	candidateLimit := limit * 2

	// FTS Search
	// We request 1 line of context by default for snippets
	ftsResults, err := s.SearchFTS(textQuery, candidateLimit, 1, false)
	if err != nil {
		return nil, fmt.Errorf("FTS search failed: %w", err)
	}

	// Vector Search
	vecResults, err := s.SearchVec(queryVec, candidateLimit)
	if err != nil {
		return nil, fmt.Errorf("vector search failed: %w", err)
	}

	// Fuse Results
	fused := ReciprocalRankFusion(ftsResults, vecResults)

	// Apply final limit
	if len(fused) > limit {
		fused = fused[:limit]
	}

	return fused, nil
}
