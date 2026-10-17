package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"free-ai-router/internal/provider"
	"free-ai-router/internal/state"
)

// stubProvider — провайдер-подставка.
//
// Задаёт ровно то, что нужно для проверки шлюза: адрес, ответ и разбор отказа.
// Настоящий провайдер здесь не нужен и мешал бы: проверяется поведение шлюза,
// а не контракт конкретного сервиса.
type stubProvider struct {
	id      provider.ID
	models  []provider.Model
	build   func(provider.Request) (provider.Prepared, error)
	classif func(*http.Response, []byte) provider.Failure
}

func (s *stubProvider) ID() provider.ID { return s.id }

func (s *stubProvider) Models(_ context.Context) ([]provider.Model, error) {
	return s.models, nil
}

func (s *stubProvider) Build(req provider.Request) (provider.Prepared, error) {
	if s.build != nil {
		return s.build(req)
	}
	return provider.Prepared{
		URL:     "http://unused.invalid/v1/chat/completions",
		Headers: make(http.Header),
		Body:    []byte(`{}`),
	}, nil
}

func (s *stubProvider) Classify(resp *http.Response, body []byte) provider.Failure {
	if s.classif != nil {
		return s.classif(resp, body)
	}
	return provider.Failure{Status: resp.StatusCode, Body: body, Headers: resp.Header}
}

// newTestHandler собирает обработчик с подставкой и записывающим транспортом.
func newTestHandler(t *testing.T, upstream *httptest.Server) (*Handler, *stubProvider, *state.Store) {
	t.Helper()

	p := &stubProvider{
		id:     "test",
		models: []provider.Model{{ID: "модель", Context: 1000, MaxOutput: 100, Free: true}},
		build: func(provider.Request) (provider.Prepared, error) {
			return provider.Prepared{
				URL:     upstream.URL + "/chat/completions",
				Headers: http.Header{"content-type": []string{"application/json"}},
				Body:    []byte(`{"model":"модель"}`),
			}, nil
		},
	}

	reg := provider.NewRegistry()
	reg.Register(p)

	st, err := state.Open(t.TempDir() + "/state.json")
	if err != nil {
		t.Fatal(err)
	}

	h := &Handler{
		Providers: reg,
		Transport: fixedTransport{rt: upstream.Client().Transport},
		State:     st,
	}
	return h, p, st
}

// fixedTransport отдаёт заранее заданный транспорт.
type fixedTransport struct{ rt http.RoundTripper }

func (f fixedTransport) RoundTripper() (http.RoundTripper, error) { return f.rt, nil }

// postChat отправляет запрос к обработчику и возвращает ответ.
func postChat(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.handleChat(rec, req)
	return rec
}

// TestRefusalPassesThroughVerbatim — ГЛАВНОЕ свойство шлюза.
//
// Отказ провайдера обязан дойти до клиента байт в байт: тот же статус, то же
// тело, те же заголовки. Клиент ищет в теле признаки, по которым принимает
// решение о повторе, и переупаковка лишила бы его этих признаков.
func TestRefusalPassesThroughVerbatim(t *testing.T) {
	const body = `{"error":{"type":"FreeTierError","code":"free_tier","message":"только изнутри"}}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.Header().Set("retry-after", "42")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, body)
	}))
	defer upstream.Close()

	h, _, st := newTestHandler(t, upstream)
	rec := postChat(t, h, `{"model":"test/модель","messages":[{"role":"user","content":"привет"}]}`)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if got := rec.Body.String(); got != body {
		t.Fatalf("body changed:\n got %s\nwant %s", got, body)
	}
	if got := rec.Header().Get("retry-after"); got != "42" {
		t.Fatalf("retry-after = %q, want it forwarded", got)
	}

	// И отказ записан в счётчики.
	snap := st.Snapshot()
	if len(snap.Days) != 1 || snap.Days[0].Refused != 1 {
		t.Fatalf("the refusal was not counted: %+v", snap.Days)
	}
}

// TestSuccessIsCounted — успешный запрос попадает в счётчики.
func TestSuccessIsCounted(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[]}\n\n")
	}))
	defer upstream.Close()

	h, _, st := newTestHandler(t, upstream)
	rec := postChat(t, h, `{"model":"test/модель","messages":[{"role":"user","content":"привет"}]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	snap := st.Snapshot()
	if len(snap.Days) != 1 || snap.Days[0].OK != 1 {
		t.Fatalf("the success was not counted: %+v", snap.Days)
	}
	if len(snap.Days[0].ByModel) != 1 || snap.Days[0].ByModel[0].Model != "модель" {
		t.Fatalf("the model rollup is wrong: %+v", snap.Days[0].ByModel)
	}
}

// TestStreamIsForwardedIncrementally — поток отдаётся по мере поступления, а
// не копится до конца: иначе стриминг бессмыслен.
func TestStreamIsForwardedIncrementally(t *testing.T) {
	const chunk = "data: {\"choices\":[{\"delta\":{\"content\":\"привет\"}}]}\n\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for i := 0; i < 3; i++ {
			_, _ = io.WriteString(w, chunk)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer upstream.Close()

	h, _, _ := newTestHandler(t, upstream)
	rec := postChat(t, h, `{"model":"test/модель","messages":[{"role":"user","content":"x"}]}`)

	if got := strings.Count(rec.Body.String(), chunk); got != 3 {
		t.Fatalf("forwarded %d chunks, want 3", got)
	}
	if ct := rec.Header().Get("content-type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}
}

// TestUnknownProviderRejected — незнакомый провайдер отвергается с понятным
// сообщением.
func TestUnknownProviderRejected(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	h, _, _ := newTestHandler(t, upstream)
	rec := postChat(t, h, `{"model":"незнакомый/модель","messages":[{"role":"user","content":"x"}]}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var env map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("the error body is not JSON: %v", err)
	}
	if _, ok := env["error"]; !ok {
		t.Fatalf("the error envelope lacks the error field: %s", rec.Body.String())
	}
}

// TestUnknownModelRejected — модель, которой нет в каталоге, отвергается:
// каталог это то, что провайдер объявил.
func TestUnknownModelRejected(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	h, _, _ := newTestHandler(t, upstream)
	rec := postChat(t, h, `{"model":"test/нет-такой","messages":[{"role":"user","content":"x"}]}`)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
