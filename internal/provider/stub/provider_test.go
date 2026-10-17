package stub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"free-ai-router/internal/provider"
)

// newTestProvider builds a stub from a whole config, so that each check states
// the settings it depends on and nothing else.
func newTestProvider(t *testing.T, cfg Config) *Provider {
	t.Helper()
	if cfg.Models == nil {
		cfg.Models = DefaultConfig().Models
	}
	if cfg.FailStatus == 0 {
		cfg.FailStatus = http.StatusTooManyRequests
	}
	if cfg.Reply == "" {
		cfg.Reply = "stub"
	}
	return NewProvider(DefaultID, "", cfg)
}

// TestDeclaredCatalogIsWhatTheStubServes — the catalog is declared by the stub
// instead of being fetched, and what it declares must be what Models reports.
//
// This is the property the stub exists for: a deployment with a stub has a
// catalog with no service behind it, so if the declared list were not reported
// verbatim, a client would see models nobody declared.
func TestDeclaredCatalogIsWhatTheStubServes(t *testing.T) {
	p := newTestProvider(t, Config{})

	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("Models = %+v, want the single default model", models)
	}
	m := models[0]
	if m.ID != DefaultModel {
		t.Fatalf("model id = %q, want %q", m.ID, DefaultModel)
	}
	// The defaults have to be usable, not merely present: a zero window or a
	// zero ceiling would make the gateway clamp every request to nothing.
	if m.Context <= 0 || m.MaxOutput <= 0 {
		t.Fatalf("the default model came back without usable limits: %+v", m)
	}
	if !m.Free {
		t.Fatal("the default model must be declared free, or a free-model gateway would filter out its own stub")
	}
}

// TestModelsReturnsACopy — a caller writing into the returned slice must not
// change what the provider serves next.
//
// The gateway edits model metadata while routing; without a copy the stub's
// catalog would be silently rewritten by whoever read it.
func TestModelsReturnsACopy(t *testing.T) {
	p := newTestProvider(t, Config{})

	first, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	first[0].ID = "tampered"

	second, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if second[0].ID != DefaultModel {
		t.Fatalf("the catalog became %q after a caller wrote to a returned slice", second[0].ID)
	}
}

// TestNewProviderFillsTheDefaults — a section consisting of nothing but its type
// has to work, which is the promise the Config documentation makes.
//
// An identifier is needed because it becomes a route name; leaving it empty
// would register the provider under an empty name that no client can address.
func TestNewProviderFillsTheDefaults(t *testing.T) {
	p := NewProvider("", "", Config{})

	if p.ID() != DefaultID {
		t.Fatalf("ID = %q, want %q for an unnamed section", p.ID(), DefaultID)
	}

	prep, err := p.Build(provider.Request{Model: provider.Model{ID: DefaultModel}, Stream: true})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// The address must stay inside the reserved, unresolvable name: reaching a
	// real socket would mean the local transport was not wired in, and that has
	// to fail loudly rather than be answered by whatever sits there.
	if !strings.Contains(prep.URL, stubHost) {
		t.Fatalf("URL = %q, want the unresolvable stub host", prep.URL)
	}

	// The request is answered in the process, so no server and no key is needed.
	resp, err := p.RoundTrip(mustRequest(t, prep))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 from a default stub", resp.StatusCode)
	}
}

// TestRefusalIsCountedAndThenStops — the fail setting exists so that a
// deployment can be shown a refusal and then a working service, in that order.
//
// The counter is per provider rather than per request, which is what makes the
// sequence observable from a reset: without that, every request after a restart
// would be refused again.
func TestRefusalIsCountedAndThenStops(t *testing.T) {
	p := newTestProvider(t, Config{Fail: 2})

	for i := 0; i < 2; i++ {
		status, refused := p.Refuse()
		if !refused {
			t.Fatalf("refusal %d: the stub answered normally while fail=%d", i+1, 2)
		}
		if status != http.StatusTooManyRequests {
			t.Fatalf("refusal %d: status = %d, want 429", i+1, status)
		}
	}

	if _, refused := p.Refuse(); refused {
		t.Fatal("the stub refused a request past the declared count")
	}
	if got := p.Requests(); got != 3 {
		t.Fatalf("Requests = %d, want 3: every call is recorded, refused or not", got)
	}
}

