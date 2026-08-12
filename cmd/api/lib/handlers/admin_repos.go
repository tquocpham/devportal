package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/devportal/api/lib/repos"
	"github.com/labstack/echo/v4"
)

// AdminReposHandler backs /api/v1/admin/repos and the repo-scoped routes
// nested under /api/v1/admin/users/:username/repos. Registered behind both
// RequireAuth and RequireAdmin in main.go, same as AdminUsersHandler.
type AdminReposHandler struct {
	store *repos.Store
}

func NewAdminReposHandler(store *repos.Store) *AdminReposHandler {
	return &AdminReposHandler{store: store}
}

// List handles GET /api/v1/admin/repos: the full catalog, not scoped to any
// user, this is what an admin picks from when granting access.
func (h *AdminReposHandler) List(c echo.Context) error {
	list, err := h.store.List()
	if err != nil {
		c.Logger().Errorf("list repo catalog: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list repos")
	}
	return c.JSON(http.StatusOK, list)
}

// ListForUser handles GET /api/v1/admin/users/:username/repos: which repos
// a specific user currently has, the admin-facing read side of
// Grant/Revoke below. Same underlying query as the self-service
// ReposHandler.List, just callable against any username, not only the
// caller's own.
func (h *AdminReposHandler) ListForUser(c echo.Context) error {
	username := c.Param("username")
	list, err := h.store.ForUser(username)
	if err != nil {
		c.Logger().Errorf("list repos for %s: %v", username, err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list repos")
	}
	if list == nil {
		list = []repos.Repo{}
	}
	return c.JSON(http.StatusOK, list)
}

type createRepoRequest struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Create handles POST /api/v1/admin/repos: adds a repo to the catalog.
// Doesn't grant anyone access to it, that's a separate Grant call.
func (h *AdminReposHandler) Create(c echo.Context) error {
	var req createRepoRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.URL) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "name and url are required")
	}

	repo, err := h.store.Create(req.Name, req.URL)
	if err != nil {
		if errors.Is(err, repos.ErrRepoExists) {
			return echo.NewHTTPError(http.StatusConflict, err.Error())
		}
		c.Logger().Errorf("create repo: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create repo")
	}
	return c.JSON(http.StatusCreated, repo)
}

// Delete handles DELETE /api/v1/admin/repos/:id: removes a repo from the
// catalog entirely, which cascades to remove every user's grant for it too
// (see the FK in 0002_create_repos.sql), not just this one admin's view.
func (h *AdminReposHandler) Delete(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid repo id")
	}
	if err := h.store.Delete(id); err != nil {
		if errors.Is(err, repos.ErrRepoNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, err.Error())
		}
		c.Logger().Errorf("delete repo %d: %v", id, err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete repo")
	}
	return c.NoContent(http.StatusNoContent)
}

type grantRepoRequest struct {
	RepoID int `json:"repoId"`
}

// Grant handles POST /api/v1/admin/users/:username/repos: gives the named
// user access to one repo from the catalog. Idempotent, granting something
// already granted just succeeds again.
func (h *AdminReposHandler) Grant(c echo.Context) error {
	username := c.Param("username")
	var req grantRepoRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}
	if req.RepoID <= 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "repoId is required")
	}

	if err := h.store.Grant(username, req.RepoID); err != nil {
		c.Logger().Errorf("grant repo %d to %s: %v", req.RepoID, username, err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to grant repo access")
	}
	return c.NoContent(http.StatusNoContent)
}

// Revoke handles DELETE /api/v1/admin/users/:username/repos/:repoId.
// Idempotent, revoking access the user never had just succeeds too.
func (h *AdminReposHandler) Revoke(c echo.Context) error {
	username := c.Param("username")
	repoID, err := strconv.Atoi(c.Param("repoId"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid repo id")
	}

	if err := h.store.Revoke(username, repoID); err != nil {
		c.Logger().Errorf("revoke repo %d from %s: %v", repoID, username, err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to revoke repo access")
	}
	return c.NoContent(http.StatusNoContent)
}
