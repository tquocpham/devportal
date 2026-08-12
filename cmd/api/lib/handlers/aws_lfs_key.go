package handlers

import (
	"net/http"

	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/labstack/echo/v4"
)

// LFSAccessKey handles POST /api/v1/aws/lfs-access-key (Flow A1). Issues a
// long-lived access key directly, no console visit or self-serve step.
func (h *AWSHandler) LFSAccessKey(c echo.Context) error {
	ctx := c.Request().Context()
	username, err := h.iamUsername(c)
	if err != nil {
		return err
	}

	if err := h.ensureUser(ctx, username); err != nil {
		c.Logger().Errorf("ensure IAM user for lfs-access-key: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to set up AWS access")
	}

	existing, err := h.iam.ListAccessKeys(ctx, &iam.ListAccessKeysInput{UserName: &username})
	if err != nil {
		c.Logger().Errorf("list access keys: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to check existing keys")
	}
	if len(existing.AccessKeyMetadata) > 0 {
		return c.JSON(http.StatusOK, map[string]any{
			"alreadyExists": true,
			"accessKeyId":   *existing.AccessKeyMetadata[0].AccessKeyId,
			"message":       "You already have an active access key. Delete it first (below) if you need a new one.",
		})
	}

	out, err := h.iam.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: &username})
	if err != nil {
		c.Logger().Errorf("create access key: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create access key")
	}

	c.Logger().Infof("issued LFS access key for %s", username)
	return c.JSON(http.StatusOK, map[string]any{
		"alreadyExists":   false,
		"accessKeyId":     *out.AccessKey.AccessKeyId,
		"secretAccessKey": *out.AccessKey.SecretAccessKey,
		"bucket":          h.cfg.Bucket,
		"region":          h.cfg.Region,
	})
}

// LFSAccessKeyDelete handles DELETE /api/v1/aws/lfs-access-key (Flow A1b).
// Self-service deletion, so a lost or rotated key never requires Flow A2.
func (h *AWSHandler) LFSAccessKeyDelete(c echo.Context) error {
	ctx := c.Request().Context()
	username, err := h.iamUsername(c)
	if err != nil {
		return err
	}

	existing, err := h.iam.ListAccessKeys(ctx, &iam.ListAccessKeysInput{UserName: &username})
	if err != nil {
		if isNoSuchEntity(err) {
			// User was never provisioned at all. Same outcome as "no key".
			return c.JSON(http.StatusOK, map[string]any{"deleted": false, "message": "You don't have a key to delete."})
		}
		c.Logger().Errorf("list access keys for delete: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to check existing keys")
	}
	if len(existing.AccessKeyMetadata) == 0 {
		return c.JSON(http.StatusOK, map[string]any{"deleted": false, "message": "You don't have a key to delete."})
	}

	keyID := existing.AccessKeyMetadata[0].AccessKeyId
	if _, err := h.iam.DeleteAccessKey(ctx, &iam.DeleteAccessKeyInput{UserName: &username, AccessKeyId: keyID}); err != nil {
		c.Logger().Errorf("delete access key: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete access key")
	}

	c.Logger().Infof("deleted LFS access key for %s", username)
	return c.JSON(http.StatusOK, map[string]any{"deleted": true})
}
