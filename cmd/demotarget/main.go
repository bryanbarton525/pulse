// Command demotarget provides deterministic HTTP, MCP, and gRPC services for
// the local quick-start without installing packages at container startup.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

const demoCatalogueOutageStatus = 529
const demoSessionCookie = "pulse-demo-session"

type behaviorState struct {
	value atomic.Value
}

func newBehaviorState(initial string) *behaviorState {
	state := &behaviorState{}
	state.value.Store(initial)
	return state
}

func (s *behaviorState) Get() string {
	return s.value.Load().(string)
}

func (s *behaviorState) Set(value string) {
	s.value.Store(value)
}

func main() {
	var service string
	flag.StringVar(&service, "service", env("DEMO_SERVICE", "catalogue"), "demo service role")
	flag.Parse()
	behavior := newBehaviorState(env("DEMO_BEHAVIOR", "healthy"))
	if service == "grpc" {
		runGRPC(behavior)
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	registerBehaviorControl(mux, behavior, nil)
	switch service {
	case "catalogue":
		catalogueRoutes(mux, behavior)
	case "downstream":
		downstreamRoutes(mux, behavior)
	case "control":
		controlRoutes(mux, behavior)
	case "mcp":
		mcpRoutes(mux, behavior)
	default:
		log.Fatalf("unsupported DEMO_SERVICE %q", service)
	}
	server := &http.Server{Addr: ":8080", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("starting %s demo target with behavior %s", service, behavior.Get())
	log.Fatal(server.ListenAndServe())
}

func catalogueRoutes(mux *http.ServeMux, behavior *behaviorState) {
	mux.HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) {
		behavior := behavior.Get()
		if behavior == "slow" {
			time.Sleep(2 * time.Second)
		}
		if behavior == "outage" {
			// A distinctive status keeps this intentional root failure separate
			// from incidental 503s a caller may observe during a local rollout.
			http.Error(w, "catalogue database unavailable", demoCatalogueOutageStatus)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if behavior == "green" {
			_, _ = w.Write([]byte(`{"items":[],"total":0}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"id":1,"name":"widget"},{"id":2,"name":"gadget"}],"total":2}`))
	})
	mux.HandleFunc("GET /health/no-content", func(w http.ResponseWriter, _ *http.Request) {
		behavior := behavior.Get()
		if behavior == "no-content-fail" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("unexpected body"))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /login", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name:     demoSessionCookie,
			Value:    "authenticated",
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
		_, _ = w.Write([]byte("<html><body><h1>Sign in</h1></body></html>"))
	})
	mux.HandleFunc("GET /session", func(w http.ResponseWriter, request *http.Request) {
		behavior := behavior.Get()
		w.Header().Set("Content-Type", "application/json")
		cookie, err := request.Cookie(demoSessionCookie)
		if err != nil || cookie.Value != "authenticated" {
			http.Error(w, `{"error":"missing demo session"}`, http.StatusUnauthorized)
			return
		}
		if behavior == "journey-fail" {
			_, _ = w.Write([]byte(`{"user":"demo","state":"guest"}`))
			return
		}
		_, _ = w.Write([]byte(`{"user":"demo","authenticated":true}`))
	})
}

func downstreamRoutes(mux *http.ServeMux, behavior *behaviorState) {
	upstream := env("CATALOGUE_URL", "http://catalogue.shop.svc:8080/")
	client := &http.Client{Timeout: 3 * time.Second}
	mux.HandleFunc("GET /", func(w http.ResponseWriter, request *http.Request) {
		behavior := behavior.Get()
		if behavior == "outage" {
			http.Error(w, "local downstream failure", http.StatusInternalServerError)
			return
		}
		upstreamRequest, err := http.NewRequestWithContext(request.Context(), http.MethodGet, upstream, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		response, err := client.Do(upstreamRequest)
		if err != nil {
			http.Error(w, "catalogue request failed: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != http.StatusOK {
			http.Error(w, fmt.Sprintf("catalogue returned %d", response.StatusCode), http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"downstream","ready":true}`))
	})
}

func controlRoutes(mux *http.ServeMux, behavior *behaviorState) {
	mux.HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) {
		behavior := behavior.Get()
		if behavior == "control-fail" {
			_, _ = w.Write([]byte(`{"state":"degraded-control"}`))
			return
		}
		_, _ = w.Write([]byte(`{"state":"healthy-control"}`))
	})
	mux.HandleFunc("GET /similar", func(w http.ResponseWriter, _ *http.Request) {
		behavior := behavior.Get()
		if behavior == "similarity-fail" {
			_, _ = w.Write([]byte(`{"state":"shared-broken"}`))
			return
		}
		_, _ = w.Write([]byte(`{"state":"shared-ready"}`))
	})
}

func mcpRoutes(mux *http.ServeMux, behavior *behaviorState) {
	type request struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
	}
	mux.HandleFunc("POST /mcp", func(w http.ResponseWriter, httpRequest *http.Request) {
		behavior := behavior.Get()
		var incoming request
		if err := json.NewDecoder(httpRequest.Body).Decode(&incoming); err != nil {
			http.Error(w, "invalid JSON-RPC request", http.StatusBadRequest)
			return
		}
		if incoming.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		result := any(map[string]any{})
		switch incoming.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": "2025-11-25",
				"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
				"serverInfo":      map[string]string{"name": "pulse-demo-mcp", "version": "1.0.0"},
			}
		case "tools/list":
			tools := []map[string]string{{"name": "orders.lookup", "description": "Look up an order"}}
			if behavior != "mcp-missing-tool" {
				tools = append(tools, map[string]string{"name": "health.check", "description": "Report service health"})
			}
			result = map[string]any{"tools": tools}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": incoming.ID, "result": result})
	})
}

func runGRPC(behavior *behaviorState) {
	listener, err := net.Listen("tcp", ":50051")
	if err != nil {
		log.Fatal(err)
	}
	grpcServer := grpc.NewServer()
	healthServer := health.NewServer()
	setStatus := func(value string) {
		status := healthpb.HealthCheckResponse_SERVING
		if value == "grpc-fail" {
			status = healthpb.HealthCheckResponse_NOT_SERVING
		}
		healthServer.SetServingStatus("", status)
		healthServer.SetServingStatus("shop.Orders", status)
	}
	setStatus(behavior.Get())
	healthpb.RegisterHealthServer(grpcServer, healthServer)
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		registerBehaviorControl(mux, behavior, setStatus)
		_ = http.ListenAndServe(":8080", mux)
	}()
	log.Printf("starting grpc demo target with behavior %s", behavior.Get())
	log.Fatal(grpcServer.Serve(listener))
}

func registerBehaviorControl(mux *http.ServeMux, state *behaviorState, changed func(string)) {
	type request struct {
		Behavior string `json:"behavior"`
	}
	mux.HandleFunc("POST /__control", func(w http.ResponseWriter, httpRequest *http.Request) {
		var incoming request
		if err := json.NewDecoder(httpRequest.Body).Decode(&incoming); err != nil || incoming.Behavior == "" {
			http.Error(w, "behavior is required", http.StatusBadRequest)
			return
		}
		state.Set(incoming.Behavior)
		if changed != nil {
			changed(incoming.Behavior)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"behavior": state.Get()})
	})
	mux.HandleFunc("GET /__control", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"behavior": state.Get()})
	})
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
