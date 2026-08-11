package handlers

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// Placeholders for Phase 3 (see docs/phase-3-aws-access-plan.md) — real
// routes so the UI has something to call and the request/error-handling
// path is exercised end-to-end, but none touch AWS yet. Swap these
// functions out for AWSHandler's real methods once Phase 3 is implemented;
// the request/response shapes documented in the plan are what the UI
// already expects back.

func AWSLFSAccessKeyStub(c echo.Context) error {
	return echo.NewHTTPError(http.StatusNotImplemented, "LFS access keys aren't set up yet — check back soon.")
}

func AWSLFSAccessKeyDeleteStub(c echo.Context) error {
	return echo.NewHTTPError(http.StatusNotImplemented, "LFS access keys aren't set up yet — check back soon.")
}

func AWSConsoleAccessStub(c echo.Context) error {
	return echo.NewHTTPError(http.StatusNotImplemented, "AWS console access isn't set up yet — check back soon.")
}

func AWSSTSCredentialsStub(c echo.Context) error {
	return echo.NewHTTPError(http.StatusNotImplemented, "Temporary AWS credentials aren't set up yet — check back soon.")
}
