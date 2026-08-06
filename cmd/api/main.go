package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/devportal/api/lib/handlers"
	mw "github.com/devportal/api/lib/middleware"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

func Serve(e *echo.Echo, addr string) error {
	errCh := make(chan error, 1)
	go func() {
		if err := e.Start(addr); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err // startup failure (e.g. port already in use)
	case <-quit:
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	return e.Shutdown(ctx)
}

func main() {
	viper.SetConfigType("yaml")
	viper.SetConfigName("config.local")
	viper.AddConfigPath(".")
	viper.ReadInConfig()

	githubClientID := mustGet("github_client_id")
	githubClientSecret := mustGet("github_client_secret")
	callbackURL := mustGet("callback_url")
	allowedOrg := viper.GetString("github_org")
	jwtSecret := mustGet("jwt_secret")
	logLevel := get("log_level", "debug")
	port := viper.GetString("port")
	if port == "" {
		port = "3000"
	}

	logger := logrus.New()
	logger.SetFormatter(&logrus.JSONFormatter{})
	level, err := logrus.ParseLevel(logLevel)
	if err != nil {
		logrus.Fatal(err)
	}
	logger.SetLevel(level)

	e := echo.New()
	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogURI:     true,
		LogStatus:  true,
		LogMethod:  true,
		LogLatency: true,
		LogError:   true,
		HandleError: true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			entry := logger.WithFields(logrus.Fields{
				"uri":     v.URI,
				"method":  v.Method,
				"status":  v.Status,
				"latency": v.Latency.String(),
			})
			if v.Error != nil {
				entry.WithError(v.Error).Error("request")
			} else {
				entry.Info("request")
			}
			return nil
		},
	}))
	e.Use(middleware.Recover())

	allowedUsers := []string{
		"tquocpham",
	}

	auth := handlers.NewAuthHandler(githubClientID, githubClientSecret, callbackURL, allowedOrg, jwtSecret, allowedUsers)

	e.GET("/auth/github", auth.Login)
	e.GET("/auth/callback", auth.Callback)
	e.GET("/", func(c echo.Context) error {
		return c.Redirect(http.StatusTemporaryRedirect, "/api/me")
	})

	protected := e.Group("")
	protected.Use(mw.RequireAuth(jwtSecret))
	protected.GET("/api/me", handlers.Me)

	// e.Logger.Fatal(e.Start(":" + port))
	if err := Serve(e, fmt.Sprintf(":%s", port)); err != nil {
		logger.Fatal(err)
	}
}
