package main

import (
	"log"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

// voyageFreeTierTokens is Voyage's advertised free monthly allowance for
// voyage-code-3 (see https://docs.voyageai.com/docs/pricing). Used as the
// default MaxEmbeddingTokens when EmbeddingProvider is "voyage", with a
// safety margin, so a run stops before it would start costing money —
// override with MAX_EMBEDDING_TOKENS if your plan differs.
const voyageFreeTierTokens = 200_000_000
const voyageFreeTierSafetyMargin = 10_000_000

type Config struct {
	// Supabase / Postgres connection
	DatabaseURL string

	// Which embedding provider to use: "voyage" or "openai"
	EmbeddingProvider string

	// API key for whichever provider EmbeddingProvider selects
	VoyageKey string
	OpenAIKey string

	// Stop indexing once cumulative embedding tokens for this run reach
	// this many. 0 means unlimited. This only guards a single run — it
	// does not track usage across separate runs within the same billing
	// period.
	MaxEmbeddingTokens int

	// Path to the Unreal project root
	ProjectPath string

	// File extensions to index
	Extensions []string
}

func LoadConfig() Config {
	if err := godotenv.Load(".env.local"); err != nil {
		log.Println("No .env.local file found, reading from environment")
	}

	projectPath := os.Getenv("PROJECT_PATH")
	if projectPath == "" {
		log.Fatal("PROJECT_PATH is required")
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL is required")
	}

	provider := os.Getenv("EMBEDDING_PROVIDER")
	if provider == "" {
		provider = "voyage"
	}

	cfg := Config{
		DatabaseURL:       dbURL,
		EmbeddingProvider: provider,
		ProjectPath:       projectPath,
		// Index C++, header, C#, and config files — adjust as needed.
		// .uasset is Unreal's binary asset format; don't index it as text.
		Extensions: []string{".cpp", ".h", ".cs", ".ini"},
	}

	switch provider {
	case "voyage":
		cfg.VoyageKey = os.Getenv("VOYAGE_API_KEY")
		if cfg.VoyageKey == "" {
			log.Fatal("VOYAGE_API_KEY is required when EMBEDDING_PROVIDER=voyage")
		}
		cfg.MaxEmbeddingTokens = voyageFreeTierTokens - voyageFreeTierSafetyMargin
	case "openai":
		cfg.OpenAIKey = os.Getenv("OPENAI_API_KEY")
		if cfg.OpenAIKey == "" {
			log.Fatal("OPENAI_API_KEY is required when EMBEDDING_PROVIDER=openai")
		}
		cfg.MaxEmbeddingTokens = 0 // no free tier to guard against by default
	default:
		log.Fatalf("unknown EMBEDDING_PROVIDER %q (expected \"voyage\" or \"openai\")", provider)
	}

	if raw := os.Getenv("MAX_EMBEDDING_TOKENS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			log.Fatalf("MAX_EMBEDDING_TOKENS must be an integer, got %q: %v", raw, err)
		}
		cfg.MaxEmbeddingTokens = n // explicit override, including 0 = unlimited
	}

	return cfg
}
