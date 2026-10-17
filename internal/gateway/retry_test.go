package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"free-ai-router/internal/capability"
	"free-ai-router/internal/provider"
)

// The strategies, and the loop that applies them.
//
// WHAT IS UNDER TEST HERE. Not the mechanisms themselves — each has its own
// tests — but the decisions the loop makes: which question the refusal answered,
// which mechanism is therefore entitled to act, how many attempts are spent, and
// what the client finally receives. Those decisions are the gateway's, and they
// are the part that a change to either mechanism must not be able to break
// silently.

// rotation builds a mechanism over the given keys.
func rotation(t *testing.T, rotate bool, keys ...string) map[string]*capability.KeyRotation {
	t.Helper()

	raw := make([]any, 0, len(keys))
	for _, k := range keys {
		raw = append(raw, k)
	}
	built, err := capability.NewKeyRotation(map[string]any{"keys": raw, "rotate": rotate})
	if err != nil {
		t.Fatalf("building the rotation mechanism: %v", err)
	}
	kr, ok := built.(*capability.KeyRotation)
	if !ok {
		t.Fatalf("the mechanism factory returned %T", built)
	}
	return map[string]*capability.KeyRotation{"test": kr}
}

// retryPolicy builds a retry policy with a fixed, jitter-free delay.
func retryPolicy(t *testing.T, attempts int, delay time.Duration) map[string]*capability.Retry {
	t.Helper()

	built, err := capability.NewRetry(map[string]any{
		"attempts":     attempts,
		"delay_ms":     int(delay.Milliseconds()),
		"max_delay_ms": int(delay.Milliseconds()),
		"jitter":       0.0,
	})
	if err != nil {
		t.Fatalf("building the retry policy: %v", err)
	}
	r, ok := built.(*capability.Retry)
	if !ok {
		t.Fatalf("the mechanism factory returned %T", built)
	}
	return map[string]*capability.Retry{"test": r}
}

// keyRejecting is the classifier of a provider that answers an authentication
// refusal by naming the key. The helper exists so that the rotation tests cannot
// quietly stop rotating because of a classifier that reports nothing.
func keyRejecting(status int) func(*http.Response, []byte) provider.Failure {
	return func(resp *http.Response, body []byte) provider.Failure {
		f := provider.Failure{Status: resp.StatusCode, Body: body, Headers: resp.Header}
		if resp.StatusCode == status {
			f.KeyRejected = true
		}
		return f
	}
}

// TestKeyRejectionRotatesAndSucceeds — the first strategy.
//
// A refusal that names the key is answered with a DIFFERENT key, and the client
// sees one successful request. Both attempts are counted, because both happened.
func TestKeyRejectionRotatesAndSucceeds(t *testing.T) {
	var calls int32
	var sawKeys []string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		sawKeys = append(sawKeys, r.Header.Get("authorization"))

		if n == 1 {
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":{"message":"invalid api key"}}`)
			return
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[]}\n\n")
	}))
	defer upstream.Close()

	h, p, st := newTestHandler(t, upstream)
	h.KeyRotation = rotation(t, true, "k1", "k2")
	p.classif = keyRejecting(http.StatusForbidden)

	rec := postChat(t, h, `{"model":"test/model","messages":[]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the rotation did not happen", rec.Code)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("upstream was called %d times, want 2", got)
	}
	if len(sawKeys) != 2 || sawKeys[0] == sawKeys[1] {
		t.Fatalf("the second attempt reused the refused key: %v", sawKeys)
	}

	snap := st.Snapshot()
	if snap.Days[0].Refused != 1 || snap.Days[0].OK != 1 {
		t.Fatalf("want one refusal and one success, got %+v", snap.Days[0])
	}
}

// TestRotationOffMeansNoSecondAttempt — the switch on the mechanism.
//
// With rotation off the mechanism still ISSUES a key — the first one — and the
// refusal reaches the client at once. That is what the switch means: rotation is
// about replacing a spent key, not about whether a key is used at all.
func TestRotationOffMeansNoSecondAttempt(t *testing.T) {
	var calls int32

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.Header.Get("authorization") == "" {
			t.Error("rotation off must not mean anonymous: no key was sent")
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid api key"}}`)
	}))
	defer upstream.Close()

	h, p, _ := newTestHandler(t, upstream)
	h.KeyRotation = rotation(t, false, "k1", "k2")
	p.classif = keyRejecting(http.StatusForbidden)

	rec := postChat(t, h, `{"model":"test/model","messages":[]}`)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("upstream was called %d times, want 1", got)
	}
}

