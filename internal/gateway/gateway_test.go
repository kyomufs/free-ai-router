package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"free-ai-router/internal/provider"
	"free-ai-router/internal/state"
	"free-ai-router/internal/wire"
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

// fixedAddress is an address resolver that always reports the same address.
type fixedAddress string

func (f fixedAddress) Address() string { return string(f) }

// TestUnreachableUpstreamIsAbortedNotRefused — an unreachable upstream is not
// the provider refusing anything.
//
// The two used to share one counter. The error column of the monitor is read as
// the provider's fault, so a transport failure counted there blames the
// provider for a broken path.
func TestUnreachableUpstreamIsAbortedNotRefused(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer upstream.Close()

	h, p, st := newTestHandler(t, upstream)

	// Point the provider at an address nothing listens on.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	p.build = func(provider.Request) (provider.Prepared, error) {
		return provider.Prepared{
			URL:     deadURL + "/chat/completions",
			Headers: http.Header{"content-type": []string{"application/json"}},
			Body:    []byte(`{"model":"model"}`),
		}, nil
	}

	rec := postChat(t, h, `{"model":"test/model","messages":[{"role":"user","content":"hello"}]}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}

	day := st.Snapshot().Days[0]
	if day.Aborted != 1 {
		t.Fatalf("Aborted = %d, want 1", day.Aborted)
	}
	if day.Refused != 0 {
		t.Fatalf("Refused = %d, want 0: an unreachable upstream is not a refusal", day.Refused)
	}
}

// TestClientCancellationIsAbortedNotRefused — a client that gives up is not the
// provider refusing anything.
//
// The live log contains lines like
// `provider "opencode": connection: Post "...": context canceled`, and those
// landed in the refusal counter before.
func TestClientCancellationIsAbortedNotRefused(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hold the request open until the client cancels it.
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer upstream.Close()
	defer close(release)

	h, _, st := newTestHandler(t, upstream)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"test/model","messages":[{"role":"user","content":"hello"}]}`)).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		h.handleChat(rec, req)
		close(done)
	}()
	cancel()
	<-done

	day := st.Snapshot().Days[0]
	if day.Aborted != 1 {
		t.Fatalf("Aborted = %d, want 1", day.Aborted)
	}
	if day.Refused != 0 {
		t.Fatalf("Refused = %d, want 0: a cancellation is not a provider refusal", day.Refused)
	}
}

