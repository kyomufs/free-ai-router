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

// stubProvider is a provider stand-in.
//
// It sets exactly what is needed to test the gateway: the address, the response
// and the refusal parsing. A real provider is not needed here and would get in
// the way: what is tested is the gateway's behaviour, not the contract of a
// particular service.
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

// newTestHandler assembles a handler with a stand-in and a recording transport.
func newTestHandler(t *testing.T, upstream *httptest.Server) (*Handler, *stubProvider, *state.Store) {
	t.Helper()

	p := &stubProvider{
		id:     "test",
		models: []provider.Model{{ID: "model", Context: 1000, MaxOutput: 100, Free: true}},
		build: func(provider.Request) (provider.Prepared, error) {
			return provider.Prepared{
				URL:     upstream.URL + "/chat/completions",
				Headers: http.Header{"content-type": []string{"application/json"}},
				Body:    []byte(`{"model":"model"}`),
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

// fixedTransport hands out a pre-set transport.
type fixedTransport struct{ rt http.RoundTripper }

func (f fixedTransport) RoundTripper() (http.RoundTripper, error) { return f.rt, nil }

// postChat sends a request to the handler and returns the response.
func postChat(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.handleChat(rec, req)
	return rec
}

// TestRefusalPassesThroughVerbatim is the CENTRAL property of the gateway.
//
// A provider refusal must reach the client byte for byte: the same status, the
// same body, the same headers. The client looks in the body for the signals it
// decides a retry by, and repackaging would deprive it of those signals.
func TestRefusalPassesThroughVerbatim(t *testing.T) {
	const body = `{"error":{"type":"FreeTierError","code":"free_tier","message":"from the inside only"}}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.Header().Set("retry-after", "42")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, body)
	}))
	defer upstream.Close()

	h, _, st := newTestHandler(t, upstream)
	rec := postChat(t, h, `{"model":"test/model","messages":[{"role":"user","content":"hello"}]}`)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if got := rec.Body.String(); got != body {
		t.Fatalf("body changed:\n got %s\nwant %s", got, body)
	}
	if got := rec.Header().Get("retry-after"); got != "42" {
		t.Fatalf("retry-after = %q, want it forwarded", got)
	}

	// And the refusal is recorded in the counters.
	snap := st.Snapshot()
	if len(snap.Days) != 1 || snap.Days[0].Refused != 1 {
		t.Fatalf("the refusal was not counted: %+v", snap.Days)
	}
}

// TestSuccessIsCounted — a successful request makes it into the counters.
func TestSuccessIsCounted(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[]}\n\n")
	}))
	defer upstream.Close()

	h, _, st := newTestHandler(t, upstream)
	rec := postChat(t, h, `{"model":"test/model","messages":[{"role":"user","content":"hello"}]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	snap := st.Snapshot()
	if len(snap.Days) != 1 || snap.Days[0].OK != 1 {
		t.Fatalf("the success was not counted: %+v", snap.Days)
	}
	if len(snap.Days[0].ByModel) != 1 || snap.Days[0].ByModel[0].Model != "model" {
		t.Fatalf("the model rollup is wrong: %+v", snap.Days[0].ByModel)
	}
}

// TestStreamIsForwardedIncrementally — the stream is handed out as it arrives
// rather than accumulated to the end: otherwise streaming is pointless.
func TestStreamIsForwardedIncrementally(t *testing.T) {
	const chunk = "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"
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
	rec := postChat(t, h, `{"model":"test/model","messages":[{"role":"user","content":"x"}]}`)

	if got := strings.Count(rec.Body.String(), chunk); got != 3 {
		t.Fatalf("forwarded %d chunks, want 3", got)
	}
	if ct := rec.Header().Get("content-type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}
}

// TestUnknownProviderRejected — an unknown provider is rejected with a clear
// message.
func TestUnknownProviderRejected(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	h, _, _ := newTestHandler(t, upstream)
	rec := postChat(t, h, `{"model":"unknown/model","messages":[{"role":"user","content":"x"}]}`)

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

// TestUnknownModelRejected — a model that is not in the catalog is rejected:
// the catalog is what the provider has declared.
func TestUnknownModelRejected(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	h, _, _ := newTestHandler(t, upstream)
	rec := postChat(t, h, `{"model":"test/no-such-thing","messages":[{"role":"user","content":"x"}]}`)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// TestRejectedHeaderPrefixesAreReplaced — the two header values the upstream
// refuses outright are replaced, not forwarded.
//
// Both are hard refusals keyed on a PREFIX rather than on a value, so a client
// that sends either shape would fail the whole call over a header nobody
// reads. The measurement behind it, and the admitted substitutes, are recorded
// next to the check itself in parse.go.
func TestRejectedHeaderPrefixesAreReplaced(t *testing.T) {
	h := &Handler{}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("x-opencode-project", "prj_f698f87ea99d390458196363")
	r.Header.Set("x-opencode-request", "req_2cb728ee0be94ed0c7b20920b21fde87")

	ids := h.sessionIDs(r, nil)

	if ids.Project != "global" {
		t.Errorf("Project = %q, want the admitted literal", ids.Project)
	}
	if !strings.HasPrefix(ids.Message, "msg_") {
		t.Errorf("Message = %q, want a derived message id", ids.Message)
	}
}

// TestAdmissibleHeaderValuesSurvive — the replacement must not touch values
// the upstream admits. A check that rewrote everything would pass the test
// above while discarding identifiers the client supplied on purpose.
func TestAdmissibleHeaderValuesSurvive(t *testing.T) {
	h := &Handler{}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("x-opencode-project", "my-project")
	r.Header.Set("x-opencode-request", "msg_13b2d53f2001ia66lVkDLegkmV")

	ids := h.sessionIDs(r, nil)

	if ids.Project != "my-project" {
		t.Errorf("Project = %q, want the caller value preserved", ids.Project)
	}
	if ids.Message != "msg_13b2d53f2001ia66lVkDLegkmV" {
		t.Errorf("Message = %q, want the caller value preserved", ids.Message)
	}
}
