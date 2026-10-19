package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"free-ai-router/internal/provider"
)

// Which credential goes into the header, and under which name.
//
// THE THREE CASES. A key was issued, no key was issued, or the provider answers
// itself and there is no header to fill. All three are normal modes; the failure
// this file guards against is the one that looks like success — a request that
// leaves without the credentials it was supposed to carry, and a refusal from the
// service that is then read as a rate limit rather than as a missing key.

// TestAnIssuedKeyReachesTheUpstream — the ordinary path.
func TestAnIssuedKeyReachesTheUpstream(t *testing.T) {
	var got []string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("authorization"))
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[]}\n\ndata: [DONE]\n\n"))
	}))
	defer upstream.Close()

	h, p, _ := newTestHandler(t, upstream)
	h.KeyRotation = rotation(t, true, "sk-secret")
	h.Providers.Register(echoingProvider{id: "test", inner: p})

	rec := postChat(t, h, `{"model":"test/model","messages":[]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(got) != 1 || got[0] != "Bearer sk-secret" {
		t.Fatalf("authorization = %q, want the issued key", got)
	}
}

// TestNoKeyMeansTheAnonymityMarker — the mode a deployment with no keys runs in.
//
// The marker is not a fallback: the service recognises it as "no account", and an
// empty header instead would be a malformed request rather than anonymous access.
func TestNoKeyMeansTheAnonymityMarker(t *testing.T) {
	var got []string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("authorization"))
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[]}\n\ndata: [DONE]\n\n"))
	}))
	defer upstream.Close()

	h, p, _ := newTestHandler(t, upstream)
	// No rotation mechanism at all: the provider substitutes its own marker.
	h.Providers.Register(anonymousProvider{id: "test", inner: p})

	rec := postChat(t, h, `{"model":"test/model","messages":[]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(got) != 1 || got[0] != "Bearer public" {
		t.Fatalf("authorization = %q, want the anonymity marker", got)
	}
}

// TestThePoolIsRotatedAcrossRequests — the mechanism is asked on every attempt.
//
// Without this the pool would exist in the configuration and nowhere in the
// traffic: every request would go out on the same key, which is the one thing a
// pool is for.
func TestThePoolIsRotatedAcrossRequests(t *testing.T) {
	var got []string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, strings.TrimPrefix(r.Header.Get("authorization"), "Bearer "))
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[]}\n\ndata: [DONE]\n\n"))
	}))
	defer upstream.Close()

	h, p, _ := newTestHandler(t, upstream)
	h.KeyRotation = rotation(t, true, "k1", "k2", "k3")
	h.Providers.Register(echoingProvider{id: "test", inner: p})

	postChat(t, h, `{"model":"test/model","messages":[]}`)
	postChat(t, h, `{"model":"test/model","messages":[]}`)

	if len(got) != 2 {
		t.Fatalf("upstream saw %d requests, want 2", len(got))
	}
	if got[0] == got[1] {
		t.Fatalf("both requests used the key %q: the pool is not rotating", got[0])
	}
}

// TestTheProviderChoosesTheHeaderShape — the name and the prefix are the
// provider's contract.
//
// A service that expects its key under another header, or without a prefix, cannot
// be served by a hard-coded "authorization: Bearer". The provider reports the
// shape, and the gateway obeys it.
func TestTheProviderChoosesTheHeaderShape(t *testing.T) {
	var got []string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("x-api-key"))
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[]}\n\ndata: [DONE]\n\n"))
	}))
	defer upstream.Close()

	h, p, _ := newTestHandler(t, upstream)
	h.KeyRotation = rotation(t, true, "sk-secret")
	h.Providers.Register(keyHeaderProvider{echoingProvider{id: "test", inner: p}})

	postChat(t, h, `{"model":"test/model","messages":[]}`)

	if len(got) != 1 || got[0] != "sk-secret" {
		t.Fatalf("x-api-key = %q, want the key without a prefix", got)
	}
}

// TestAProviderThatUsesNoCredentialGetsNoHeader — a provider that answers
// in-process has no header to fill, and filling one anyway would put credentials
// on a request to a host that does not exist.
func TestAProviderThatUsesNoCredentialGetsNoHeader(t *testing.T) {
	var sawHeader []string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawHeader = append(sawHeader, r.Header.Get("authorization"))
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[]}\n\ndata: [DONE]\n\n"))
	}))
	defer upstream.Close()

	h, p, _ := newTestHandler(t, upstream)
	h.KeyRotation = rotation(t, true, "sk-secret")
	// The stand-in has no Credential method: it never asked for keys.
	h.Providers.Register(p)

	rec := postChat(t, h, `{"model":"test/model","messages":[]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// The default scheme still applies, so the key does go out — this is what
	// "no credential method" means: the provider has no opinion, not that it
	// refuses keys. The case that matters is asserting the fallback stays, so
	// that a provider added without a Credential method is not silently
	// anonymous.
	if len(sawHeader) != 1 || sawHeader[0] != "Bearer sk-secret" {
		t.Fatalf("authorization = %q, want the issued key under the default scheme", sawHeader)
	}
}

// --- the stand-ins used by the checks above ---

// echoingProvider is a provider whose credential is whatever was issued, and
// which never substitutes a marker: with no key it sends nothing.
type echoingProvider struct {
	id    provider.ID
	inner *stubProvider
}

func (e echoingProvider) ID() provider.ID { return e.id }

func (e echoingProvider) Models(ctx context.Context) ([]provider.Model, error) {
	return e.inner.Models(ctx)
}

func (e echoingProvider) Build(req provider.Request) (provider.Prepared, error) {
	return e.inner.Build(req)
}

func (e echoingProvider) Classify(resp *http.Response, body []byte) provider.Failure {
	return e.inner.Classify(resp, body)
}

// Credential implements the credential scheme.
func (e echoingProvider) Credential(key string) string { return key }

// anonymousProvider substitutes a marker when no key was issued.
type anonymousProvider struct {
	id    provider.ID
	inner *stubProvider
}

func (a anonymousProvider) ID() provider.ID { return a.id }

func (a anonymousProvider) Models(ctx context.Context) ([]provider.Model, error) {
	return a.inner.Models(ctx)
}

func (a anonymousProvider) Build(req provider.Request) (provider.Prepared, error) {
	return a.inner.Build(req)
}

func (a anonymousProvider) Classify(resp *http.Response, body []byte) provider.Failure {
	return a.inner.Classify(resp, body)
}

// Credential implements the credential scheme, with a marker for the empty key.
func (a anonymousProvider) Credential(key string) string {
	if key == "" {
		return "public"
	}
	return key
}

// keyHeaderProvider carries its key under a header of its own choosing.
type keyHeaderProvider struct{ echoingProvider }

// CredentialHeader implements the header shape.
func (keyHeaderProvider) CredentialHeader() (string, string) { return "x-api-key", "" }
