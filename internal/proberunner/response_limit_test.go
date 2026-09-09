package proberunner

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestHTTPRequestRejectsOversizedResponseBeforeBufferingIt(t *testing.T) {
	runner := NewRunner(logr.Discard(), prometheus.NewRegistry(), AuthStore{})
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", maxHTTPResponseBytes+1))),
			Header:     make(http.Header),
		}, nil
	})}

	status, body, err := runner.doHTTPRequest(client, "http://example.test", http.MethodGet, nil, "", nil)
	if err == nil || !strings.Contains(err.Error(), "safety limit") {
		t.Fatalf("error = %v, want response-body safety-limit error", err)
	}
	if status != http.StatusOK || body != nil {
		t.Fatalf("status=%d body-bytes=%d, want status 200 and no retained body", status, len(body))
	}
}

func TestHTTPRequestAcceptsResponseAtSafetyLimit(t *testing.T) {
	runner := NewRunner(logr.Discard(), prometheus.NewRegistry(), AuthStore{})
	want := strings.Repeat("x", maxHTTPResponseBytes)
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(want)), Header: make(http.Header)}, nil
	})}

	_, body, err := runner.doHTTPRequest(client, "http://example.test", http.MethodGet, nil, "", nil)
	if err != nil || string(body) != want {
		t.Fatalf("boundary response error=%v bytes=%d, want %d", err, len(body), len(want))
	}
}
