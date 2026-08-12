package mcp

import (
	"context"

	"github.com/devportal/api/lib/handlers"
	"github.com/devportal/retrieval"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// searchArgs is inferred into the tool's input schema by the SDK's
// jsonschema-go: the field is required because it has no "omitempty", and
// the jsonschema tag content is used directly as its description (a
// different tag grammar than toolrunner's invopop-based
// "description=..." convention in chat.go's searchInput).
type searchArgs struct {
	Query string `json:"query" jsonschema:"A focused search query, e.g. a filename, class name, or specific concept to look up in the indexed codebase."`
}

// addSearchTool registers search_codebase on server, calling the exact same
// retrieval logic as the web chat's identically named tool
// (handlers.SearchCodebase), so an MCP client and the web chat get the same
// answer for the same query.
func addSearchTool(server *sdkmcp.Server, embedder retrieval.Embedder, store *retrieval.Store, topK int) {
	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        "search_codebase",
		Description: "Search the indexed codebase (source + config files) for content relevant to a query. Returns the closest-matching chunks with their file paths and line ranges. Call this as many times as you need. e.g. search again with a different, more specific query if the first result doesn't settle the question.",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, args searchArgs) (*sdkmcp.CallToolResult, any, error) {
		text, _, err := handlers.SearchCodebase(embedder, store, args.Query, topK)
		if err != nil {
			return nil, nil, err
		}
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: text}},
		}, nil, nil
	})
}
