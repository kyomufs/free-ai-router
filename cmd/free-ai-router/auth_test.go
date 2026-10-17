package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"free-ai-router/internal/config"
)

// newServerConfig returns a configuration in server mode with a token.
func newServerConfig(t *testing.T, token string) *config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Mode.Set(config.ModeServer, config.SourceDefault)
	cfg.Token.Set(token, config.SourceDefault)
	return cfg
}

// TestTokenCoversEveryPath — the main property of the access check.
//
// The check sits AROUND the router, not inside one of the surfaces, so every
// path is closed. This is not a detail: previously the check existed only in
// the control surface, and the models surface answered anyone, spending the
// operator's quota.
func TestTokenCoversEveryPath(t *testing.T) {
	inside := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := requireToken(inside, newServerConfig(t, "secret"))

	paths := []string{
		"/v1/models",
		"/v1/chat/completions",
		"/_control/status",
		"/_control/keys",
		"/p/opencode/v1/models",
		"/anything-at-all",
	}
	for _, path := range paths {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s without a token = %d, want 401", path, rec.Code)
		}
	}
}

// TestTokenAcceptsBearerAndBare — both header forms are accepted.
//
// Different clients behave differently, and demanding strictly one form would
// mean breaking someone else's code for no reason.
func TestTokenAcceptsBearerAndBare(t *testing.T) {
	inside := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := requireToken(inside, newServerConfig(t, "secret"))

	for _, header := range []string{"Bearer secret", "secret"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("authorization", header)
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("authorization %q = %d, want 200", header, rec.Code)
		}
	}
}

// TestTokenRejectsWrongValue — a wrong token does not pass.
func TestTokenRejectsWrongValue(t *testing.T) {
	inside := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := requireToken(inside, newServerConfig(t, "secret"))

	for _, header := range []string{"", "Bearer", "Bearer wrong", "sec", "secret2", "Bearer secret "} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		if header != "" {
			req.Header.Set("authorization", header)
		}
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("authorization %q = %d, want 401", header, rec.Code)
		}
	}
}

// TestLocalModeSkipsTheCheck — in local mode the check is not performed at all.
//
// Nobody but this machine can reach a loopback listener, and demanding a token
// there would mean complicating the work for no reason.
func TestLocalModeSkipsTheCheck(t *testing.T) {
	inside := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	cfg := config.Default()
	cfg.Mode.Set(config.ModeLocal, config.SourceDefault)

	rec := httptest.NewRecorder()
	requireToken(inside, cfg).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("local mode = %d, want 200", rec.Code)
	}
}

// TestResponseForm — the refusal arrives in the common gateway shape.
func TestResponseForm(t *testing.T) {
	inside := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	rec := httptest.NewRecorder()
	requireToken(inside, newServerConfig(t, "secret")).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))

	if ct := rec.Header().Get("content-type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
	if !contains(rec.Body.String(), `"AuthError"`) {
		t.Fatalf("the refusal lacks a machine-readable class: %s", rec.Body.String())
	}
}

// contains reports whether the string contains the substring.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