// TestClassifyReadsTheVerdictOffTheStatus — the two verdicts answer different
// machinery, so they must not be folded into one another.
//
// A rejected key is answered by taking another key; a "not now" is answered by
// waiting. Sending the rotation machinery after a rate limit burns a key for
// nothing, and waiting on a rejected key waits for something that will not
// arrive.
func TestClassifyReadsTheVerdictOffTheStatus(t *testing.T) {
	p := newTestProvider(t, Config{})

	cases := []struct {
		name        string
		status      int
		body        string
		keyRejected bool
		retryable   bool
	}{
		{"a rejected key is not a reason to wait", http.StatusUnauthorized, `{"error":{"message":"bad key"}}`, true, false},
		{"a forbidden key is the same verdict", http.StatusForbidden, `{"error":{"message":"no access"}}`, true, false},
		{"a rate limit is a reason to wait, not to rotate", http.StatusTooManyRequests, `{"error":{"message":"rate limit exceeded"}}`, false, true},
		{"an exhausted allowance is not restored by waiting", http.StatusTooManyRequests, `{"error":{"message":"insufficient quota"}}`, true, false},
		{"a server fault is worth repeating", http.StatusBadGateway, `{"error":{"message":"upstream"}}`, false, true},
		{"a bad request is worth neither", http.StatusBadRequest, `{"error":{"message":"malformed"}}`, false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{StatusCode: tc.status, Header: make(http.Header)}
			f := p.Classify(resp, []byte(tc.body))

			if f.Status != tc.status {
				t.Fatalf("Status = %d, want %d", f.Status, tc.status)
			}
			// The body goes to the client unchanged: the client parses its own
			// signals out of it, and a rewritten body would take those away.
			if string(f.Body) != tc.body {
				t.Fatalf("Body = %q, want the response verbatim", f.Body)
			}
			if f.KeyRejected != tc.keyRejected {
				t.Fatalf("KeyRejected = %v, want %v", f.KeyRejected, tc.keyRejected)
			}
			if f.Retryable != tc.retryable {
				t.Fatalf("Retryable = %v, want %v", f.Retryable, tc.retryable)
			}
		})
	}
}

// TestClassifyParsesTheAnnouncedWait — a refusal may announce how long to wait,
// and the rotation machinery reads that instead of its own guess.
func TestClassifyParsesTheAnnouncedWait(t *testing.T) {
	p := newTestProvider(t, Config{})

	header := make(http.Header)
	header.Set("Retry-After", "7")
	f := p.Classify(&http.Response{StatusCode: http.StatusTooManyRequests, Header: header}, []byte(`{}`))

	if f.RetryAfter.Seconds() != 7 {
		t.Fatalf("RetryAfter = %v, want 7s from the header", f.RetryAfter)
	}
	if !f.Retryable {
		t.Fatal("a rate limit with an announced wait is still a reason to repeat")
	}

	// A header that cannot be read must leave the wait at zero rather than
	// inventing one: the rotation machinery then falls back to its own delay.
	silent := p.Classify(&http.Response{StatusCode: http.StatusTooManyRequests, Header: make(http.Header)}, []byte(`{}`))
	if silent.RetryAfter != 0 {
		t.Fatalf("RetryAfter = %v, want none when the header is absent", silent.RetryAfter)
	}
}

// TestReplyIsTheAnswer — the assistant text of an answer is the configured
// reply, so a client can tell the stub's answer apart from a real one in a log.
func TestReplyIsTheAnswer(t *testing.T) {
	p := newTestProvider(t, Config{Reply: "from the stub"})

	prep, err := p.Build(provider.Request{
		Model:    provider.Model{ID: DefaultModel},
		Stream:   true,
		Messages: []provider.Message{{Role: "user", Text: "hello"}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	resp, err := p.RoundTrip(mustRequest(t, prep))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the answer: %v", err)
	}
	// A streaming request gets an SSE stream terminated the way clients expect;
	// the terminator is what ends their read loop.
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream for a streaming request", ct)
	}
	if !strings.Contains(string(body), "from the stub") {
		t.Fatalf("the answer lost the configured reply: %s", body)
	}
	if !strings.Contains(string(body), "data: [DONE]") {
		t.Fatalf("the stream has no terminator: %s", body)
	}
}

// TestBuildReportsTheModelItWasAsked — the body carries the request it was given,
// which is what makes the stub inspectable through a proxy or a log.
func TestBuildReportsTheModelItWasAsked(t *testing.T) {
	p := newTestProvider(t, Config{})
	const model = "stub-slow"

	prep, err := p.Build(provider.Request{
		Model:     provider.Model{ID: model},
		System:    "be brief",
		MaxTokens: 64,
		Messages:  []provider.Message{{Role: "user", Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var body struct {
		Model     string `json:"model"`
		System    string `json:"system"`
		MaxTokens int    `json:"max_tokens"`
	}
	if err := json.Unmarshal(prep.Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body.Model != model {
		t.Fatalf("model = %q, want the one requested", body.Model)
	}
	if body.System != "be brief" {
		t.Fatalf("system = %q, want the prompt the gateway assembled", body.System)
	}
	if body.MaxTokens != 64 {
		t.Fatalf("max_tokens = %d, want 64", body.MaxTokens)
	}

	// The answer names the requested model too, so a request can be traced from
	// the log to the response without guessing.
	resp, err := p.RoundTrip(mustRequest(t, prep))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()
	var answer struct {
		Model string `json:"model"`
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the answer: %v", err)
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		t.Fatalf("unmarshal answer: %v", err)
	}
	if answer.Model != model {
		t.Fatalf("the answer names %q, want %q", answer.Model, model)
	}
}

// mustRequest turns a prepared request into the value a transport receives.
func mustRequest(t *testing.T, prep provider.Prepared) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, prep.URL, strings.NewReader(string(prep.Body)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for name, values := range prep.Headers {
		req.Header[name] = values
	}
	return req
}
