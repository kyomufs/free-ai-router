package gateway

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"free-ai-router/internal/provider"
)

// THE CREDENTIAL AXIS DECIDES THE SOURCE, and the pool only supplies the value.
//
// This file exists because the two used to be tangled the other way round: the
// axis gated rotation alone, while any key present in the configuration was sent
// regardless. A deployment could then be switched to "anonymous" and keep
// presenting a key, which is a lie in two directions at once — the operator
// believes the account is untouched, and the log attributes the traffic to a
// credential that was never meant to be spent.
//
// The distinction the tests below draw is between two questions that look alike
// and are not:
//
//	does the section supply a key?
//	does the request carry one?
//
// Under "account" the answers agree. Under "anonymous" they deliberately do not,
// and either of them being read for the other is the failure this file guards
// against.

// recordingUpstream returns an upstream that records every Authorization header
// it receives.
//
// It answers with a refusal rather than a stream: the retry and rotation
// machinery must not be involved in a test about which credential is presented,
// and a refusal ends the request after exactly one attempt.
func recordingUpstream(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()

	var mu sync.Mutex
	var seen []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("authorization"))
		mu.Unlock()

		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[]}\n\n"))
	}))

	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

// TestAnonymousPresentsNoKeyEvenWhenThePoolHasOne.
//
// The whole point of the mode. A key on file is not consent to spend it: the
// operator may keep it configured while testing anonymously, and a pool that
// leaks into the request makes the mode a label rather than a behaviour.
func TestAnonymousPresentsNoKeyEvenWhenThePoolHasOne(t *testing.T) {
	upstream, seen := recordingUpstream(t)
	defer upstream.Close()

	h, p, _ := newTestHandler(t, upstream)
	h.KeyRotation = rotation(t, true, "sk-secret")
	h.Credential = func(provider.ID) string { return credentialAnonymous }
	h.Providers.Register(anonymousProvider{id: "test", inner: p})

	postChat(t, h, `{"model":"test/model","messages":[]}`)

	got := seen()
	if len(got) != 1 {
		t.Fatalf("the upstream saw %d requests, want 1", len(got))
	}
	if got[0] != "Bearer public" {
		t.Fatalf("authorization = %q, want the anonymity marker: a key was presented under the anonymous credential", got[0])
	}
}

// TestAccountPresentsTheKeyFromThePool — the ordinary keyed path.
//
// It is here beside the anonymous case on purpose: the two differ in exactly one
// thing, and a reader comparing them should not have to trust that the rest of
// the setup is the same.
func TestAccountPresentsTheKeyFromThePool(t *testing.T) {
	upstream, seen := recordingUpstream(t)
	defer upstream.Close()

	h, p, _ := newTestHandler(t, upstream)
	h.KeyRotation = rotation(t, true, "sk-secret")
	h.Credential = func(provider.ID) string { return credentialAccount }
	h.Providers.Register(anonymousProvider{id: "test", inner: p})

	postChat(t, h, `{"model":"test/model","messages":[]}`)

	got := seen()
	if len(got) != 1 {
		t.Fatalf("the upstream saw %d requests, want 1", len(got))
	}
	if got[0] != "Bearer sk-secret" {
		t.Fatalf("authorization = %q, want the key from the pool", got[0])
	}
}

// TestAnonymousWithoutAPoolStillPresentsTheMarker.
//
// The case where the axis and the pool agree. A daemon with no keys at all is
// the free tier's ordinary deployment, and it must not be broken by the axis
// becoming authoritative.
func TestAnonymousWithoutAPoolStillPresentsTheMarker(t *testing.T) {
	upstream, seen := recordingUpstream(t)
	defer upstream.Close()

	h, p, _ := newTestHandler(t, upstream)
	h.Credential = func(provider.ID) string { return credentialAnonymous }
	h.Providers.Register(anonymousProvider{id: "test", inner: p})

	postChat(t, h, `{"model":"test/model","messages":[]}`)

	got := seen()
	if len(got) != 1 {
		t.Fatalf("the upstream saw %d requests, want 1", len(got))
	}
	if got[0] != "Bearer public" {
		t.Fatalf("authorization = %q, want the anonymity marker", got[0])
	}
}

