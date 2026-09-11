package proberunner

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bryanbarton525/pulse/internal/authn"
)

func TestResultsAPIRequiresAuthorization(t *testing.T) {
	t.Parallel()

	token := "secret"
	runner := NewRunner(logr.Discard(), prometheus.NewRegistry(), AuthStore{})
	handler := NewAPIServeMux(runner, logr.Discard(), authn.Policy{Token: func() string { return token }})

	for _, testCase := range []struct {
		name   string
		header string
		want   int
	}{
		{name: "missing", want: http.StatusUnauthorized},
		{name: "raw", header: "secret", want: http.StatusUnauthorized},
		{name: "wrong scheme", header: "Basic secret", want: http.StatusUnauthorized},
		{name: "empty bearer", header: "Bearer", want: http.StatusUnauthorized},
		{name: "malformed", header: "Bearer secret extra", want: http.StatusUnauthorized},
		{name: "incorrect", header: "Bearer wrong", want: http.StatusUnauthorized},
		{name: "correct", header: "Bearer secret", want: http.StatusOK},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/results", nil)
			request.Header.Set("Authorization", testCase.header)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != testCase.want {
				t.Fatalf("status = %d, want %d", response.Code, testCase.want)
			}
		})
	}

	token = "rotated"
	request := httptest.NewRequest(http.MethodGet, "/results", nil)
	request.Header.Set("Authorization", "Bearer rotated")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("rotated token status = %d, want %d", response.Code, http.StatusOK)
	}
}

func TestResultsAPIFailsClosedUnlessLocalModeIsExplicit(t *testing.T) {
	t.Parallel()

	runner := NewRunner(logr.Discard(), prometheus.NewRegistry(), AuthStore{})
	for _, testCase := range []struct {
		name string
		auth authn.Policy
		want int
	}{
		{name: "production empty token", auth: authn.Policy{}, want: http.StatusUnauthorized},
		{name: "explicit local mode", auth: authn.Policy{AllowUnauthenticated: true}, want: http.StatusOK},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler := NewAPIServeMux(runner, logr.Discard(), testCase.auth)
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/results", nil))
			if response.Code != testCase.want {
				t.Fatalf("status = %d, want %d", response.Code, testCase.want)
			}
		})
	}
}
