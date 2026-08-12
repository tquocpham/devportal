package handlers

import (
	"net/http"

	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/labstack/echo/v4"
)

// stsMinSessionDurationSeconds is STS's own floor, not something we chose.
const stsMinSessionDurationSeconds = 900

type stsCredentialsRequest struct {
	DurationSeconds int32 `json:"durationSeconds"`
}

// STSCredentials handles POST /api/v1/aws/sts-credentials (Flow B). The
// short-lived "ops stuff" path. No long-lived secret ever created.
func (h *AWSHandler) STSCredentials(c echo.Context) error {
	ctx := c.Request().Context()
	username, err := h.iamUsername(c)
	if err != nil {
		return err
	}

	var req stsCredentialsRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	duration := h.cfg.STSMaxSessionDurationSeconds
	if req.DurationSeconds > 0 {
		duration = min(max(req.DurationSeconds, stsMinSessionDurationSeconds), h.cfg.STSMaxSessionDurationSeconds)
	}

	roleArn := h.cfg.STSRoleARN
	sessionName := username
	out, err := h.sts.AssumeRole(ctx, &sts.AssumeRoleInput{
		RoleArn:         &roleArn,
		RoleSessionName: &sessionName,
		DurationSeconds: &duration,
	})
	if err != nil {
		c.Logger().Errorf("assume role: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get temporary credentials")
	}

	c.Logger().Infof("issued STS credentials for %s, duration %ds", username, duration)
	return c.JSON(http.StatusOK, map[string]any{
		"accessKeyId":     *out.Credentials.AccessKeyId,
		"secretAccessKey": *out.Credentials.SecretAccessKey,
		"sessionToken":    *out.Credentials.SessionToken,
		"expiration":      out.Credentials.Expiration,
	})
}