// TestAccountWithoutAPoolSendsNoCredentialRatherThanTheMarker.
//
// The degenerate case, and the one that reaches the upstream only when the pool
// empties after the daemon started: validation refuses "account without a key",
// so a running daemon has no business reaching this state through the front
// door.
//
// The assertion is deliberately about the ABSENCE of the key rather than about
// the marker: the gateway must not quietly substitute one credential for
// another, and the marker in particular would be the anonymous behaviour wearing
// the account's name. What actually goes out is nothing, which the provider
// turns into its marker — and that is the provider's decision to make, not the
// gateway's.
func TestAccountWithoutAPoolSendsNoCredentialRatherThanTheMarker(t *testing.T) {
	upstream, seen := recordingUpstream(t)
	defer upstream.Close()

	h, p, _ := newTestHandler(t, upstream)
	h.Credential = func(provider.ID) string { return credentialAccount }
	h.Providers.Register(anonymousProvider{id: "test", inner: p})

	postChat(t, h, `{"model":"test/model","messages":[]}`)

	got := seen()
	if len(got) != 1 {
		t.Fatalf("the upstream saw %d requests, want 1", len(got))
	}
	if got[0] == "Bearer sk-secret" {
		t.Fatalf("authorization = %q: a key appeared from an empty pool", got[0])
	}
}

// TestTheAxisAloneDecidesWhenBothArePresent.
//
// The regression this file was written for, stated as one assertion: with a
// populated pool and rotation switched on, the axis is the only thing that
// changes between the two runs. If the pool could override it, both runs would
// look alike and the mode would be a comment.
func TestTheAxisAloneDecidesWhenBothArePresent(t *testing.T) {
	cases := []struct {
		credential string
		want       string
	}{
		{credentialAnonymous, "Bearer public"},
		{credentialAccount, "Bearer sk-secret"},
	}

	for _, c := range cases {
		t.Run(c.credential, func(t *testing.T) {
			upstream, seen := recordingUpstream(t)
			defer upstream.Close()

			h, p, _ := newTestHandler(t, upstream)
			h.KeyRotation = rotation(t, true, "sk-secret")
			h.Credential = func(provider.ID) string { return c.credential }
			h.Providers.Register(anonymousProvider{id: "test", inner: p})

			postChat(t, h, `{"model":"test/model","messages":[]}`)

			got := seen()
			if len(got) != 1 {
				t.Fatalf("the upstream saw %d requests, want 1", len(got))
			}
			if got[0] != c.want {
				t.Fatalf("credential %q: authorization = %q, want %q", c.credential, got[0], c.want)
			}
		})
	}
}

// TestWithoutAResolverThePoolDecides.
//
// The fallback, kept because a handler assembled by hand — a test, a future
// embedder — may wire a registry without a configuration. Absent an answer about
// the axis, the pool's presence is the only evidence available, and using it is
// better than refusing to serve.
func TestWithoutAResolverThePoolDecides(t *testing.T) {
	upstream, seen := recordingUpstream(t)
	defer upstream.Close()

	h, p, _ := newTestHandler(t, upstream)
	h.KeyRotation = rotation(t, true, "sk-secret")
	h.Providers.Register(anonymousProvider{id: "test", inner: p})

	postChat(t, h, `{"model":"test/model","messages":[]}`)

	got := seen()
	if len(got) != 1 {
		t.Fatalf("the upstream saw %d requests, want 1", len(got))
	}
	if got[0] != "Bearer sk-secret" {
		t.Fatalf("authorization = %q, want the key: an unwired handler must still use its pool", got[0])
	}
}

// TestIssuesCredentialsFollowsTheAxis.
//
// The monitor asks this rather than reading the axis, and the two must not
// disagree: a report of "keys in use" over requests that carry none is worse than
// no report, because it sends the reader to look for the fault in the wrong
// place.
func TestIssuesCredentialsFollowsTheAxis(t *testing.T) {
	upstream, _ := recordingUpstream(t)
	defer upstream.Close()

	cases := []struct {
		name       string
		credential string
		keys       bool
		want       bool
	}{
		{"anonymous with a pool", credentialAnonymous, true, false},
		{"anonymous without a pool", credentialAnonymous, false, false},
		{"account with a pool", credentialAccount, true, true},
		// No pool and no key to present: the axis cannot conjure one, and the
		// monitor must not claim it did.
		{"account without a pool", credentialAccount, false, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, p, _ := newTestHandler(t, upstream)
			if c.keys {
				h.KeyRotation = rotation(t, true, "sk-secret")
			}
			h.Credential = func(provider.ID) string { return c.credential }

			if got := h.issuesCredentials(p); got != c.want {
				t.Fatalf("issuesCredentials = %t, want %t", got, c.want)
			}
		})
	}
}
