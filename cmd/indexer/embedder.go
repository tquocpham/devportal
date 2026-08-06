package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// We use OpenAI's embedding API here as Anthropic doesn't expose
// a dedicated embedding endpoint — text-embedding-3-small is cheap
// (~$0.02 per million tokens) and works great for code search.
// Swap the URL/key if you use a different provider.

const embeddingModel = "text-embedding-3-small"
const embeddingURL = "https://api.openai.com/v1/embeddings"
const embeddingDim = 1536

type embedRequest struct {
	Input string `json:"input"`
	Model string `json:"model"`
}

type embedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type Embedder struct {
	apiKey string
	client *http.Client
}

func NewEmbedder(apiKey string) *Embedder {
	return &Embedder{
		apiKey: apiKey,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (e *Embedder) Embed(text string) ([]float32, error) {
	body, _ := json.Marshal(embedRequest{
		Input: text,
		Model: embeddingModel,
	})

	req, err := http.NewRequest("POST", embeddingURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	if result.Error != nil {
		return nil, fmt.Errorf("embedding API error: %s", result.Error.Message)
	}

	if len(result.Data) == 0 {
		return nil, fmt.Errorf("no embedding returned")
	}

	return result.Data[0].Embedding, nil
}
