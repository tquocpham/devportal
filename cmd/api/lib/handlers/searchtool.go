package handlers

import (
	"strconv"
	"strings"

	"github.com/devportal/retrieval"
)

// SearchCodebase embeds query, searches store for the topK closest chunks,
// and formats them as the text block an LLM tool result expects. Shared by
// the web chat's search_codebase tool (Chat, below) and the MCP server's
// identically named tool (lib/mcp/search.go), so there's exactly one
// implementation of "search the index and format results," not two that can
// drift apart.
func SearchCodebase(embedder retrieval.Embedder, store *retrieval.Store, query string, topK int) (string, []retrieval.Chunk, error) {
	vec, _, err := embedder.EmbedQuery(query)
	if err != nil {
		return "", nil, err
	}
	chunks, err := store.Search(vec, topK)
	if err != nil {
		return "", nil, err
	}

	var b strings.Builder
	if len(chunks) == 0 {
		b.WriteString("No results.")
	}
	for _, ch := range chunks {
		b.WriteString(ch.RelPath)
		b.WriteString(":")
		b.WriteString(strconv.Itoa(ch.StartLine))
		b.WriteString("-")
		b.WriteString(strconv.Itoa(ch.EndLine))
		b.WriteString("\n```\n")
		b.WriteString(ch.Content)
		b.WriteString("\n```\n\n")
	}

	return b.String(), chunks, nil
}
