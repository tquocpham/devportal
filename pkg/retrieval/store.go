package retrieval

import (
	"database/sql"
	"fmt"

	_ "github.com/lib/pq"
)

var migrateSQL = fmt.Sprintf(`
	CREATE EXTENSION IF NOT EXISTS vector;

	CREATE TABLE IF NOT EXISTS code_chunks (
		id          SERIAL PRIMARY KEY,
		file_path   TEXT NOT NULL,
		rel_path    TEXT NOT NULL,
		start_line  INT,
		end_line    INT,
		content     TEXT NOT NULL,
		embedding   vector(%d),
		indexed_at  TIMESTAMP DEFAULT NOW()
	);

	-- No ANN index on embedding: an IVFFlat index needs roughly
	-- rows-per-list in the thousands to give good recall, and at a
	-- single-codebase scale (hundreds to low thousands of chunks) an
	-- approximate index costs real recall for no measurable speed win —
	-- exact search (sequential scan) is both simpler and correct. Revisit
	-- if this ever grows past ~100k chunks.
	DROP INDEX IF EXISTS code_chunks_embedding_idx;

	-- For fast deletion by file when re-indexing
	CREATE INDEX IF NOT EXISTS code_chunks_rel_path_idx
		ON code_chunks (rel_path);
`, EmbeddingDim)

type Store struct {
	db *sql.DB
}

func NewStore(databaseURL string) (*Store, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("cannot connect to database: %w", err)
	}
	return &Store{db: db}, nil
}

// Migrate creates the table and vector index if they don't exist.
//
// NOTE: `vector(N)` in the schema is fixed at whatever EmbeddingDim was
// when the table was first created (CREATE TABLE IF NOT EXISTS won't
// alter an existing table's column type). If you change embedding
// providers/dimensions after the table already exists, drop
// `code_chunks` first and re-run a full index.
func (s *Store) Migrate() error {
	_, err := s.db.Exec(migrateSQL)
	return err
}

// CheckReady fails fast if code_chunks hasn't been created yet — used by
// cmd/api at startup so a service pointed at an un-migrated/un-indexed
// database fails loudly instead of erroring on the first chat request.
func (s *Store) CheckReady() error {
	var dummy int
	err := s.db.QueryRow(`SELECT 1 FROM code_chunks LIMIT 1`).Scan(&dummy)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	return nil
}

// DeleteFile removes all chunks for a given file (before re-indexing it)
func (s *Store) DeleteFile(relPath string) error {
	_, err := s.db.Exec(`DELETE FROM code_chunks WHERE rel_path = $1`, relPath)
	return err
}

// DeleteAll clears the entire index (for full re-index)
func (s *Store) DeleteAll() error {
	_, err := s.db.Exec(`DELETE FROM code_chunks`)
	return err
}

// Insert stores a chunk and its embedding
func (s *Store) Insert(chunk Chunk, embedding []float32) error {
	// Convert float32 slice to postgres vector string: '[0.1,0.2,...]'
	vec := float32SliceToVector(embedding)

	_, err := s.db.Exec(`
		INSERT INTO code_chunks (file_path, rel_path, start_line, end_line, content, embedding)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, chunk.FilePath, chunk.RelPath, chunk.StartLine, chunk.EndLine, chunk.Content, vec)

	return err
}

// Search finds the top-k most relevant chunks for a query embedding
func (s *Store) Search(queryEmbedding []float32, topK int) ([]Chunk, error) {
	vec := float32SliceToVector(queryEmbedding)

	rows, err := s.db.Query(`
		SELECT file_path, rel_path, start_line, end_line, content
		FROM code_chunks
		ORDER BY embedding <=> $1
		LIMIT $2
	`, vec, topK)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Chunk
	for rows.Next() {
		var c Chunk
		if err := rows.Scan(&c.FilePath, &c.RelPath, &c.StartLine, &c.EndLine, &c.Content); err != nil {
			return nil, err
		}
		results = append(results, c)
	}

	return results, nil
}

func float32SliceToVector(v []float32) string {
	s := "["
	for i, f := range v {
		if i > 0 {
			s += ","
		}
		s += fmt.Sprintf("%f", f)
	}
	s += "]"
	return s
}
