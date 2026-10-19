package opencode

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"free-ai-router/internal/provider"
)

// conversationRequest builds a request the way the gateway does for a client
// that sends no identifier headers: the identifiers are minted, and the
// conversation starts with one user message under a system part.
func conversationRequest(t *testing.T, system, first string) provider.Request {
	t.Helper()

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ids := sessionIDs(r)
	if !ids.SessionMinted {
		t.Fatalf("client sent nothing, yet Session = %q is not marked minted", ids.Session)
	}

	return provider.Request{
		Model:    provider.Model{ID: "x"},
		System:   system,
		Messages: []provider.Message{{Role: "user", Text: first}},
		Session:  ids,
	}
}

// TestSessionIsStableAcrossTheTurnsOfOneConversation — the identifier of a
// conversation must not change between its turns.
//
// THE PROPERTY IS THE POINT, not the value: the service counts traffic by this
// identifier and routes the prompt cache by it, so a conversation that presents
// a new session on every call is a new anonymous client on every call. That is
// what used to happen, because our client sends no identifier headers and the
// provider minted a fresh one per request.
func TestSessionIsStableAcrossTheTurnsOfOneConversation(t *testing.T) {
	p := newTestProvider(t)

	// Two requests of one conversation. Each arrives without identifiers, so
	// each carries a minted value of its own — which is exactly the case the
	// cache has to fold into one session.
	first, err := p.Build(conversationRequest(t, "you are helpful", "посчитай сумму"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Build(conversationRequest(t, "you are helpful", "посчитай сумму"))
	if err != nil {
		t.Fatal(err)
	}

	got, want := header(second.Headers, "x-opencode-session"), header(first.Headers, "x-opencode-session")
	if got != want {
		t.Fatalf("x-opencode-session = %q on the second turn, want the first turn's %q", got, want)
	}
	if !isSessionID(got) {
		t.Fatalf("x-opencode-session = %q, want an id of the service's shape", got)
	}
	if got := header(second.Headers, "x-opencode-session-id"); got != want {
		t.Fatalf("x-opencode-session-id = %q, want the same session %q", got, want)
	}
}

// TestDifferentConversationsGetDifferentSessions — the fold must not collapse
// every conversation into one session, which would be the opposite failure:
// unrelated traffic accounted as a single client.
func TestDifferentConversationsGetDifferentSessions(t *testing.T) {
	p := newTestProvider(t)

	a, err := p.Build(conversationRequest(t, "you are helpful", "первая задача"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.Build(conversationRequest(t, "you are helpful", "вторая задача"))
	if err != nil {
		t.Fatal(err)
	}

	gotA, gotB := header(a.Headers, "x-opencode-session"), header(b.Headers, "x-opencode-session")
	if gotA == gotB {
		t.Fatalf("both conversations got the session %q, want distinct identifiers", gotA)
	}
}

// TestClientSuppliedSessionIsNeverRewritten — a client that mints its own
// identifiers owns its conversations, and the cache stays out of the way.
func TestClientSuppliedSessionIsNeverRewritten(t *testing.T) {
	p := newTestProvider(t)

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set(headerSessionID, "ses_1a2b3c4d5e6fABCDEFGHIJKLMN")
	ids := sessionIDs(r)
	if ids.SessionMinted {
		t.Fatal("a canonical client id was marked minted")
	}

	prep, err := p.Build(provider.Request{
		Model:    provider.Model{ID: "x"},
		System:   "you are helpful",
		Messages: []provider.Message{{Role: "user", Text: "привет"}},
		Session:  ids,
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := header(prep.Headers, "x-opencode-session"); got != "ses_1a2b3c4d5e6fABCDEFGHIJKLMN" {
		t.Fatalf("x-opencode-session = %q, want the caller value preserved", got)
	}
}

// TestSessionCacheIsBounded — the mapping may not grow without limit over a day
// of agent work, and the eviction must drop the oldest conversation.
func TestSessionCacheIsBounded(t *testing.T) {
	c := newSessionCache(2)

	c.put("a", "ses_a")
	c.put("b", "ses_b")
	c.put("c", "ses_c")

	if got := c.size(); got != 2 {
		t.Fatalf("size = %d, want the ceiling 2", got)
	}
	if _, ok := c.get("a"); ok {
		t.Fatal("the oldest conversation survived the eviction")
	}
	if id, ok := c.get("c"); !ok || id != "ses_c" {
		t.Fatalf("c = %q, %v; want the newest conversation kept", id, ok)
	}
}

// TestConversationWithoutUserMessageKeepsTheMintedId — nothing to fingerprint
// means nothing to be stable about, and the request must still go out.
func TestConversationWithoutUserMessageKeepsTheMintedId(t *testing.T) {
	p := newTestProvider(t)

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ids := sessionIDs(r)

	prep, err := p.Build(provider.Request{
		Model:    provider.Model{ID: "x"},
		System:   "you are helpful",
		Messages: []provider.Message{{Role: "tool", Text: "result", ToolCallID: "call_1"}},
		Session:  ids,
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := header(prep.Headers, "x-opencode-session"); got != ids.Session {
		t.Fatalf("x-opencode-session = %q, want the minted %q", got, ids.Session)
	}
}
