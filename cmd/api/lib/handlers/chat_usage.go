package handlers

import (
	"net/http"
	"time"

	"github.com/devportal/api/lib/usage"
	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
)

// ChatUsageHandler backs GET /api/v1/me/chat-usage: the logged-in caller's
// own usage for the current billing period, no admin required. Same
// period/store as AdminChatUsageHandler, just scoped to one user instead of
// everyone, so people asking questions can see where they stand
// (docs/phase-4b-chat-usage-tracking-plan.md) without needing an admin to
// tell them.
type ChatUsageHandler struct {
	store              usage.Store
	billingPeriodStart int
}

func NewChatUsageHandler(store usage.Store, billingPeriodStart int) *ChatUsageHandler {
	return &ChatUsageHandler{
		store:              store,
		billingPeriodStart: billingPeriodStart,
	}
}

type meChatUsageResponse struct {
	PeriodStart time.Time `json:"periodStart"`
	PeriodEnd   time.Time `json:"periodEnd"`
	usage.UserSummary
}

// Me handles GET /api/v1/me/chat-usage.
func (h *ChatUsageHandler) Me(c echo.Context) error {
	claims, ok := c.Get("user").(jwt.MapClaims)
	if !ok {
		return echo.NewHTTPError(http.StatusUnauthorized, "not logged in")
	}
	username, _ := claims["sub"].(string)

	periodStart, periodEnd := usage.CurrentBillingPeriod(h.billingPeriodStart, time.Now())

	u, err := h.store.ForUser(username, periodStart, periodEnd)
	if err != nil {
		c.Logger().Errorf("chat usage for %s: %v", username, err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to load chat usage")
	}

	return c.JSON(http.StatusOK, meChatUsageResponse{
		PeriodStart: periodStart,
		PeriodEnd:   periodEnd,
		UserSummary: u,
	})
}
