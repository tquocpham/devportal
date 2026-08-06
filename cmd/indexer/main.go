package main

import (
	"flag"
	"fmt"
	"log"
	"time"
)

func main() {
	// CLI flags
	full := flag.Bool("full", false, "Full re-index of entire codebase")
	changed := flag.Bool("changed", false, "Index only files changed since last git commit")
	flag.Parse()

	if !*full && !*changed {
		fmt.Println("Usage:")
		fmt.Println("  indexer -full       # index entire codebase")
		fmt.Println("  indexer -changed    # index only files changed since last commit")
		return
	}

	cfg := LoadConfig()

	// Connect to Supabase
	store, err := NewStore(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("DB connection failed: %v", err)
	}
	log.Println("Connected to database")

	if err := store.Migrate(); err != nil {
		log.Fatalf("Migration failed: %v", err)
	}

	embedder := NewEmbedder(cfg.AnthropicKey)

	// Get files to index
	var files []FileEntry
	if *full {
		log.Println("Mode: FULL re-index")
		if err := store.DeleteAll(); err != nil {
			log.Fatalf("Failed to clear index: %v", err)
		}
		files, err = WalkAll(cfg)
	} else {
		log.Println("Mode: INCREMENTAL (changed files only)")
		files, err = WalkChanged(cfg)
	}

	if err != nil {
		log.Fatalf("Failed to walk codebase: %v", err)
	}

	log.Printf("Found %d files to index", len(files))

	// Index each file
	totalChunks := 0
	start := time.Now()

	for i, file := range files {
		log.Printf("[%d/%d] Indexing %s", i+1, len(files), file.RelPath)

		// Remove old chunks for this file (incremental mode)
		if *changed {
			if err := store.DeleteFile(file.RelPath); err != nil {
				log.Printf("  Warning: could not delete old chunks: %v", err)
			}
		}

		// Chunk the file
		chunks, err := ChunkFile(file)
		if err != nil {
			log.Printf("  Skipping (read error): %v", err)
			continue
		}

		// Embed and store each chunk
		for _, chunk := range chunks {
			embedding, err := embedder.Embed(chunk.Content)
			if err != nil {
				log.Printf("  Embedding failed for chunk at line %d: %v", chunk.StartLine, err)
				continue
			}

			if err := store.Insert(chunk, embedding); err != nil {
				log.Printf("  Store failed for chunk at line %d: %v", chunk.StartLine, err)
				continue
			}

			totalChunks++
		}

		// Small delay to avoid hammering the embedding API
		time.Sleep(50 * time.Millisecond)
	}

	elapsed := time.Since(start)
	log.Printf("Done. Indexed %d chunks from %d files in %s", totalChunks, len(files), elapsed.Round(time.Second))
}
