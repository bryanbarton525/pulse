package embed

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPEmbedderSendsOpenAICompatibleRequest(t *testing.T) {
	t.Parallel()

	var gotAuth, gotContentType, gotPath string
	var gotBody embeddingsRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		gotPath = r.URL.Path

		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"index":0,"embedding":[1,0,0]},
			{"index":1,"embedding":[0,1,0]}
		]}`))
	}))
	defer server.Close()

	embedder := NewHTTPEmbedder(
		server.URL+"/v1/embeddings", "bge-small", "secret-token", SpaceMiniLM, 5*time.Second)

	vectors, err := embedder.Embed(context.Background(), []string{"alpha", "beta"})
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}

	if gotPath != "/v1/embeddings" {
		t.Fatalf("request path = %q, want /v1/embeddings", gotPath)
	}
	if gotAuth != "Bearer secret-token" {
		t.Fatalf("Authorization = %q, want a bearer token", gotAuth)
	}
	if gotContentType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", gotContentType)
	}
	if gotBody.Model != "bge-small" {
		t.Fatalf("request model = %q, want bge-small", gotBody.Model)
	}
	if len(gotBody.Input) != 2 || gotBody.Input[0] != "alpha" {
		t.Fatalf("request input = %v, want [alpha beta]", gotBody.Input)
	}

	if len(vectors) != 2 {
		t.Fatalf("Embed() returned %d vectors, want 2", len(vectors))
	}
	if vectors[0].Space != SpaceMiniLM {
		t.Fatalf("vector space = %q, want %q", vectors[0].Space, SpaceMiniLM)
	}
	if embedder.Dimensions() != 3 {
		t.Fatalf("Dimensions() = %d, want 3", embedder.Dimensions())
	}
}

// The API allows results in any order, so index must be authoritative — using
// array position would silently pair each text with the wrong vector.
func TestHTTPEmbedderReordersByIndex(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[
			{"index":1,"embedding":[0,1]},
			{"index":0,"embedding":[1,0]}
		]}`))
	}))
	defer server.Close()

	embedder := NewHTTPEmbedder(server.URL, "", "", SpaceMiniLM, 5*time.Second)
	vectors, err := embedder.Embed(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}

	if vectors[0].Values[0] != 1 || vectors[1].Values[1] != 1 {
		t.Fatalf("vectors were not reordered by index: %v", vectors)
	}
}

func TestHTTPEmbedderOmitsAuthorizationWithoutAPIKey(t *testing.T) {
	t.Parallel()

	var hadAuth bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadAuth = r.Header["Authorization"]
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[1,0]}]}`))
	}))
	defer server.Close()

	embedder := NewHTTPEmbedder(server.URL, "", "", SpaceMiniLM, 5*time.Second)
	if _, err := embedder.Embed(context.Background(), []string{"x"}); err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if hadAuth {
		t.Fatal("Authorization header was sent without an API key")
	}
}

// A generation server rejects embedding requests. The error must name the
// cause rather than surfacing as a bare status code.
func TestHTTPEmbedderSurfacesEndpointErrors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(
			`{"error":{"message":"This model does not appear to be an embedding model by default."}}`))
	}))
	defer server.Close()

	embedder := NewHTTPEmbedder(server.URL, "", "", SpaceMiniLM, 5*time.Second)
	_, err := embedder.Embed(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("Embed() error = nil, want an endpoint error")
	}
	if !strings.Contains(err.Error(), "embedding model") {
		t.Fatalf("Embed() error = %v, want it to carry the server's message", err)
	}
}

// A response with fewer vectors than inputs would misalign every result.
func TestHTTPEmbedderRejectsMismatchedResultCount(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[1,0]}]}`))
	}))
	defer server.Close()

	embedder := NewHTTPEmbedder(server.URL, "", "", SpaceMiniLM, 5*time.Second)
	if _, err := embedder.Embed(context.Background(), []string{"a", "b"}); err == nil {
		t.Fatal("Embed() error = nil, want a result-count mismatch error")
	}
}
