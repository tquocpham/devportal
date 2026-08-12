package handlers

import (
	"net/http"

	"github.com/devportal/api/lib/repos"
	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
)

type ReposHandler struct {
	store *repos.Store
}

func NewReposHandler(store *repos.Store) *ReposHandler {
	return &ReposHandler{store: store}
}

// List handles GET /api/v1/repos: the repos the logged-in user has
// specifically been granted, not the full catalog, that's
// AdminReposHandler.List. Always returns a JSON array, [] rather than null
// when the user has no grants, so the frontend never needs a null-check
// before iterating.
func (h *ReposHandler) List(c echo.Context) error {
	claims := c.Get("user").(jwt.MapClaims)
	username, _ := claims["sub"].(string)

	list, err := h.store.ForUser(username)
	if err != nil {
		c.Logger().Errorf("list repos for %s: %v", username, err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to load repos")
	}
	if list == nil {
		list = []repos.Repo{}
	}
	return c.JSON(http.StatusOK, list)
}
