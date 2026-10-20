package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"free-ai-router/internal/provider"
	"free-ai-router/internal/state"
)

// modelStub is a provider whose catalog and whose answer both depend on the
// model, which is what a fallback is about.
type modelStub struct {
	models []provider.Model
	url    string

	mu   sync.Mutex
	sent []string
}

func (s *modelStub) ID() provider.ID { return "test" }

func (s *modelStub) Models(_ context.Context) ([]provider.Model, error) { return s.models, nil }

func (s *modelStub) Build(req provider.Request) (provider.Prepared, error) {
	s.mu.Lock()
	s.sent = append(s.sent, req.Model.ID)
	s.mu.Unlock()
	return provider.Prepared{
		URL:     s.url,
		Headers: http.Header{"content-type": []string{"application/json"}},
		Body:    []byte(`{"model":"` + req.Model.ID + `"}`),
	}, nil
}

func (s *modelStub) Classify(resp *http.Response, body []byte) provider.Failure {
	// The real classification reduced to the one judgement under test: the
	// endpoint-unavailable body is read as such whatever the status says.
	f := provider.Failure{Status: resp.StatusCode, Body: body, Headers: resp.Header}
	if strings.Contains(strings.ToLower(string(body)), "endpoint is unavailable") {
		f.Retryable = true
		f.UpstreamUnavailable = true
	}
	return f
}

// newFallbackHandler wires a handler whose upstream answers per model.
func newFallbackHandler(t *testing.T, p *modelStub, answer func(model string) (int, string)) (*Handler, *state.Store) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &req)
		status, body := answer(req.Model)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	p.url = server.URL

	reg := provider.NewRegistry()
	reg.Register(p)

	st, err := state.Open(t.TempDir() + "/state.json")
	if err != nil {
		t.Fatal(err)
	}

	h := &Handler{
		Providers: reg,
		Transport: fixedTransport{rt: server.Client().Transport},
		State:     st,
	}
	return h, st
}

const unavailableBody = `{"error":{"type":"server_error","message":"Upstream request failed: Endpoint is unavailable."}}`

// TestUnavailableModelMovesTheRequestToAnother — the model host is out, and a
// provider serves its free models from separate endpoints.
//
// Measured on 2026-10-20: `deepseek-v4.1-flash-free` answered with "Upstream
// request failed: Endpoint is unavailable" while `glm-5.3-flash-free`,
// `mimo-v2.6-flash-free`, `minimax-m3.1-flash-free` and
// `longcat-2.5-preview-free` answered 200 in the same second. Repeating the same
// request on the same model could only wait on the same dead endpoint, so the
// request moves.
func TestUnavailableModelMovesTheRequestToAnother(t *testing.T) {
	p := &modelStub{
		models: []provider.Model{
			{ID: "dead", Free: true},
			{ID: "alive", Free: true},
		},
	}

	h, st := newFallbackHandler(t, p, func(model string) (int, string) {
		if model == "dead" {
			return http.StatusServiceUnavailable, unavailableBody
		}
		return http.StatusOK, "data: {\"choices\":[]}\n\ndata: [DONE]\n\n"
	})

	rec := postChat(t, h, `{"model":"test/dead","messages":[{"role":"user","content":"hello"}]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := strings.Join(p.sent, ","); got != "dead,alive" {
		t.Fatalf("the request went to %q, want \"dead,alive\"", got)
	}

	// The refused attempt is counted against the model that refused and the
	// success against the model that answered: the counters describe attempts.
	snap := st.Snapshot()
	if len(snap.Days) != 1 {
		t.Fatalf("days = %d, want 1", len(snap.Days))
	}
	if snap.Days[0].OK != 1 || snap.Days[0].Refused != 1 {
		t.Fatalf("ok/refused = %d/%d, want 1/1", snap.Days[0].OK, snap.Days[0].Refused)
	}
}

// TestFallbackIsBoundedAndNeverCycles — a catalog where every model is out must
// not be walked in full, must not be repeated, and must not spend money.
//
// The moves are capped because the client waits through every one of them: a
// catalog of ten dead models would otherwise cost the client a minute per
// request with nothing to show for it. A paid model is never a fallback — the
// router exists to spend the free lane, and a fallback that bills is not an
// improvement.
func TestFallbackIsBoundedAndNeverCycles(t *testing.T) {
	p := &modelStub{
		models: []provider.Model{
			{ID: "a", Free: true},
			{ID: "b", Free: true},
			{ID: "c", Free: true},
			{ID: "d", Free: true},
			{ID: "paid"},
		},
	}

	h, _ := newFallbackHandler(t, p, func(string) (int, string) {
		return http.StatusServiceUnavailable, unavailableBody
	})

	rec := postChat(t, h, `{"model":"test/a","messages":[{"role":"user","content":"hello"}]}`)

	// The client still gets the provider's own refusal, verbatim.
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if len(p.sent) != modelFallbacks+1 {
		t.Fatalf("sent %d times (%v), want %d", len(p.sent), p.sent, modelFallbacks+1)
	}
	seen := map[string]bool{}
	for _, id := range p.sent {
		if seen[id] {
			t.Fatalf("model %q was tried twice: %v", id, p.sent)
		}
		seen[id] = true
	}
	if seen["paid"] {
		t.Fatalf("a paid model was used: %v", p.sent)
	}
}

// TestUnavailableBodyIsNotBlamedOnTheKey — a 403 carrying the endpoint message is
// an upstream outage, not a credential refusal, whatever the status says.
//
// The status of this one condition is not fixed: 503 when the provider's edge
// answered, 403 and 429 when an earlier hop did. Reading the status here would
// split one condition into three verdicts, and the 403 reading would spend a key
// pool on a model that cannot reach its own host.
func TestUnavailableBodyIsNotBlamedOnTheKey(t *testing.T) {
	p := &modelStub{
		models: []provider.Model{{ID: "dead", Free: true}},
	}

	h, _ := newFallbackHandler(t, p, func(string) (int, string) {
		return http.StatusForbidden, unavailableBody
	})

	rec := postChat(t, h, `{"model":"test/dead","messages":[{"role":"user","content":"hello"}]}`)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if rec.Body.String() != unavailableBody {
		t.Fatalf("the body was repackaged: %s", rec.Body.String())
	}
}
