package middleware

import (
	"net/http"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
)

func RequireAuth(jwtSecret string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			cookie, err := c.Cookie("session")
			if err != nil {
				return echo.NewHTTPError(http.StatusUnauthorized, "not logged in")
			}

			token, err := jwt.Parse(cookie.Value, func(t *jwt.Token) (any, error) {
				if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, echo.NewHTTPError(http.StatusUnauthorized, "invalid token")
				}
				return []byte(jwtSecret), nil
			})
			if err != nil || !token.Valid {
				return echo.NewHTTPError(http.StatusUnauthorized, "invalid or expired session")
			}

			c.Set("user", token.Claims)
			return next(c)
		}
	}
}
