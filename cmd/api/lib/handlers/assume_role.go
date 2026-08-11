package handlers

import (
	"net/http"
	"time"

	"github.com/devportal/api/lib/users"
	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
)

// Duration is caller-requested (assumeRoleRequest.DurationSeconds) but
// bounded: short by default since this token is for poking at role
// boundaries during testing, not a real session, but a caller can ask for
// longer, up to assumeRoleMaxDuration.
const (
	assumeRoleDefaultDuration = 15 * time.Minute
	assumeRoleMinDuration     = 1 * time.Minute
	assumeRoleMaxDuration     = 24 * time.Hour
)

type AssumeRoleHandler struct {
	jwtSecret []byte
}

func NewAssumeRoleHandler(jwtSecret string) *AssumeRoleHandler {
	return &AssumeRoleHandler{jwtSecret: []byte(jwtSecret)}
}

type assumeRoleRequest struct {
	Role users.Role `json:"role"`
	// DurationSeconds is optional; omitted or 0 uses assumeRoleDefaultDuration.
	// Clamped to [assumeRoleMinDuration, assumeRoleMaxDuration] otherwise.
	DurationSeconds int `json:"durationSeconds"`
}

// AssumeRole handles POST /api/v1/admin/assume-role (admin-only). Mints a
// short-lived token for the CALLER'S OWN username under a different role,
// so an admin can see exactly what a developer session can and can't do.
// Does not touch the real "session" cookie — the admin's actual login is
// untouched the whole time, so there's no separate "switch back" step,
// just stop using the minted token.
func (h *AssumeRoleHandler) AssumeRole(c echo.Context) error {
	claims := c.Get("user").(jwt.MapClaims)
	username, _ := claims["sub"].(string)

	var req assumeRoleRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}
	if req.Role != users.RoleAdmin && req.Role != users.RoleDeveloper {
		return echo.NewHTTPError(http.StatusBadRequest, `role must be "admin" or "developer"`)
	}

	duration := assumeRoleDefaultDuration
	if req.DurationSeconds > 0 {
		duration = min(max(time.Duration(req.DurationSeconds)*time.Second, assumeRoleMinDuration), assumeRoleMaxDuration)
	}

	exp := time.Now().Add(duration)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":  username,
		"role": string(req.Role),
		"exp":  exp.Unix(),
	})
	signed, err := token.SignedString(h.jwtSecret)
	if err != nil {
		c.Logger().Errorf("sign assumed-role token: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to mint token")
	}

	return c.JSON(http.StatusOK, map[string]any{
		"token":     signed,
		"role":      req.Role,
		"expiresAt": exp,
	})
}
