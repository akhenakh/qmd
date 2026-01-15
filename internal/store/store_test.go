package store_test

import (
	"os"
	"testing"

	"github.com/akhenakh/qmd/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTestEnv configures a temporary cache directory so NewStore
// creates an isolated DB for testing purposes.
func setupTestEnv(t *testing.T) (*store.Store, func()) {
	// Create a temporary directory
	tempDir, err := os.MkdirTemp("", "qmd_test_*")
	require.NoError(t, err)

	// Mock UserCacheDir by setting XDG_CACHE_HOME (Linux/Mac) and LocalAppData (Windows)
	originalCacheEnv := os.Getenv("XDG_CACHE_HOME")
	originalWinEnv := os.Getenv("LocalAppData")

	os.Setenv("XDG_CACHE_HOME", tempDir)
	os.Setenv("LocalAppData", tempDir)

	s, err := store.NewStore()
	require.NoError(t, err)

	cleanup := func() {
		s.DB.Close()
		os.RemoveAll(tempDir)
		// Restore env
		os.Setenv("XDG_CACHE_HOME", originalCacheEnv)
		os.Setenv("LocalAppData", originalWinEnv)
	}

	return s, cleanup
}

// TestFTS_InternalState verifies that the Triggers are correctly firing
// and populating the FTS table.
func TestFTS_InternalState(t *testing.T) {
	s, cleanup := setupTestEnv(t)
	defer cleanup()

	content := "Search test content"
	err := s.IndexDocument("debug", "debug.md", content)
	require.NoError(t, err)

	// 1. Check documents table
	var count int
	err = s.DB.QueryRow("SELECT COUNT(*) FROM documents").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "documents table should have 1 row")

	// 2. Check FTS table
	var ftsCount int
	err = s.DB.QueryRow("SELECT COUNT(*) FROM documents_fts").Scan(&ftsCount)
	require.NoError(t, err)

	if ftsCount == 0 {
		t.Fatal("documents_fts is empty! The SQLite Triggers failed to populate the FTS table.")
	}

	// 3. Check FTS content
	var body string
	err = s.DB.QueryRow("SELECT body FROM documents_fts LIMIT 1").Scan(&body)
	require.NoError(t, err)
	assert.Equal(t, content, body, "FTS body should match inserted content")
}

func TestSearchFTS_Basic(t *testing.T) {
	s, cleanup := setupTestEnv(t)
	defer cleanup()

	// Insert a document
	content := `
# Project Alpha
We are discussing the architecture of Project Alpha.
`
	err := s.IndexDocument("work", "alpha.md", content)
	require.NoError(t, err)

	// Test: Search for a word in the body
	results, err := s.SearchFTS("architecture", 10, 1, false)
	require.NoError(t, err)

	if len(results) == 0 {
		// Dump info if failed
		t.Log("Search returned 0 results. Dumping table state:")
		rows, _ := s.DB.Query("SELECT title, body FROM documents_fts")
		for rows.Next() {
			var ti, bo string
			rows.Scan(&ti, &bo)
			t.Logf("Row: Title='%s' Body='%s'", ti, bo)
		}
		t.FailNow()
	}

	assert.Equal(t, "work/alpha.md", results[0].Filepath)
}

func TestIndexAndGetDocument(t *testing.T) {
	s, cleanup := setupTestEnv(t)
	defer cleanup()

	col := "notes"
	path := "test.md"
	content := "# My Title\nThis is a test content body."

	err := s.IndexDocument(col, path, content)
	assert.NoError(t, err)

	retrieved, err := s.GetDocument(col, path)
	assert.NoError(t, err)
	assert.Equal(t, content, retrieved)
}

func TestUpdateDocument(t *testing.T) {
	s, cleanup := setupTestEnv(t)
	defer cleanup()

	// 1. Create initial doc
	err := s.IndexDocument("main", "update.md", "This is the initial version.")
	require.NoError(t, err)

	// Verify initial search
	res, _ := s.SearchFTS("initial", 10, 0, false)
	require.Len(t, res, 1)

	// 2. Update doc
	err = s.IndexDocument("main", "update.md", "This is the updated version.")
	require.NoError(t, err)

	// 3. Search for OLD term (should fail)
	res, _ = s.SearchFTS("initial", 10, 0, false)
	assert.Len(t, res, 0, "Old content should be removed from FTS index")

	// 4. Search for NEW term (should succeed)
	res, _ = s.SearchFTS("updated", 10, 0, false)
	assert.Len(t, res, 1, "New content should be present in FTS index")
}

func TestVectors(t *testing.T) {
	s, cleanup := setupTestEnv(t)
	defer cleanup()

	content := "Vector test content"
	// Index normally to get hash/content
	err := s.IndexDocument("vec", "vec.md", content)
	require.NoError(t, err)

	// Get hash
	pending, err := s.GetPendingEmbeddings()
	require.NoError(t, err)
	require.Len(t, pending, 1)

	var hash string
	for h := range pending {
		hash = h
	}

	// Save embedding
	vec := make([]float32, 768)
	vec[0] = 0.5
	vec[1] = 0.5
	err = s.SaveEmbedding(hash, 0, vec)
	assert.NoError(t, err)

	// Search Vector
	results, err := s.SearchVec(vec, 5)
	assert.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "vec/vec.md", results[0].Filepath)
}
