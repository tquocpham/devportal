package handlers

import (
	"net/http"
	"time"

	"github.com/devportal/api/lib/usage"
	"github.com/labstack/echo/v4"
)

// AdminChatUsageHandler backs GET /api/v1/admin/chat-usage. Registered
// behind both RequireAuth and RequireAdmin in main.go, same as
// AdminUsersHandler/AdminReposHandler.
type adminChatUsageHandler struct {
	store              usage.Store
	billingPeriodStart int // day of month the billing cycle resets on, 1 = calendar month
}

type AdminChatUsageHandler interface {
	List(c echo.Context) error
}

func NewAdminChatUsageHandler(store usage.Store, billingPeriodStart int) AdminChatUsageHandler {
	return &adminChatUsageHandler{
		store:              store,
		billingPeriodStart: billingPeriodStart,
	}
}

type chatUsageResponse struct {
	PeriodStart time.Time           `json:"periodStart"`
	PeriodEnd   time.Time           `json:"periodEnd"`
	Users       []usage.UserSummary `json:"users"`
}

// List handles GET /api/v1/admin/chat-usage: questions and token usage per
// user for the current billing period, most active first,
// docs/phase-4b-chat-usage-tracking-plan.md's visibility-only view, no caps
// or blocking here. chat_usage itself keeps every day forever; only this
// query's window changes as the period rolls over, month to month.
func (h *adminChatUsageHandler) List(c echo.Context) error {
	periodStart, periodEnd := usage.CurrentBillingPeriod(h.billingPeriodStart, time.Now())

	summary, err := h.store.Summary(periodStart, periodEnd)
	if err != nil {
		c.Logger().Errorf("chat usage summary: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to load chat usage")
	}
	if summary == nil {
		summary = []usage.UserSummary{}
	}
	return c.JSON(http.StatusOK, chatUsageResponse{
		PeriodStart: periodStart,
		PeriodEnd:   periodEnd,
		Users:       summary,
	})
}
