package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/devportal/api/lib/handlers"
	mw "github.com/devportal/api/lib/middleware"
	"github.com/devportal/api/lib/users"
	"github.com/devportal/retrieval"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

// web directory holds index.html, style.css, and app.js to be served as
// static assets via StaticFS below, which defaults "/" to index.html the
// same way a normal web server would.
//
//go:embed web
var webFS embed.FS

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

	// Retrieval + generation config. database_url and embedding_provider
	// must match whatever cmd/indexer was configured with. The query
	// embedding has to come from the same model/dimension as what's
	// stored, or similarity search is meaningless. anthropic_api_key is
	// the one company-paid key that powers chat for every logged-in user.
	databaseURL := mustGet("database_url")
	anthropicKey := mustGet("anthropic_api_key")
	embeddingProvider := get("embedding_provider", "voyage")

	chatCfg := handlers.DefaultChatConfig()
	if v := get("chat_model", ""); v != "" {
		chatCfg.Model = v
	}
	if v := viper.GetInt64("chat_max_tokens"); v > 0 {
		chatCfg.MaxTokens = v
	}
	if v := viper.GetInt("chat_top_k"); v > 0 {
		chatCfg.TopK = v
	}
	if v := viper.GetInt("chat_max_history_len"); v > 0 {
		chatCfg.MaxHistoryLen = v
	}
	if v := viper.GetInt("chat_max_iterations"); v > 0 {
		chatCfg.MaxIterations = v
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
		LogURI:      true,
		LogStatus:   true,
		LogMethod:   true,
		LogLatency:  true,
		LogError:    true,
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

	userStore, err := users.NewStore(databaseURL)
	if err != nil {
		logger.Fatalf("users DB connection failed: %v", err)
	}
	if err := userStore.CheckSchema(); err != nil {
		logger.Fatalf("allowed_users table not ready; run cmd/api/migrations/migrate.sh via CI/CD first: %v", err)
	}

	auth := handlers.NewAuthHandler(
		githubClientID, githubClientSecret, callbackURL, allowedOrg,
		jwtSecret, userStore)

	store, err := retrieval.NewStore(databaseURL)
	if err != nil {
		logger.Fatalf("DB connection failed: %v", err)
	}
	if err := store.CheckReady(); err != nil {
		logger.Fatalf("code_chunks table not ready; run cmd/indexer first: %v", err)
	}

	var embedder retrieval.Embedder
	switch embeddingProvider {
	case "voyage":
		embedder = retrieval.NewVoyageEmbedder(mustGet("voyage_api_key"))
	case "openai":
		embedder = retrieval.NewOpenAIEmbedder(mustGet("openai_api_key"))
	default:
		logger.Fatalf("unknown embedding_provider %q", embeddingProvider)
	}

	anthropicClient := anthropic.NewClient(option.WithAPIKey(anthropicKey))
	chat := handlers.NewChatHandler(store, embedder, anthropicClient, chatCfg)

	e.GET("/auth/github", auth.Login)
	e.GET("/auth/callback", auth.Callback)
	webAssets := echo.MustSubFS(webFS, "web")
	e.StaticFS("/", webAssets)

	// Prefix must be non-empty ("/api", not ""): Group.Use registers its own
	// catch-all route at prefix+"/*" so the middleware always fires, and an
	// empty prefix puts that catch-all at "/*", the exact pattern the
	// StaticFS route above uses, silently replacing it with an auth-gated
	// 404 for every path, including "/" itself. A real prefix keeps that
	// catch-all scoped to "/api/v1/*", where it belongs.
	protected := e.Group("/api/v1")
	protected.Use(mw.RequireAuth(jwtSecret))
	protected.GET("/me", handlers.Me)
	protected.POST("/chat", chat.Chat)

	// Stubs for Phase 3 (docs/phase-3-aws-access-plan.md): real routes,
	// not yet real AWS calls. See handlers.AWSLFSAccessKeyStub.
	protected.POST("/aws/lfs-access-key", handlers.AWSLFSAccessKeyStub)
	protected.DELETE("/aws/lfs-access-key", handlers.AWSLFSAccessKeyDeleteStub)
	protected.POST("/aws/console-access", handlers.AWSConsoleAccessStub)
	protected.POST("/aws/sts-credentials", handlers.AWSSTSCredentialsStub)

	adminUsers := handlers.NewAdminUsersHandler(userStore)
	assumeRole := handlers.NewAssumeRoleHandler(jwtSecret)
	admin := protected.Group("/admin")
	admin.Use(mw.RequireAdmin)
	admin.GET("/users", adminUsers.List)
	admin.POST("/users", adminUsers.Add)
	admin.PATCH("/users/:username", adminUsers.SetRole)
	admin.DELETE("/users/:username", adminUsers.Remove)
	admin.POST("/assume-role", assumeRole.AssumeRole)

	// e.Logger.Fatal(e.Start(":" + port))
	if err := Serve(e, fmt.Sprintf(":%s", port)); err != nil {
		logger.Fatal(err)
	}
}
