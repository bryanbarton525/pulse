package proberunner

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bryanbarton525/pulse/internal/observation"
)

type authRoundTripFunc func(*http.Request) (*http.Response, error)

func (function authRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestHTTPShipperAuthenticatesObservationPush(t *testing.T) {
	t.Parallel()

	var authorization string
	token := NewInternalToken("shared-secret")
	shipper := &HTTPShipper{
		endpoint:    "http://incident-engine/observations",
		shard:       "0",
		tokenSource: token,
		logger:      logr.Discard(),
		client: &http.Client{Transport: authRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			authorization = request.Header.Get("Authorization")
			return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader(""))}, nil
		})},
	}

	shipper.send([]observation.Observation{{Probe: "shop/catalogue"}})
	if authorization != "Bearer shared-secret" {
		t.Fatalf("Authorization = %q, want bearer token", authorization)
	}
	token.Set("rotated-secret")
	shipper.send([]observation.Observation{{Probe: "shop/catalogue"}})
	if authorization != "Bearer rotated-secret" {
		t.Fatalf("Authorization after rotation = %q, want rotated bearer token", authorization)
	}
}

func TestResultPusherAuthenticatesResultPush(t *testing.T) {
	t.Parallel()

	var authorization string
	token := NewInternalToken("shared-secret")
	pusher := &ResultPusher{
		endpoint:    "http://incident-engine/results",
		shard:       "0",
		tokenSource: token,
		logger:      logr.Discard(),
		runner:      NewRunner(logr.Discard(), prometheus.NewRegistry(), AuthStore{}),
		client: &http.Client{Transport: authRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			authorization = request.Header.Get("Authorization")
			return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader(""))}, nil
		})},
	}

	pusher.push(context.Background())
	if authorization != "Bearer shared-secret" {
		t.Fatalf("Authorization = %q, want bearer token", authorization)
	}
	token.Set("rotated-secret")
	pusher.push(context.Background())
	if authorization != "Bearer rotated-secret" {
		t.Fatalf("Authorization after rotation = %q, want rotated bearer token", authorization)
	}
}
