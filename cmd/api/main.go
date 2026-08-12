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
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/devportal/api/lib/handlers"
	"github.com/devportal/api/lib/mcp"
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

	// How long a POST /api/v1/me/mcp-token token lasts before it needs
	// reissuing. Safe to make this long (default 90 days): unlike the 8-hour
	// web session, mcp.NewTokenVerifier re-checks the allowlist on every MCP
	// call, so revoking a user's platform access cuts off their MCP access
	// immediately regardless of how much of this window is left.
	mcpTokenDuration := 90 * 24 * time.Hour
	if v := viper.GetInt("mcp_token_duration_seconds"); v > 0 {
		mcpTokenDuration = time.Duration(v) * time.Second
	}

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

	// AWS self-service (docs/phase-3-aws-access-plan.md). The provisioner's
	// own credentials, not per-user; see docs/aws-one-time-setup.md for the
	// IAM setup this depends on.
	ctx := context.Background()
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(mustGet("aws_region")),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			mustGet("aws_access_key_id"), mustGet("aws_secret_access_key"), "")),
	)
	if err != nil {
		logger.Fatalf("AWS config failed: %v", err)
	}
	iamClient := iam.NewFromConfig(awsCfg)
	stsClient := sts.NewFromConfig(awsCfg)

	identity, err := stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		logger.Fatalf("AWS credentials check failed (sts:GetCallerIdentity): %v", err)
	}

	awsHandlerCfg := handlers.DefaultAWSConfig()
	awsHandlerCfg.AccountID = *identity.Account
	awsHandlerCfg.Region = mustGet("aws_region")
	awsHandlerCfg.Bucket = mustGet("aws_lfs_bucket")
	awsHandlerCfg.STSRoleARN = mustGet("aws_sts_role_arn")
	if v := viper.GetInt("aws_sts_max_session_duration_seconds"); v > 0 {
		awsHandlerCfg.STSMaxSessionDurationSeconds = int32(v)
	}
	if v := get("aws_iam_username_prefix", ""); v != "" {
		awsHandlerCfg.UsernamePrefix = v
	}
	if v := get("aws_lfs_contributor_policy_name", ""); v != "" {
		awsHandlerCfg.ContributorPolicyName = v
	}
	if v := get("aws_self_manage_policy_name", ""); v != "" {
		awsHandlerCfg.SelfManagePolicyName = v
	}
	awsHandler := handlers.NewAWSHandler(iamClient, stsClient, awsHandlerCfg)

	e.GET("/auth/github", auth.Login)
	e.GET("/auth/callback", auth.Callback)
	webAssets := echo.MustSubFS(webFS, "web")
	e.StaticFS("/", webAssets)

	// Top-level, not under protected: MCP clients authenticate with the
	// bearer token minted by POST /api/v1/me/mcp-token, checked inside
	// mcp.NewHandler itself (sdkauth.RequireBearerToken), not the session
	// cookie RequireAuth expects. e.Any + echo.WrapHandler because the real
	// handler is a single http.Handler that dispatches POST/GET/DELETE itself.
	e.Any("/mcp", echo.WrapHandler(mcp.NewHandler(jwtSecret, userStore, embedder, store, chatCfg.TopK)))

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
	mcpToken := handlers.NewMCPTokenHandler(jwtSecret, mcpTokenDuration)
	protected.POST("/me/mcp-token", mcpToken.MCPToken)

	// AWS self-service (docs/phase-3-aws-access-plan.md).
	protected.POST("/aws/lfs-access-key", awsHandler.LFSAccessKey)
	protected.DELETE("/aws/lfs-access-key", awsHandler.LFSAccessKeyDelete)
	protected.POST("/aws/console-access", awsHandler.ConsoleAccess)
	protected.POST("/aws/sts-credentials", awsHandler.STSCredentials)

	adminUsers := handlers.NewAdminUsersHandler(userStore)
	assumeRole := handlers.NewAssumeRoleHandler(jwtSecret)
	admin := protected.Group("/admin")
	admin.Use(mw.RequireAdmin)
	admin.GET("/users", adminUsers.List)
	admin.POST("/users", adminUsers.Add)
	admin.PATCH("/users/:username", adminUsers.SetRole)
	admin.DELETE("/users/:username", adminUsers.Remove)
	admin.DELETE("/users/:username/aws-console-access", awsHandler.AdminConsoleAccessDelete)
	admin.POST("/assume-role", assumeRole.AssumeRole)

	// e.Logger.Fatal(e.Start(":" + port))
	if err := Serve(e, fmt.Sprintf(":%s", port)); err != nil {
		logger.Fatal(err)
	}
}
