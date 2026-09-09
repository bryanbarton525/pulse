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
