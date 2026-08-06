package main

import (
	"log"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	// Supabase / Postgres connection
	DatabaseURL string

	// Anthropic API for embeddings
	AnthropicKey string

	// Path to the Unreal project root
	ProjectPath string

	// File extensions to index
	Extensions []string
}

func LoadConfig() Config {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, reading from environment")
	}

	projectPath := os.Getenv("PROJECT_PATH")
	if projectPath == "" {
		log.Fatal("PROJECT_PATH is required")
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL is required")
	}

	anthropicKey := os.Getenv("ANTHROPIC_API_KEY")
	if anthropicKey == "" {
		log.Fatal("ANTHROPIC_API_KEY is required")
	}

	return Config{
		DatabaseURL:  dbURL,
		AnthropicKey: anthropicKey,
		ProjectPath:  projectPath,
		// Index C++ and header files — adjust as needed
		Extensions: []string{".cpp", ".h", ".cs", ".ini", ".uasset"},
	}
}
