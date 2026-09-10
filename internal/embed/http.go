package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sync"
	"time"
)

// HTTPEmbedder calls an OpenAI-compatible /v1/embeddings endpoint.
//
// This is the bring-your-own-model path for the cold tier: Text Embeddings
// Inference, Infinity, Ollama, or an SGLang server launched with --is-embedding.
//
// Note that a GENERATION server does not serve this endpoint. SGLang, for
// example, rejects embedding requests against a chat model with "This model
// does not appear to be an embedding model by default" — one server instance is
// either generative or an embedder, never both.
type HTTPEmbedder struct {
	endpoint string
	model    string
	apiKey   string
	space    string
	client   *http.Client
	mu       sync.RWMutex

	// dimensions is discovered from the first response, since the endpoint
	// does not advertise it up front.
	dimensions int
}

// NewHTTPEmbedder builds an embedder backed by a remote service. Its space is
// derived from the endpoint and model, deliberately excluding the API key so
// credential rotation retains learned correlation state.
func NewHTTPEmbedder(endpoint, model, apiKey string, timeout time.Duration) *HTTPEmbedder {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	return &HTTPEmbedder{
		endpoint: endpoint,
		model:    model,
		apiKey:   apiKey,
		space:    SpaceIdentity("http", NormalizedEndpoint(endpoint), model),
		client:   &http.Client{Timeout: timeout},
	}
}

// Space implements Embedder.
func (h *HTTPEmbedder) Space() string { return h.space }

// Dimensions implements Embedder. It reports zero until the first successful
// call, because the endpoint only reveals the width in its response.
func (h *HTTPEmbedder) Dimensions() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.dimensions
}

// Close implements Embedder.
func (h *HTTPEmbedder) Close() error { return nil }

type embeddingsRequest struct {
	Model string   `json:"model,omitempty"`
	Input []string `json:"input"`
}

type embeddingsResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Embed implements Embedder.
func (h *HTTPEmbedder) Embed(ctx context.Context, texts []string) ([]Vector, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	payload, err := json.Marshal(embeddingsRequest{Model: h.model, Input: texts})
	if err != nil {
		return nil, fmt.Errorf("encoding embeddings request: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, h.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("building embeddings request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if h.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+h.apiKey)
	}

	response, err := h.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("calling embeddings endpoint: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("reading embeddings response: %w", err)
	}

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embeddings endpoint returned %d: %s",
			response.StatusCode, truncate(string(body), 256))
	}

	var decoded embeddingsResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decoding embeddings response: %w", err)
	}
	if decoded.Error != nil {
		return nil, fmt.Errorf("embeddings endpoint error: %s", decoded.Error.Message)
	}
	if len(decoded.Data) != len(texts) {
		return nil, errShortResult{want: len(texts), got: len(decoded.Data)}
	}

	vectors := make([]Vector, len(decoded.Data))
	seen := make([]bool, len(decoded.Data))
	dimensions := 0
	for _, item := range decoded.Data {
		if item.Index < 0 || item.Index >= len(vectors) || seen[item.Index] {
			return nil, fmt.Errorf("embeddings endpoint returned invalid or duplicate index %d", item.Index)
		}
		seen[item.Index] = true
		if len(item.Embedding) == 0 {
			return nil, fmt.Errorf("embeddings endpoint returned an empty vector at index %d", item.Index)
		}
		if dimensions == 0 {
			dimensions = len(item.Embedding)
		} else if len(item.Embedding) != dimensions {
			return nil, fmt.Errorf("embeddings endpoint returned mixed dimensions %d and %d",
				dimensions, len(item.Embedding))
		}
		values := append([]float32(nil), item.Embedding...)
		if !finiteEmbedding(values) {
			return nil, fmt.Errorf("embeddings endpoint returned a non-finite value at index %d", item.Index)
		}
		normalizeInPlace(values)
		vectors[item.Index] = Vector{Space: h.space, Values: values}
	}

	h.mu.Lock()
	if h.dimensions == 0 {
		h.dimensions = dimensions
	} else if h.dimensions != dimensions {
		known := h.dimensions
		h.mu.Unlock()
		return nil, fmt.Errorf("embeddings endpoint changed dimensions from %d to %d", known, dimensions)
	}
	h.mu.Unlock()

	return vectors, nil
}

func finiteEmbedding(values []float32) bool {
	for _, value := range values {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return false
		}
	}
	return true
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}
