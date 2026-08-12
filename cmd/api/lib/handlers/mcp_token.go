package handlers

import (
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
)

type MCPTokenHandler struct {
	jwtSecret []byte
	duration  time.Duration
}

func NewMCPTokenHandler(jwtSecret string, duration time.Duration) *MCPTokenHandler {
	return &MCPTokenHandler{jwtSecret: []byte(jwtSecret), duration: duration}
}

// MCPToken handles POST /api/v1/me/mcp-token. Mints a long-lived JWT so the
// caller can authenticate their own Claude Code session to /mcp, which has
// no session cookie to check. No role claim: /mcp doesn't gate by role, only
// by "still an allowed user," re-checked on every call by
// mcp.NewTokenVerifier's userStore.Lookup, not baked into the token itself.
func (h *MCPTokenHandler) MCPToken(c echo.Context) error {
	claims := c.Get("user").(jwt.MapClaims)
	username, _ := claims["sub"].(string)

	exp := time.Now().Add(h.duration)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": username,
		"exp": exp.Unix(),
	})
	signed, err := token.SignedString(h.jwtSecret)
	if err != nil {
		c.Logger().Errorf("sign mcp token: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to mint token")
	}

	return c.JSON(http.StatusOK, map[string]any{
		"token":     signed,
		"expiresAt": exp,
	})
}
