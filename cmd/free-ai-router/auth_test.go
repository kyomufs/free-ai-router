package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"free-ai-router/internal/config"
)

// newServerConfig возвращает конфигурацию в режиме server с токеном.
func newServerConfig(t *testing.T, token string) *config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Mode.Set(config.ModeServer, config.SourceDefault)
	cfg.Token.Set(token, config.SourceDefault)
	return cfg
}

// TestTokenCoversEveryPath — главное свойство проверки доступа.
//
// Проверка стоит ВОКРУГ маршрутизатора, а не внутри одной из поверхностей,
// поэтому закрыт любой путь. Это не деталь: раньше проверка была только в
// управлении, и поверхность моделей отвечала кому угодно, тратя квоту
// оператора.
func TestTokenCoversEveryPath(t *testing.T) {
	inside := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := requireToken(inside, newServerConfig(t, "секрет"))

	paths := []string{
		"/v1/models",
		"/v1/chat/completions",
		"/_control/status",
		"/_control/keys",
		"/p/opencode/v1/models",
		"/что-угодно",
	}
	for _, path := range paths {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s without a token = %d, want 401", path, rec.Code)
		}
	}
}

// TestTokenAcceptsBearerAndBare — принимаются обе формы заголовка.
//
// Разные клиенты ведут себя по-разному, и требовать строго одну форму значило
// бы ломать чужой код без причины.
func TestTokenAcceptsBearerAndBare(t *testing.T) {
	inside := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := requireToken(inside, newServerConfig(t, "секрет"))

	for _, header := range []string{"Bearer секрет", "секрет"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("authorization", header)
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("authorization %q = %d, want 200", header, rec.Code)
		}
	}
}

// TestTokenRejectsWrongValue — неверный токен не проходит.
func TestTokenRejectsWrongValue(t *testing.T) {
	inside := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := requireToken(inside, newServerConfig(t, "секрет"))

	for _, header := range []string{"", "Bearer", "Bearer неверный", "сек", "секрет2", "Bearer секрет "} {
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

// TestLocalModeSkipsTheCheck — в режиме local проверка не выполняется вовсе.
//
// До loopback-слушателя кроме этой машины никто не дотянется, и требовать
// токен там значило бы усложнять работу без причины.
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

// TestResponseForm — отказ приходит в общей форме шлюза.
func TestResponseForm(t *testing.T) {
	inside := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	rec := httptest.NewRecorder()
	requireToken(inside, newServerConfig(t, "секрет")).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))

	if ct := rec.Header().Get("content-type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
	if !contains(rec.Body.String(), `"AuthError"`) {
		t.Fatalf("the refusal lacks a machine-readable class: %s", rec.Body.String())
	}
}

// contains сообщает, содержит ли строка подстроку.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
