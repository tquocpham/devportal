package mcp

import (
	"context"
	"fmt"
	"net/http"

	"github.com/devportal/api/lib/users"
	"github.com/golang-jwt/jwt/v5"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
)

// NewTokenVerifier builds the sdkauth.TokenVerifier that sdkauth.RequireBearerToken
// calls to authenticate MCP clients. Unlike mw.RequireAuth's session cookie
// check, MCP tokens (minted by POST /api/v1/me/mcp-token) are long-lived, so
// every call also re-checks userStore.Lookup: if an admin revokes someone's
// platform access, their MCP access dies on their very next call instead of
// waiting for the token's natural expiry (docs/phase-4-mcp-server-plan.md).
func NewTokenVerifier(jwtSecret string, userStore *users.Store) sdkauth.TokenVerifier {
	secret := []byte(jwtSecret)
	return func(ctx context.Context, token string, req *http.Request) (*sdkauth.TokenInfo, error) {
		parsed, err := jwt.Parse(token, func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method")
			}
			return secret, nil
		})
		if err != nil || !parsed.Valid {
			return nil, fmt.Errorf("%w: invalid or expired token", sdkauth.ErrInvalidToken)
		}

		claims, ok := parsed.Claims.(jwt.MapClaims)
		if !ok {
			return nil, fmt.Errorf("%w: malformed claims", sdkauth.ErrInvalidToken)
		}
		username, _ := claims["sub"].(string)
		if username == "" {
			return nil, fmt.Errorf("%w: missing subject", sdkauth.ErrInvalidToken)
		}

		allowed, _, err := userStore.Lookup(username)
		if err != nil {
			return nil, err
		}
		if !allowed {
			return nil, fmt.Errorf("%w: user no longer allowed", sdkauth.ErrInvalidToken)
		}

		info := &sdkauth.TokenInfo{UserID: username}
		if exp, err := parsed.Claims.GetExpirationTime(); err == nil && exp != nil {
			info.Expiration = exp.Time
		}
		return info, nil
	}
}
