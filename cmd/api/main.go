package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log"
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
	"github.com/devportal/api/lib/repos"
	"github.com/devportal/api/lib/usage"
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
	viper.AddConfigPath(".")

	// config.yaml is the base/shared layer (optional); config.local.yaml
	// (the gitignored dev/local convention) is merged on top and overrides
	// any key it also defines. MergeInConfig, not ReadInConfig, for both:
	// a later merge's values win over an earlier one's for shared keys,
	// which is what actually gives config.local.yaml override precedence.
	// Previously ReadInConfig's error was discarded entirely, so a
	// missing/misnamed file didn't fail here, it silently ran with zero
	// config loaded and surfaced later as a confusing "required config key
	// %q is not set" fatal from whichever mustGet happened to run first,
	// not the actual problem. A file that exists but fails to parse still
	// fails loudly and immediately, that's not swallowed either.
	loaded := false
	for _, name := range []string{"config", "config.local"} {
		viper.SetConfigName(name)
		if err := viper.MergeInConfig(); err != nil {
			var notFound viper.ConfigFileNotFoundError
			if !errors.As(err, &notFound) {
				log.Fatalf("%s.yaml found but failed to parse: %v", name, err)
			}
			continue
		}
		loaded = true
	}
	if !loaded {
		log.Fatalf("no config file found (looked for config.yaml and config.local.yaml next to the binary)")
	}

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

	// Day of month the Anthropic billing cycle resets on; 1 = calendar
	// month. Clamped to [1, 28] so every month actually has that day,
	// no Feb 30 edge cases to reason about.
	billingPeriodStart := 1
	if v := viper.GetInt("billing_period_start_day"); v > 0 {
		billingPeriodStart = min(max(v, 1), 28)
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

	repoStore, err := repos.NewStore(databaseURL)
	if err != nil {
		logger.Fatalf("repos DB connection failed: %v", err)
	}
	if err := repoStore.CheckSchema(); err != nil {
		logger.Fatalf("repos table not ready; run cmd/api/migrations/migrate.sh via CI/CD first: %v", err)
	}

	usageStore, err := usage.NewStore(databaseURL)
	if err != nil {
		logger.Fatalf("usage DB connection failed: %v", err)
	}
	if err := usageStore.CheckSchema(); err != nil {
		logger.Fatalf("chat_usage table not ready; run cmd/api/migrations/migrate.sh via CI/CD first: %v", err)
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
	chat := handlers.NewChatHandler(store, embedder, anthropicClient, usageStore, chatCfg)

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
	reposHandler := handlers.NewReposHandler(repoStore)
	protected.GET("/repos", reposHandler.List)
	chatUsageHandler := handlers.NewChatUsageHandler(usageStore, billingPeriodStart)
	protected.GET("/me/chat-usage", chatUsageHandler.Me)

	// AWS self-service (docs/phase-3-aws-access-plan.md).
	protected.POST("/aws/lfs-access-key", awsHandler.LFSAccessKey)
	protected.DELETE("/aws/lfs-access-key", awsHandler.LFSAccessKeyDelete)
	protected.POST("/aws/console-access", awsHandler.ConsoleAccess)
	protected.POST("/aws/sts-credentials", awsHandler.STSCredentials)

	adminUsers := handlers.NewAdminUsersHandler(userStore)
	adminRepos := handlers.NewAdminReposHandler(repoStore)
	adminChatUsage := handlers.NewAdminChatUsageHandler(usageStore, billingPeriodStart)
	assumeRole := handlers.NewAssumeRoleHandler(jwtSecret)
	admin := protected.Group("/admin")
	admin.Use(mw.RequireAdmin)
	admin.GET("/users", adminUsers.List)
	admin.POST("/users", adminUsers.Add)
	admin.PATCH("/users/:username", adminUsers.SetRole)
	admin.DELETE("/users/:username", adminUsers.Remove)
	admin.DELETE("/users/:username/aws-console-access", awsHandler.AdminConsoleAccessDelete)
	admin.GET("/repos", adminRepos.List)
	admin.POST("/repos", adminRepos.Create)
	admin.DELETE("/repos/:id", adminRepos.Delete)
	admin.GET("/users/:username/repos", adminRepos.ListForUser)
	admin.POST("/users/:username/repos", adminRepos.Grant)
	admin.DELETE("/users/:username/repos/:repoId", adminRepos.Revoke)
	admin.GET("/chat-usage", adminChatUsage.List)
	admin.POST("/assume-role", assumeRole.AssumeRole)

	// e.Logger.Fatal(e.Start(":" + port))
	if err := Serve(e, fmt.Sprintf(":%s", port)); err != nil {
		logger.Fatal(err)
	}
}
