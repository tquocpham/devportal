package handlers

import (
	"net/http"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
)

func Me(c echo.Context) error {
	claims := c.Get("user").(jwt.MapClaims)
	cookie, err := c.Cookie("session")
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "not logged in")
	}
	return c.JSON(http.StatusOK, map[string]any{
		"id":       claims["id"],
		"username": claims["sub"],
		"name":     claims["name"],
		"email":    claims["email"],
		"avatar":   claims["avatar"],
		"orgs":     claims["orgs"],
		"jwt":      cookie.Value,
	})
}