// TestRefusedKeysAreNotTriedTwice — the dedupe.
//
// The mechanism knows only the key it handed out and the current one, so with
// more keys than attempts it can hand back a key already refused. Without the
// dedupe a two-key pool under a blanket refusal ping-pongs until the client
// gives up — spending the pool and the client's time on a refusal that changing
// the key cannot fix.
func TestRefusedKeysAreNotTriedTwice(t *testing.T) {
	var calls int32

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid api key"}}`)
	}))
	defer upstream.Close()

	h, p, _ := newTestHandler(t, upstream)
	h.KeyRotation = rotation(t, true, "k1", "k2")
	p.classif = keyRejecting(http.StatusForbidden)

	rec := postChat(t, h, `{"model":"test/model","messages":[]}`)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("upstream was called %d times, want 2 — once per key and no more", got)
	}
}

// TestRetryableRefusalIsRepeated — the second strategy.
//
// A refusal that names the MOMENT rather than the key is answered by sending the
// same request again, and the client sees one successful request.
func TestRetryableRefusalIsRepeated(t *testing.T) {
	var calls int32

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"rate limit exceeded"}}`)
			return
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[]}\n\n")
	}))
	defer upstream.Close()

	h, p, st := newTestHandler(t, upstream)
	h.Retry = retryPolicy(t, 3, time.Millisecond)
	p.classif = func(resp *http.Response, body []byte) provider.Failure {
		f := provider.Failure{Status: resp.StatusCode, Body: body, Headers: resp.Header}
		if resp.StatusCode == http.StatusTooManyRequests {
			f.Retryable = true
		}
		return f
	}

	rec := postChat(t, h, `{"model":"test/model","messages":[]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the refusal was not repeated", rec.Code)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("upstream was called %d times, want 2", got)
	}

	snap := st.Snapshot()
	if snap.Days[0].Refused != 1 || snap.Days[0].OK != 1 {
		t.Fatalf("want one refusal and one success, got %+v", snap.Days[0])
	}
}

// TestKeyRejectionIsNotWaitedOut — the two questions stay apart.
//
// A refusal carrying BOTH judgements is answered by rotation, never by waiting:
// switching a key costs nothing and takes effect at once, while waiting costs the
// client time. A provider that means "these credentials are spent" must not be
// able to make the gateway sleep on them.
//
// The retry budget is set far higher than the number of keys on purpose. If
// waiting were allowed to take part, the attempt count would reach the budget;
// what is asserted is that the keys run out first, at two.
func TestKeyRejectionIsNotWaitedOut(t *testing.T) {
	var calls int32

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid api key"}}`)
	}))
	defer upstream.Close()

	h, p, _ := newTestHandler(t, upstream)
	h.KeyRotation = rotation(t, true, "k1", "k2")
	h.Retry = retryPolicy(t, 5, time.Millisecond)
	p.classif = func(resp *http.Response, body []byte) provider.Failure {
		return provider.Failure{
			Status:      resp.StatusCode,
			Body:        body,
			Headers:     resp.Header,
			KeyRejected: true,
			Retryable:   true, // both at once, on purpose
		}
	}

	rec := postChat(t, h, `{"model":"test/model","messages":[]}`)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("upstream was called %d times, want 2 — the keys ran out before the retry budget did", got)
	}
}

// TestRetryBudgetIsRespected — the policy bounds the repeats.
//
// The last refusal is the one the client receives, byte for byte. The policy is a
// budget, not a promise: when it runs out the answer is the provider's.
func TestRetryBudgetIsRespected(t *testing.T) {
	const body = `{"error":{"type":"RateLimit","message":"rate limit exceeded"}}`
	var calls int32

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, body)
	}))
	defer upstream.Close()

	h, p, st := newTestHandler(t, upstream)
	h.Retry = retryPolicy(t, 2, time.Millisecond)
	p.classif = func(resp *http.Response, body []byte) provider.Failure {
		return provider.Failure{
			Status:    resp.StatusCode,
			Body:      body,
			Headers:   resp.Header,
			Retryable: true,
		}
	}

	rec := postChat(t, h, `{"model":"test/model","messages":[]}`)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if got := rec.Body.String(); got != body {
		t.Fatalf("the final refusal changed:\n got %s\nwant %s", got, body)
	}
	// Two attempts, not two repeats: the budget counts requests, and every one
	// of them is recorded.
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("upstream was called %d times, want 2", got)
	}
	if snap := st.Snapshot(); snap.Days[0].Refused != 2 {
		t.Fatalf("want 2 refusals recorded, got %+v", snap.Days[0])
	}
}

