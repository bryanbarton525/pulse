package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCatalogueJourneyRequiresCookieFromLogin(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	catalogueRoutes(mux, "healthy")
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
	catalogueRoutes(mux, "journey-fail")
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
