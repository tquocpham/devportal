package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/devportal/api/lib/users"
	"github.com/labstack/echo/v4"
)

// AdminUsersHandler backs /api/v1/admin/users
// Registered behind both RequireAuth and RequireAdmin in
// main.go, so every method here can assume the caller is
// a logged-in admin.
type AdminUsersHandler struct {
	store *users.Store
}

func NewAdminUsersHandler(store *users.Store) *AdminUsersHandler {
	return &AdminUsersHandler{store: store}
}

// List handles GET /api/v1/admin/users.
func (h *AdminUsersHandler) List(c echo.Context) error {
	list, err := h.store.List()
	if err != nil {
		c.Logger().Errorf("list users: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list users")
	}
	return c.JSON(http.StatusOK, list)
}

type addUserRequest struct {
	Username string     `json:"username"`
	Role     users.Role `json:"role"`
}

// Add handles POST /api/v1/admin/users. Role defaults to "developer" if
// omitted.
func (h *AdminUsersHandler) Add(c echo.Context) error {
	var req addUserRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}
	if strings.TrimSpace(req.Username) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "username is required")
	}
	if req.Role == "" {
		req.Role = users.RoleDeveloper
	}

	if err := h.store.Add(req.Username, req.Role); err != nil {
		return adminUsersError(err)
	}
	return c.JSON(http.StatusCreated, users.User{Username: req.Username, Role: req.Role})
}

type setRoleRequest struct {
	Role users.Role `json:"role"`
}

// SetRole handles PATCH /api/v1/admin/users/:username.
func (h *AdminUsersHandler) SetRole(c echo.Context) error {
	username := c.Param("username")
	var req setRoleRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}
	if err := h.store.SetRole(username, req.Role); err != nil {
		return adminUsersError(err)
	}
	return c.JSON(http.StatusOK, users.User{Username: username, Role: req.Role})
}

// Remove handles DELETE /api/v1/admin/users/:username.
func (h *AdminUsersHandler) Remove(c echo.Context) error {
	username := c.Param("username")
	if err := h.store.Remove(username); err != nil {
		return adminUsersError(err)
	}
	return c.NoContent(http.StatusNoContent)
}

func adminUsersError(err error) error {
	switch {
	case errors.Is(err, users.ErrUserExists):
		return echo.NewHTTPError(http.StatusConflict, err.Error())
	case errors.Is(err, users.ErrUserNotFound):
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	case errors.Is(err, users.ErrInvalidRole), errors.Is(err, users.ErrLastAdmin):
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	default:
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
}
