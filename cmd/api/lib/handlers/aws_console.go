package handlers

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/labstack/echo/v4"
)

const (
	tempPasswordLength    = 24
	tempPasswordUppercase = "ABCDEFGHJKLMNPQRSTUVWXYZ"  // no I/O, easy to misread
	tempPasswordLowercase = "abcdefghijkmnopqrstuvwxyz" // no l
	tempPasswordDigits    = "23456789"                  // no 0/1
	tempPasswordSymbols   = "!@#$%^&*()-_=+"
)

// randomTempPassword generates a one-time console password that satisfies
// typical IAM account password policies (min length + all four character
// classes), not just crypto/rand.Read like randomState() in auth.go:203 —
// AWS rejects CreateLoginProfile outright if the account's policy requires a
// class this password doesn't have. It's reset on first sign-in regardless
// (PasswordResetRequired), so this only has to be accepted once.
func randomTempPassword() (string, error) {
	classes := []string{tempPasswordUppercase, tempPasswordLowercase, tempPasswordDigits, tempPasswordSymbols}
	all := tempPasswordUppercase + tempPasswordLowercase + tempPasswordDigits + tempPasswordSymbols

	pw := make([]byte, tempPasswordLength)
	for i, class := range classes {
		c, err := randomChar(class)
		if err != nil {
			return "", err
		}
		pw[i] = c
	}
	for i := len(classes); i < len(pw); i++ {
		c, err := randomChar(all)
		if err != nil {
			return "", err
		}
		pw[i] = c
	}

	// Fisher-Yates shuffle so the guaranteed classes aren't always in the
	// first four positions.
	for i := len(pw) - 1; i > 0; i-- {
		jBig, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return "", err
		}
		j := jBig.Int64()
		pw[i], pw[j] = pw[j], pw[i]
	}

	return string(pw), nil
}

func randomChar(charset string) (byte, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
	if err != nil {
		return 0, err
	}
	return charset[n.Int64()], nil
}

// ConsoleAccess handles POST /api/v1/aws/console-access (Flow A2). Kept for
// devops work that actually needs the AWS console UI in a browser.
func (h *AWSHandler) ConsoleAccess(c echo.Context) error {
	ctx := c.Request().Context()
	username, err := h.iamUsername(c)
	if err != nil {
		return err
	}

	if err := h.ensureUser(ctx, username); err != nil {
		c.Logger().Errorf("ensure IAM user for console-access: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to set up AWS access")
	}

	if _, err := h.iam.GetLoginProfile(ctx, &iam.GetLoginProfileInput{UserName: &username}); err == nil {
		// Already has a login profile. Don't touch it, might silently
		// rotate a password the developer already changed.
		return c.JSON(http.StatusOK, map[string]any{
			"alreadyExisted": true,
			"message":        `You already have console access. Use "forgot password" on the AWS sign-in page if you need a reset.`,
		})
	} else if !isNoSuchEntity(err) {
		c.Logger().Errorf("get login profile: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to check console access")
	}

	tempPassword, err := randomTempPassword()
	if err != nil {
		c.Logger().Errorf("generate temp password: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to generate password")
	}

	if _, err := h.iam.CreateLoginProfile(ctx, &iam.CreateLoginProfileInput{
		UserName:              &username,
		Password:              &tempPassword,
		PasswordResetRequired: true,
	}); err != nil {
		c.Logger().Errorf("create login profile: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create console login")
	}

	c.Logger().Infof("issued console access for %s", username)
	return c.JSON(http.StatusOK, map[string]any{
		"alreadyExisted":        false,
		"username":              username,
		"consoleSignInURL":      fmt.Sprintf("https://%s.signin.aws.amazon.com/console", h.cfg.AccountID),
		"temporaryPassword":     tempPassword,
		"passwordResetRequired": true,
	})
}

// AdminConsoleAccessDelete handles DELETE
// /api/v1/admin/users/:username/aws-console-access (admin-only, registered
// behind RequireAdmin in main.go). Unlike Flow A2 above, which only ever
// creates a login profile and never rotates or removes one, this lets an
// admin revoke someone else's console login outright, e.g. as part of
// offboarding them.
func (h *AWSHandler) AdminConsoleAccessDelete(c echo.Context) error {
	ctx := c.Request().Context()
	username, err := h.resolveUsername(c.Param("username"))
	if err != nil {
		return err
	}

	if _, err := h.iam.GetLoginProfile(ctx, &iam.GetLoginProfileInput{UserName: &username}); err != nil {
		if isNoSuchEntity(err) {
			return c.JSON(http.StatusOK, map[string]any{"deleted": false, "message": "No console access to remove."})
		}
		c.Logger().Errorf("get login profile for delete: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to check console access")
	}

	if _, err := h.iam.DeleteLoginProfile(ctx, &iam.DeleteLoginProfileInput{UserName: &username}); err != nil {
		c.Logger().Errorf("delete login profile: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to remove console access")
	}

	c.Logger().Infof("admin removed console access for %s", username)
	return c.JSON(http.StatusOK, map[string]any{"deleted": true})
}