// TestAnonymousRefusalDoesNotBlameTheKey — with no credential configured there
// is nothing to rotate, so the provider's verdict on the key is discarded.
//
// A provider answers 401/403 for a bare, keyless request as well, and in
// anonymous mode the gateway substitutes an anonymity marker. Reporting that as
// a rejected key is a lie in the log, and once a key pool exists it would burn
// the whole pool on an address-level refusal.
func TestAnonymousRefusalDoesNotBlameTheKey(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"type":"Forbidden","message":"no"}}`)
	}))
	defer upstream.Close()

	h, p, st := newTestHandler(t, upstream)

	// The provider blames the key, the way a real one does on any 401/403.
	p.classif = func(resp *http.Response, body []byte) provider.Failure {
		return provider.Failure{
			Status:      resp.StatusCode,
			Body:        body,
			Headers:     resp.Header,
			KeyRejected: true,
		}
	}

	logPath := t.TempDir() + "/app.log"
	logger, err := state.OpenLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	h.Log = logger

	postChat(t, h, `{"model":"test/model","messages":[{"role":"user","content":"hello"}]}`)

	// It is still a refusal: the provider did refuse this request.
	if day := st.Snapshot().Days[0]; day.Refused != 1 {
		t.Fatalf("Refused = %d, want 1", day.Refused)
	}

	// But the log must not claim the key was rejected: none was sent.
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "key rejected: false") {
		t.Fatalf("the log claims a key was rejected in anonymous mode:\n%s", raw)
	}
}

// TestEgressAddressReachesTheCounters — the per-address matrix is drawn from the
// counters, so the address a request left from must be recorded with it.
//
// Behind NAT the local interfaces name an address the provider never sees, so
// the address is asked of the outside and handed to the gateway.
func TestEgressAddressReachesTheCounters(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()

	h, _, st := newTestHandler(t, upstream)
	h.Addresses = fixedAddress("213.14.43.234")

	postChat(t, h, `{"model":"test/model","messages":[{"role":"user","content":"hello"}]}`)

	day := st.Snapshot().Days[0]
	if len(day.ByAddress) != 1 {
		t.Fatalf("ByAddress = %+v, want one row", day.ByAddress)
	}
	if got := day.ByAddress[0].Address; got != "213.14.43.234" {
		t.Fatalf("Address = %q", got)
	}
	if day.ByAddress[0].OK != 1 {
		t.Fatalf("the address row did not record the success: %+v", day.ByAddress[0])
	}
}

// TestUnknownAddressStillCounts — the address is a property of the network.
//
// When it cannot be determined the counters must keep working, and no invented
// row may appear in the matrix: the operator would read it as a real address the
// provider limits.
func TestUnknownAddressStillCounts(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()

	h, _, st := newTestHandler(t, upstream)
	// h.Addresses stays nil: the address is unknown.

	postChat(t, h, `{"model":"test/model","messages":[{"role":"user","content":"hello"}]}`)

	day := st.Snapshot().Days[0]
	if day.OK != 1 {
		t.Fatalf("OK = %d, want 1: an unknown address must not stop the counters", day.OK)
	}
	if len(day.ByAddress) != 0 {
		t.Fatalf("ByAddress = %+v, want no row for an unknown address", day.ByAddress)
	}
}

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
		_, _ = io.WriteString(w, "data: {\"choices\":[]}\n\ndata: [DONE]\n\n")
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
	rec := postChat(t, h, `{"model":"test/model","messages":[{"role":"user","content":"x"}],"stream":true}`)

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

// TestForeignSessionIdsAreReplaced — session identifiers of someone else's shape
// do not reach the upstream.
//
// The admission check reads the session headers too: two requests that differ
// only in the session id were measured on 2026-10-19, and the one carrying
// `ses_test` got a FreeTierError while the one carrying `ses_` + 12 hex + 14
// base62 was answered. A client with its own naming convention (a bare UUID, a
// prefixed counter) therefore fails every call for a reason nobody can see, so
// the value is replaced by an id of the client's own shape.
func TestForeignSessionIdsAreReplaced(t *testing.T) {
	h := &Handler{}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("x-session-id", "3f2a1c9e-0b44-4d7a-9d21-8b1f2c3d4e5f")
	r.Header.Set("x-opencode-session", "ses_test")
	r.Header.Set("x-parent-session-id", "conversation-42")

	ids := h.sessionIDs(r, nil)

	if !wire.IsCanonicalSessionID(ids.Session) {
		t.Errorf("Session = %q, want an id of the client's shape", ids.Session)
	}
	if !wire.IsCanonicalSessionID(ids.Affinity) {
		t.Errorf("Affinity = %q, want an id of the client's shape", ids.Affinity)
	}
	if ids.Parent != "" {
		t.Errorf("Parent = %q, want it dropped instead of forwarded", ids.Parent)
	}
	if ids.Affinity != ids.Session {
		t.Errorf("Affinity = %q, want the session %q when there is no parent", ids.Affinity, ids.Session)
	}
}

// TestClientSessionIdsSurvive — an id the client minted itself is the session
// for the upstream, so it travels unchanged; that is also what keeps prefix
// caching on the same conversation across requests.
func TestClientSessionIdsSurvive(t *testing.T) {
	h := &Handler{}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("x-opencode-session-id", "ses_1a2b3c4d5e6fABCDEFGHIJKLMN")
	r.Header.Set("x-opencode-session", "ses_6f5e4d3c2b1aZYXWVUTSRQPONB")

	ids := h.sessionIDs(r, nil)

	if ids.Session != "ses_1a2b3c4d5e6fABCDEFGHIJKLMN" {
		t.Errorf("Session = %q, want the caller value preserved", ids.Session)
	}
	if ids.Affinity != "ses_6f5e4d3c2b1aZYXWVUTSRQPONB" {
		t.Errorf("Affinity = %q, want the caller value preserved", ids.Affinity)
	}
}

// TestModelCapacitiesAreReported — the catalog describes what the models can do.
//
// The numbers exist in the daemon and nowhere else: a provider's catalog is
// fetched here, and a client asking /v1/models is exactly the party that must
// not guess. Withholding them pushed the guess onto the client, where a
// hardcoded window is wrong in both directions — too large and the client
// overruns the context and sees a refusal it cannot explain, too small and it
// truncates a conversation that would have fit.
func TestModelCapacitiesAreReported(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	h, _, _ := newTestHandler(t, upstream)
	// Two models, one of which declares nothing: the difference between "not
	// declared" and "declared as zero" has to survive into the answer.
	h.Providers.Register(&stubProvider{
		id: "bare",
		models: []provider.Model{
			{
				ID: "vision", Name: "Vision", Context: 200000, MaxOutput: 64000,
				Vision: true, Reasoning: true, Free: true,
				ReasoningOptions: []provider.ReasoningOption{
					// A toggle and a ladder of words, in the provider's own
					// spelling: "off" comes from the switch, the words from the
					// ladder, and the duplicate proves deduplication.
					{Kind: provider.ReasoningToggle},
					{Kind: provider.ReasoningEffort, Values: []string{"low", "high"}},
					{Kind: provider.ReasoningEffort, Values: []string{"high", "max"}},
				},
			},
			{ID: "bare"},
		},
	})

	rec := httptest.NewRecorder()
	h.handleModels(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/models → %d, want 200", rec.Code)
	}

	var body struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("the answer is not JSON: %v", err)
	}

	byID := map[string]map[string]any{}
	for _, entry := range body.Data {
		if id, ok := entry["id"].(string); ok {
			byID[id] = entry
		}
	}

	full, ok := byID["bare/vision"]
	if !ok {
		t.Fatalf("bare/vision is missing from the catalog: %v", rec.Body.String())
	}
	if got := full["context_length"]; got != float64(200000) {
		t.Errorf("context_length = %v, want 200000", got)
	}
	if got := full["max_completion_tokens"]; got != float64(64000) {
		t.Errorf("max_completion_tokens = %v, want 64000", got)
	}
	if got := full["name"]; got != "Vision" {
		t.Errorf("name = %v, want Vision", got)
	}
	if got := full["reasoning"]; got != true {
		t.Errorf("reasoning = %v, want true", got)
	}
	if got := full["free"]; got != true {
		t.Errorf("free = %v, want true", got)
	}
	if got, want := full["input_modalities"], []any{"text", "image"}; !reflect.DeepEqual(got, want) {
		t.Errorf("input_modalities = %v, want %v", got, want)
	}
	// The ladder reaches the client in the provider's spelling, ordered and
	// deduplicated: offering a word the provider never declared is a request it
	// will ignore or refuse.
	if got, want := full["reasoning_efforts"], []any{"off", "low", "high", "max"}; !reflect.DeepEqual(got, want) {
		t.Errorf("reasoning_efforts = %v, want %v", got, want)
	}

	// A model that declared nothing must say nothing, not zero: a client
	// reading 0 would treat the model as unusable instead of unknown.
	bare := byID["bare/bare"]
	if bare == nil {
		t.Fatal("bare/bare is missing from the catalog")
	}
	for _, key := range []string{"context_length", "max_completion_tokens", "reasoning", "reasoning_efforts", "free"} {
		if _, present := bare[key]; present {
			t.Errorf("%s is present on a model that declared nothing: %v", key, bare[key])
		}
	}
	if got, want := bare["input_modalities"], []any{"text"}; !reflect.DeepEqual(got, want) {
		t.Errorf("input_modalities = %v, want %v", got, want)
	}
}
