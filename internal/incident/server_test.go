package incident

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
)

func TestInternalPostsRequireBearerToken(t *testing.T) {
	engine := NewEngine(EngineOptions{Logger: logr.Discard()})
	token := "secret"
	handler := NewServeMux(engine, NewAggregator(time.Minute), logr.Discard(), prometheus.NewRegistry(), func() string { return token })

	request := httptest.NewRequest(http.MethodPost, "/observations", strings.NewReader(`{"observations":[]}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want %d", response.Code, http.StatusUnauthorized)
	}

	request = httptest.NewRequest(http.MethodPost, "/observations", strings.NewReader(`{"observations":[]}`))
	request.Header.Set("Authorization", "Bearer secret")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("authenticated status = %d, want %d", response.Code, http.StatusAccepted)
	}

	token = "rotated"
	request = httptest.NewRequest(http.MethodPost, "/observations", strings.NewReader(`{"observations":[]}`))
	request.Header.Set("Authorization", "Bearer rotated")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("rotated-token status = %d, want %d", response.Code, http.StatusAccepted)
	}
}

func TestOperationalReadsRequireBearerToken(t *testing.T) {
	t.Parallel()

	engine := NewEngine(EngineOptions{Logger: logr.Discard()})
	handler := NewAPIServeMux(
		engine, NewAggregator(time.Minute), logr.Discard(), func() string { return "secret" })
	for _, path := range []string{"/results", "/incidents", "/topology"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s without token = %d, want %d", path, response.Code, http.StatusUnauthorized)
		}

		request = httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer secret")
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s with token = %d, want %d", path, response.Code, http.StatusOK)
		}
	}
}

func TestMetricsMuxDoesNotExposeOperationalEndpoints(t *testing.T) {
	t.Parallel()

	handler := NewMetricsServeMux(logr.Discard(), prometheus.NewRegistry())
	for _, path := range []string{"/metrics", "/healthz"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s = %d, want %d", path, response.Code, http.StatusOK)
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/incidents", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("metrics mux /incidents = %d, want %d", response.Code, http.StatusNotFound)
	}
}
