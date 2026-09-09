package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
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

	// dimensions is discovered from the first response, since the endpoint
	// does not advertise it up front.
	dimensions int
}

// NewHTTPEmbedder builds an embedder backed by a remote service.
func NewHTTPEmbedder(endpoint, model, apiKey, space string, timeout time.Duration) *HTTPEmbedder {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	return &HTTPEmbedder{
		endpoint: endpoint,
		model:    model,
		apiKey:   apiKey,
		space:    space,
		client:   &http.Client{Timeout: timeout},
	}
}

// Space implements Embedder.
func (h *HTTPEmbedder) Space() string { return h.space }

// Dimensions implements Embedder. It reports zero until the first successful
// call, because the endpoint only reveals the width in its response.
func (h *HTTPEmbedder) Dimensions() int { return h.dimensions }

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

	// The spec allows results in any order; index is authoritative.
	sort.Slice(decoded.Data, func(i, j int) bool {
		return decoded.Data[i].Index < decoded.Data[j].Index
	})

	vectors := make([]Vector, len(decoded.Data))
	for index, item := range decoded.Data {
		values := append([]float32(nil), item.Embedding...)
		normalizeInPlace(values)
		vectors[index] = Vector{Space: h.space, Values: values}
		if h.dimensions == 0 {
			h.dimensions = len(values)
		}
	}

	return vectors, nil
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}