// TestCancellationDuringTheWaitIsAborted — nobody is waiting for the answer.
//
// The wait is the client's wait. When the client goes away mid-wait the request
// must stop there: sleeping through it would hold a connection and a goroutine
// for a caller that stopped listening.
func TestCancellationDuringTheWaitIsAborted(t *testing.T) {
	var calls int32

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"rate limit exceeded"}}`)
	}))
	defer upstream.Close()

	h, p, st := newTestHandler(t, upstream)
	// Long enough that the cancellation wins the race deterministically.
	h.Retry = retryPolicy(t, 5, 30*time.Second)
	p.classif = func(resp *http.Response, body []byte) provider.Failure {
		return provider.Failure{
			Status:    resp.StatusCode,
			Body:      body,
			Headers:   resp.Header,
			Retryable: true,
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"test/model","messages":[]}`)).WithContext(ctx)
	rec := httptest.NewRecorder()

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	h.handleChat(rec, req)

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("upstream was called %d times, want 1 — the wait did not end with the client", got)
	}

	// The refusal that arrived before the cancellation was recorded as a real
	// attempt; the request itself ended without an answer and is an abort.
	snap := st.Snapshot()
	if snap.Days[0].Aborted == 0 {
		t.Fatalf("the cancelled wait was not recorded as an abort: %+v", snap.Days[0])
	}
}

// TestStreamFlagFollowsTheClient — the shape of the answer is the client's.
//
// A client that sent stream: false and receives an event stream is being lied to
// about the content type of its own response, and every non-streaming parser
// fails on the body. The absent field means the protocol's default, which is a
// single response.
func TestStreamFlagFollowsTheClient(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"asked for a stream", `{"model":"test/model","messages":[],"stream":true}`, true},
		{"asked for a single response", `{"model":"test/model","messages":[],"stream":false}`, false},
		{"did not say", `{"model":"test/model","messages":[]}`, false},
		{"said something else", `{"model":"test/model","messages":[],"stream":"true"}`, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("content-type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"choices\":[]}\n\n")
			}))
			defer upstream.Close()

			h, p, _ := newTestHandler(t, upstream)

			var seen bool
			base := p.build
			p.build = func(req provider.Request) (provider.Prepared, error) {
				seen = req.Stream
				return base(req)
			}

			postChat(t, h, tc.body)

			if seen != tc.want {
				t.Fatalf("the provider was asked for stream = %t, want %t", seen, tc.want)
			}
		})
	}
}

// TestLocalProviderIsNotDialed — a provider that answers itself is not reached
// over the network.
//
// The stub builds an address that does not resolve precisely so that this is
// visible: if the local transport were not honoured, the request would fail with
// a name-resolution error instead of being answered.
func TestLocalProviderIsNotDialed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer upstream.Close()

	h, p, st := newTestHandler(t, upstream)
	p.build = func(provider.Request) (provider.Prepared, error) {
		return provider.Prepared{
			URL:     "http://stub.invalid/chat/completions",
			Headers: http.Header{"content-type": []string{"application/json"}},
			Body:    []byte(`{}`),
		}, nil
	}
	h.Providers.Register(localProvider{id: "local", inner: p})
	h.DefaultProvider = "local"

	rec := postChat(t, h, `{"model":"local/model","messages":[]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the local transport was not used", rec.Code)
	}
	if snap := st.Snapshot(); snap.Days[0].OK != 1 {
		t.Fatalf("the local answer was not counted: %+v", snap.Days[0])
	}
}

// localProvider is a provider that offers its own transport.
type localProvider struct {
	id    provider.ID
	inner provider.Provider
}

func (l localProvider) ID() provider.ID { return l.id }

func (l localProvider) Models(ctx context.Context) ([]provider.Model, error) {
	return l.inner.Models(ctx)
}

func (l localProvider) Build(req provider.Request) (provider.Prepared, error) {
	return l.inner.Build(req)
}

func (l localProvider) Classify(resp *http.Response, body []byte) provider.Failure {
	return l.inner.Classify(resp, body)
}

// RoundTripper implements provider.Local. It answers without a socket.
func (l localProvider) RoundTripper() http.RoundTripper { return localTransport{} }

type localTransport struct{}

func (localTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"content-type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader("data: {\"choices\":[]}\n\n")),
	}, nil
}
