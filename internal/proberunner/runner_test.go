package proberunner

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
)

func TestExecuteRequestSupportsMethodHeadersBodyAndContainsText(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q, want %q", r.Method, http.MethodPost)
		}
		if got := r.Header.Get("X-Test-Header"); got != "demo" {
			t.Fatalf("header = %q, want %q", got, "demo")
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if string(body) != `{"username":"demo"}` {
			t.Fatalf("body = %q", string(body))
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("welcome demo"))
	}))
	defer server.Close()

	client, err := newHTTPClient()
	if err != nil {
		t.Fatalf("newHTTPClient() error = %v", err)
	}

	result := executeRequest(
		"default/sample",
		client,
		server.URL,
		http.MethodPost,
		map[string]string{"X-Test-Header": "demo"},
		`{"username":"demo"}`,
		http.StatusOK,
		"welcome",
	)

	if !result.Healthy {
		t.Fatalf("result healthy = false, message = %q", result.Message)
	}
	if result.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", result.StatusCode, http.StatusOK)
	}
	if !strings.Contains(result.Message, "matched response text") {
		t.Fatalf("message = %q, want contains matched response text", result.Message)
	}
}

func TestExecuteJourneyReusesCookiesAcrossSteps(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "demo"})
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("Sign in"))
		case "/dashboard":
			cookie, err := r.Cookie("session")
			if err != nil {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte("missing session"))
				return
			}
			if cookie.Value != "demo" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte("bad session"))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("dashboard ready"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := newHTTPClient()
	if err != nil {
		t.Fatalf("newHTTPClient() error = %v", err)
	}

	runner := NewRunner(logr.Discard(), prometheus.NewRegistry())
	result := runner.executeJourney(client, Probe{
		Name:           "default/sample-ui-login",
		URL:            server.URL + "/dashboard",
		ExpectedStatus: http.StatusOK,
		Journey: []ProbeStep{
			{
				Name:           "open-login",
				URL:            server.URL + "/login",
				Method:         http.MethodGet,
				ExpectedStatus: http.StatusOK,
				ContainsText:   "Sign in",
			},
			{
				Name:           "load-dashboard",
				URL:            server.URL + "/dashboard",
				Method:         http.MethodGet,
				ExpectedStatus: http.StatusOK,
				ContainsText:   "dashboard ready",
			},
		},
	})

	if !result.Healthy {
		t.Fatalf("journey healthy = false, message = %q", result.Message)
	}
	if result.StatusCode != http.StatusOK {
		t.Fatalf("journey status code = %d, want %d", result.StatusCode, http.StatusOK)
	}
	if !strings.Contains(result.Message, "Synthetic journey succeeded") {
		t.Fatalf("message = %q", result.Message)
	}
}

func TestExecuteCheckStdoutOnlySkipsPrometheusAndWritesJSON(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	registry := prometheus.NewRegistry()
	runner := NewRunner(logr.Discard(), registry)

	var stdout bytes.Buffer
	runner.stdoutWriter = &stdout

	runner.executeCheck(Probe{
		Name:           "default/stdout-only",
		URL:            server.URL,
		Interval:       30,
		ExpectedStatus: http.StatusOK,
		Outputs: []ProbeOutput{
			{Type: ProbeOutputStdout},
		},
	})

	metricFamilies, err := registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error = %v", err)
	}
	for _, family := range metricFamilies {
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetValue() == "default/stdout-only" {
					t.Fatalf("unexpected prometheus metric for stdout-only probe in family %q", family.GetName())
				}
			}
		}
	}

	output := strings.TrimSpace(stdout.String())
	if output == "" {
		t.Fatal("stdout output is empty")
	}

	var result ProbeResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("stdout JSON unmarshal error = %v", err)
	}
	if result.Name != "default/stdout-only" {
		t.Fatalf("stdout result name = %q, want %q", result.Name, "default/stdout-only")
	}
	if !result.Healthy {
		t.Fatalf("stdout result healthy = false, message = %q", result.Message)
	}
}

func TestShouldEmitPrometheusDefaultsToTrue(t *testing.T) {
	t.Parallel()

	if !shouldEmitPrometheus(nil) {
		t.Fatal("shouldEmitPrometheus(nil) = false, want true")
	}
}
