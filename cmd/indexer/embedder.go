package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Anthropic doesn't expose a dedicated embedding endpoint, so embeddings
// come from a separate provider. Embedder abstracts over that provider so
// it can be swapped — but note the data isn't portable across providers:
// different models produce incompatible vector spaces, and the pgvector
// column has a fixed dimension (see store.go), so switching providers
// always means re-running a full index, not just changing a setting.
type Embedder interface {
	// Embed returns the embedding vector and the number of tokens the
	// provider billed for this call, so callers can track cumulative
	// spend against a budget (see MaxEmbeddingTokens in config.go).
	Embed(text string) (embedding []float32, tokensUsed int, err error)
}

// ErrRateLimited is returned when the embedding provider responds with
// HTTP 429. Callers should treat this as fatal for the whole run rather
// than skipping the chunk and continuing: a 429 means every subsequent
// call will keep failing the same way until the underlying limit is
// fixed (e.g. adding a payment method), so continuing just burns through
// remaining quota/time and produces an incomplete index.
var ErrRateLimited = errors.New("embedding provider rate limited the request")

// ---- Voyage AI (recommended: code-tuned, free tier covers this project) ----

const voyageEmbeddingModel = "voyage-code-3"
const voyageEmbeddingURL = "https://api.voyageai.com/v1/embeddings"

// EmbeddingDim is the pgvector column dimension. voyage-code-3 supports
// 256/512/1024/2048; 1024 is its default and what we request explicitly
// so a future model-default change on Voyage's side can't silently break
// inserts into a fixed-width column.
const EmbeddingDim = 1024

type voyageEmbedRequest struct {
	Input           []string `json:"input"`
	Model           string   `json:"model"`
	InputType       string   `json:"input_type"`
	OutputDimension int      `json:"output_dimension"`
}

type voyageEmbedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

type voyageEmbedder struct {
	apiKey string
	client *http.Client
}

func NewVoyageEmbedder(apiKey string) Embedder {
	return &voyageEmbedder{
		apiKey: apiKey,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (e *voyageEmbedder) Embed(text string) ([]float32, int, error) {
	body, _ := json.Marshal(voyageEmbedRequest{
		Input:           []string{text},
		Model:           voyageEmbeddingModel,
		InputType:       "document",
		OutputDimension: EmbeddingDim,
	})

	req, err := http.NewRequest("POST", voyageEmbeddingURL, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, 0, fmt.Errorf("%w: %s", ErrRateLimited, respBody)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, 0, fmt.Errorf("voyage embedding API error (status %d): %s", resp.StatusCode, respBody)
	}

	var result voyageEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, 0, err
	}
	if len(result.Data) == 0 {
		return nil, 0, fmt.Errorf("no embedding returned")
	}

	return result.Data[0].Embedding, result.Usage.TotalTokens, nil
}

// ---- OpenAI (alternative: no free tier, but cheap and no code-specific tuning needed) ----

const openAIEmbeddingModel = "text-embedding-3-small"
const openAIEmbeddingURL = "https://api.openai.com/v1/embeddings"

type openAIEmbedRequest struct {
	Input string `json:"input"`
	Model string `json:"model"`
}

type openAIEmbedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type openAIEmbedder struct {
	apiKey string
	client *http.Client
}

// NewOpenAIEmbedder is unused while EMBEDDING_PROVIDER=voyage, but kept as
// a documented fallback: text-embedding-3-small outputs 1536 dimensions,
// which does not match EmbeddingDim above, so switching to it also means
// updating EmbeddingDim and re-running the migration/full index.
func NewOpenAIEmbedder(apiKey string) Embedder {
	return &openAIEmbedder{
		apiKey: apiKey,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (e *openAIEmbedder) Embed(text string) ([]float32, int, error) {
	body, _ := json.Marshal(openAIEmbedRequest{
		Input: text,
		Model: openAIEmbeddingModel,
	})

	req, err := http.NewRequest("POST", openAIEmbeddingURL, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, 0, fmt.Errorf("%w: %s", ErrRateLimited, respBody)
	}

	var result openAIEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, 0, err
	}
	if result.Error != nil {
		return nil, 0, fmt.Errorf("openai embedding API error: %s", result.Error.Message)
	}
	if len(result.Data) == 0 {
		return nil, 0, fmt.Errorf("no embedding returned")
	}

	return result.Data[0].Embedding, result.Usage.TotalTokens, nil
}
