package main

import (
	"os"
	"strings"

	"github.com/devportal/retrieval"
)

type Chunk = retrieval.Chunk

const (
	maxChunkLines = 80 // max lines per chunk
	overlapLines  = 10 // lines of overlap between chunks for context continuity
)

// ChunkFile reads a file and splits it into overlapping chunks.
// For C++ we try to split on function/class boundaries first,
// then fall back to line-based chunking if the file is simple.
func ChunkFile(entry FileEntry) ([]Chunk, error) {
	data, err := os.ReadFile(entry.Path)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(data), "\n")

	// Try semantic splitting for C++ files
	ext := strings.ToLower(entry.Path[strings.LastIndex(entry.Path, "."):])
	if ext == ".cpp" || ext == ".h" {
		chunks := splitOnBoundaries(lines, entry)
		if len(chunks) > 0 {
			return chunks, nil
		}
	}

	// Fall back to sliding window chunking
	return slideWindow(lines, entry), nil
}

// splitOnBoundaries tries to chunk at function/class definitions
func splitOnBoundaries(lines []string, entry FileEntry) []Chunk {
	var chunks []Chunk
	var current []string
	startLine := 0

	flush := func(end int) {
		if len(current) == 0 {
			return
		}
		chunks = append(chunks, Chunk{
			FilePath:  entry.Path,
			RelPath:   entry.RelPath,
			Content:   strings.Join(current, "\n"),
			StartLine: startLine + 1,
			EndLine:   end,
		})
		current = nil
	}

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Detect function/class boundaries
		isBoundary := strings.HasPrefix(trimmed, "void ") ||
			strings.HasPrefix(trimmed, "bool ") ||
			strings.HasPrefix(trimmed, "int ") ||
			strings.HasPrefix(trimmed, "float ") ||
			strings.HasPrefix(trimmed, "class ") ||
			strings.HasPrefix(trimmed, "struct ") ||
			strings.HasPrefix(trimmed, "UFUNCTION") ||
			strings.HasPrefix(trimmed, "UCLASS") ||
			strings.HasPrefix(trimmed, "UPROPERTY")

		if isBoundary && len(current) > overlapLines {
			flush(i)
			startLine = i
			// Keep overlap from previous chunk
			if len(lines) > i-overlapLines && i-overlapLines > 0 {
				current = append(current, lines[i-overlapLines:i]...)
			}
		}

		current = append(current, line)

		// Force flush if chunk is getting too long
		if len(current) >= maxChunkLines {
			flush(i + 1)
			startLine = i + 1
		}
	}

	flush(len(lines))
	return chunks
}

// slideWindow is a simple fallback chunker
func slideWindow(lines []string, entry FileEntry) []Chunk {
	var chunks []Chunk
	total := len(lines)

	for start := 0; start < total; start += maxChunkLines - overlapLines {
		end := start + maxChunkLines
		if end > total {
			end = total
		}

		chunks = append(chunks, Chunk{
			FilePath:  entry.Path,
			RelPath:   entry.RelPath,
			Content:   strings.Join(lines[start:end], "\n"),
			StartLine: start + 1,
			EndLine:   end,
		})

		if end == total {
			break
		}
	}

	return chunks
}
