package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCatalogueJourneyRequiresCookieFromLogin(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	catalogueRoutes(mux, newBehaviorState("healthy"))
	missing := httptest.NewRecorder()
	mux.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/session", nil))
	if missing.Code != http.StatusUnauthorized {
		t.Fatalf("session without cookie returned %d, want 401", missing.Code)
	}

	login := httptest.NewRecorder()
	mux.ServeHTTP(login, httptest.NewRequest(http.MethodGet, "/login", nil))
	cookies := login.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != demoSessionCookie {
		t.Fatalf("login cookies = %v, want %s", cookies, demoSessionCookie)
	}
	sessionRequest := httptest.NewRequest(http.MethodGet, "/session", nil)
	sessionRequest.AddCookie(cookies[0])
	session := httptest.NewRecorder()
	mux.ServeHTTP(session, sessionRequest)
	if session.Code != http.StatusOK {
		t.Fatalf("session after login returned %d, want 200", session.Code)
	}
}

func TestCatalogueJourneyFailureOccursAfterSessionAuthentication(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	catalogueRoutes(mux, newBehaviorState("journey-fail"))
	login := httptest.NewRecorder()
	mux.ServeHTTP(login, httptest.NewRequest(http.MethodGet, "/login", nil))
	sessionRequest := httptest.NewRequest(http.MethodGet, "/session", nil)
	sessionRequest.AddCookie(login.Result().Cookies()[0])
	session := httptest.NewRecorder()
	mux.ServeHTTP(session, sessionRequest)
	if session.Code != http.StatusOK {
		t.Fatalf("journey failure should violate body assertion, got status %d", session.Code)
	}
}

func TestBehaviorControlChangesRoutesWithoutRestart(t *testing.T) {
	t.Parallel()

	state := newBehaviorState("healthy")
	mux := http.NewServeMux()
	registerBehaviorControl(mux, state, nil)
	controlRoutes(mux, state)

	change := httptest.NewRecorder()
	mux.ServeHTTP(change, httptest.NewRequest(http.MethodPost, "/__control",
		strings.NewReader(`{"behavior":"control-fail"}`)))
	if change.Code != http.StatusOK {
		t.Fatalf("control returned %d, want 200", change.Code)
	}

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(response.Body.String(), "degraded-control") {
		t.Fatalf("body = %q, want changed behavior", response.Body.String())
	}
}
