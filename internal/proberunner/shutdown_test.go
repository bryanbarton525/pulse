package proberunner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
)

// The shutdown sequence used to be able to panic the process.
//
// Runner.Stop only cancelled the context without waiting, so a probe already
// inside its HTTP call kept running. The binary then closed the shipper's
// queue, and the in-flight check reported its result into a closed channel --
// which panics even inside a select with a default arm.
//
// Run this with -race to also catch the ordering hazard.
func TestStopWaitsForInFlightChecksBeforeTheShipperCloses(t *testing.T) {
	t.Parallel()

	// A deliberately slow endpoint, so a check is guaranteed to be mid-flight
	// when shutdown begins.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer slow.Close()

	var received int
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer engine.Close()

	shipper := NewHTTPShipper(ShipperOptions{
		Endpoint: engine.URL, Shard: "0", Logger: logr.Discard(),
	})

	registry := prometheus.NewRegistry()
	runner := NewRunner(logr.Discard(), registry, AuthStore{})
	runner.SetIntelligence(NewIntelligence(
		nil, shipper, logr.Discard(), NewIntelligenceMetrics(registry)))

	config := &ProbeConfig{Probes: []Probe{{
		Name: "default/slow", Type: ProbeTypeHTTP, URL: slow.URL,
		Interval: 1, ExpectedStatus: 200,
		Intelligence: &ProbeIntelligence{Policy: "pulse-system/test"},
	}}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runner.Start(ctx, config)

	// Stop while the first check is still inside its HTTP call.
	time.Sleep(30 * time.Millisecond)

	if drained := runner.Stop(); !drained {
		t.Fatal("Stop() reported that probes were still running past the deadline")
	}

	// Safe now precisely because Stop waited.
	shipper.Stop()
}

// Even with the lifecycle correct, Ship must never panic if it races Stop.
// This is the defence-in-depth half of the fix.
func TestShipAfterStopIsSafe(t *testing.T) {
	t.Parallel()

	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer engine.Close()

	shipper := NewHTTPShipper(ShipperOptions{
		Endpoint: engine.URL, Shard: "0", Logger: logr.Discard(),
	})
	shipper.Stop()

	// Would panic on a closed channel before the stopped guard existed.
	shipper.Ship(context.Background(), testObservation())
}

// Concurrent Ship and Stop must not race or panic.
func TestConcurrentShipAndStop(t *testing.T) {
	t.Parallel()

	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer engine.Close()

	shipper := NewHTTPShipper(ShipperOptions{
		Endpoint: engine.URL, Shard: "0", Logger: logr.Discard(),
	})

	var group sync.WaitGroup
	for range 32 {
		group.Add(1)
		go func() {
			defer group.Done()
			for range 50 {
				shipper.Ship(context.Background(), testObservation())
			}
		}()
	}

	time.Sleep(5 * time.Millisecond)
	shipper.Stop()
	group.Wait()
}

// Stop must be safe to call more than once, since shutdown paths can overlap.
func TestStopIsIdempotent(t *testing.T) {
	t.Parallel()

	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer engine.Close()

	shipper := NewHTTPShipper(ShipperOptions{
		Endpoint: engine.URL, Shard: "0", Logger: logr.Discard(),
	})

	shipper.Stop()
	shipper.Stop()
}

// A reload must not leave the previous generation of probe goroutines running
// alongside the new one, or every probe is executed twice for a while.
func TestReloadWaitsForThePreviousGeneration(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	inFlight := 0
	peak := 0

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		mu.Unlock()

		time.Sleep(80 * time.Millisecond)

		mu.Lock()
		inFlight--
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	runner := NewRunner(logr.Discard(), prometheus.NewRegistry(), AuthStore{})
	config := &ProbeConfig{Probes: []Probe{{
		Name: "default/one", Type: ProbeTypeHTTP, URL: target.URL,
		Interval: 1, ExpectedStatus: 200,
	}}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runner.Start(ctx, config)
	time.Sleep(20 * time.Millisecond)

	// Reload while the first check is still running.
	runner.Reload(ctx, config, AuthStore{})
	time.Sleep(120 * time.Millisecond)
	runner.Stop()

	mu.Lock()
	defer mu.Unlock()
	if peak > 1 {
		t.Fatalf("peak concurrent checks for one probe = %d, want 1 — "+
			"reload started a new goroutine before the old one finished", peak)
	}
}
