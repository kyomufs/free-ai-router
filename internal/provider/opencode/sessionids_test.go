package opencode

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"free-ai-router/internal/provider"
)

// TestRejectedHeaderPrefixesAreReplaced — the two header values the service
// refuses outright are replaced, not forwarded.
//
// Both are hard refusals keyed on a PREFIX rather than on a value, so a client
// that sends either shape would fail the whole call over a header nobody
// reads. The measurement behind it, and the admitted substitutes, are recorded
// next to the check itself in sessionids.go.
func TestRejectedHeaderPrefixesAreReplaced(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set(headerProject, "prj_f698f87ea99d390458196363")
	r.Header.Set(headerRequest, "req_2cb728ee0be94ed0c7b20920b21fde87")

	ids := sessionIDs(r)

	if !isProjectHash(ids.Project) {
		t.Errorf("Project = %q, want a 40-hex directory hash", ids.Project)
	}
	if !strings.HasPrefix(ids.Message, messageIDPrefix) {
		t.Errorf("Message = %q, want a derived message id", ids.Message)
	}
}

// TestLegacyProjectValueIsReplaced — v1 sent "global" outside a git worktree;
// v2 mints a directory hash. Forwarding the v1 sentinel would keep the request
// looking like the version the lane is retiring, so it is translated too.
func TestLegacyProjectValueIsReplaced(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set(headerProject, legacyProjectID)

	ids := sessionIDs(r)

	if ids.Project == legacyProjectID {
		t.Fatalf("Project = %q, want the v1 sentinel replaced", ids.Project)
	}
	if !isProjectHash(ids.Project) {
		t.Errorf("Project = %q, want a 40-hex directory hash", ids.Project)
	}
}

// isProjectHash reports whether v is the 40 lowercase hex the v2 client mints.
func isProjectHash(v string) bool {
	if len(v) != 40 {
		return false
	}
	for _, c := range v {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// TestAdmissibleHeaderValuesSurvive — the replacement must not touch values
// the service admits. A check that rewrote everything would pass the test
// above while discarding identifiers the client supplied on purpose.
func TestAdmissibleHeaderValuesSurvive(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set(headerProject, "my-project")
	r.Header.Set(headerRequest, "msg_13b2d53f2001ia66lVkDLegkmV")

	ids := sessionIDs(r)

	if ids.Project != "my-project" {
		t.Errorf("Project = %q, want the caller value preserved", ids.Project)
	}
	if ids.Message != "msg_13b2d53f2001ia66lVkDLegkmV" {
		t.Errorf("Message = %q, want the caller value preserved", ids.Message)
	}
}

// TestForeignSessionIdsAreReplaced — session identifiers of someone else's shape
// do not reach the service.
//
// The admission check reads the session headers too: two requests that differ
// only in the session id were measured on 2026-10-19, and the one carrying
// `ses_test` got a FreeTierError while the one carrying `ses_` + 12 hex + 14
// base62 was answered. A client with its own naming convention (a bare UUID, a
// prefixed counter) therefore fails every call for a reason nobody can see, so
// the value is replaced by an id of the client's own shape.
func TestForeignSessionIdsAreReplaced(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set(headerPlainSessionID, "3f2a1c9e-0b44-4d7a-9d21-8b1f2c3d4e5f")
	r.Header.Set(headerSession, "ses_test")
	r.Header.Set(headerPlainParentID, "conversation-42")

	ids := sessionIDs(r)

	if !isSessionID(ids.Session) {
		t.Errorf("Session = %q, want an id of the client's shape", ids.Session)
	}
	if !isSessionID(ids.Affinity) {
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
// for the service, so it travels unchanged; that is also what keeps prefix
// caching on the same conversation across requests.
func TestClientSessionIdsSurvive(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set(headerSessionID, "ses_1a2b3c4d5e6fABCDEFGHIJKLMN")
	r.Header.Set(headerSession, "ses_6f5e4d3c2b1aZYXWVUTSRQPONB")

	ids := sessionIDs(r)

	if ids.Session != "ses_1a2b3c4d5e6fABCDEFGHIJKLMN" {
		t.Errorf("Session = %q, want the caller value preserved", ids.Session)
	}
	if ids.Affinity != "ses_6f5e4d3c2b1aZYXWVUTSRQPONB" {
		t.Errorf("Affinity = %q, want the caller value preserved", ids.Affinity)
	}
}

// TestProviderIsAskedForIdentifiers — the derivation must be reachable through
// the interface the gateway asks for, not only as a package function. Without
// it the gateway hands an empty set and the request reaches the service with no
// session header at all.
//
// The assignment is also the compile-time assertion: a provider that stopped
// implementing IdentifierSource would fail to build here rather than in a live
// call that returns FreeTierError.
func TestProviderIsAskedForIdentifiers(t *testing.T) {
	p, err := New(Config{ID: provider.ID("opencode")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var src provider.IdentifierSource = p

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set(headerSession, "ses_test")

	if got := src.SessionIDs(r); !isSessionID(got.Session) {
		t.Errorf("Session = %q, want an id of the client's shape", got.Session)
	}
}
