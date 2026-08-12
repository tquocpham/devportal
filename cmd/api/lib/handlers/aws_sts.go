package handlers

import (
	"net/http"

	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/labstack/echo/v4"
)

// stsMinSessionDurationSeconds is STS's own floor, and
// stsMaxSessionDurationSeconds its absolute ceiling for AssumeRole
// sessions, neither is something we chose or that varies by deployment; no
// role can ever be granted a longer session than the max, regardless of how
// its own MaxSessionDuration is configured (docs/aws-one-time-setup.md step 4
// sets the role to exactly this ceiling). stsDefaultSessionDurationSeconds is
// ours, unlike the other two, used only when the caller omits
// durationSeconds; kept separate from the max so an omitted field doesn't
// silently grant the longest possible session.
const (
	stsDefaultSessionDurationSeconds = 3600
	stsMinSessionDurationSeconds     = 900
	stsMaxSessionDurationSeconds     = 43200
)

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

	duration := int32(stsDefaultSessionDurationSeconds)
	if req.DurationSeconds > 0 {
		duration = req.DurationSeconds
		if duration < stsMinSessionDurationSeconds {
			duration = stsMinSessionDurationSeconds
		} else if duration > stsMaxSessionDurationSeconds {
			duration = stsMaxSessionDurationSeconds
		}
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
