package mcp

import (
	"net/http"

	"github.com/devportal/api/lib/users"
	"github.com/devportal/retrieval"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewHandler builds the /mcp http.Handler: an MCP server (with
// search_codebase registered, see search.go) wrapped in
// sdkauth.RequireBearerToken, so every request must carry a valid
// "Authorization: Bearer <token>" minted by POST /api/v1/me/mcp-token.
func NewHandler(jwtSecret string, userStore *users.Store, embedder retrieval.Embedder, store *retrieval.Store, topK int) http.Handler {
	server := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    "devportal",
		Version: "0.1.0",
	}, nil)
	addSearchTool(server, embedder, store, topK)

	mcpHandler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server {
		return server
	}, nil)

	return sdkauth.RequireBearerToken(NewTokenVerifier(jwtSecret, userStore), nil)(mcpHandler)
}
