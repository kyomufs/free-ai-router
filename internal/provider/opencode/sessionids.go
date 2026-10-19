package opencode

import (
	"net/http"
	"strings"

	"free-ai-router/internal/provider"
)

// Session identifiers as the admission check sees them.
//
// The check reads the identifiers a client puts into its headers, so they are
// not the client's to decide alone: two requests that differed only in a
// session header were measured on 2026-10-19, and the one carrying `ses_test`
// got a FreeTierError while the one carrying `ses_` + 12 hex + 14 base62 was
// answered. What the check admits is recorded in contract.go; this file is the
// code that applies it, and it is here rather than in the gateway because it is
// a property of THIS check. The gateway asks for the identifiers through
// provider.IdentifierSource and knows nothing about their shape, so a provider
// whose service ignores these headers forwards the client's values untouched.

// Header names the client mints its identifiers into.
const (
	headerSessionID = "x-opencode-session-id"
	headerSession   = "x-opencode-session"
	headerProject   = "x-opencode-project"
	headerRequest   = "x-opencode-request"
	headerParentID  = "x-opencode-parent-session-id"
)

// The same identifiers under neutral names, for a client that follows the
// shape without borrowing the prefix. Both spellings carry the same value, and
// the prefixed one wins when a client sends both.
const (
	headerPlainSessionID = "x-session-id"
	headerPlainSession   = "x-session-affinity"
	headerPlainParentID  = "x-parent-session-id"
)

// SessionIDs derives the identifiers for one request from the client's headers.
//
// It implements provider.IdentifierSource.
func (p *Provider) SessionIDs(r *http.Request) provider.SessionIDs {
	return sessionIDs(r)
}

// sessionIDs is the derivation itself, separate from the method so that the
// tests can exercise it without a provider.
//
// The client may send its own identifiers; if it did, they are respected. If
// the shape is one the check refuses, the value is replaced rather than
// forwarded, because the call would fail over a header the model never sees.
func sessionIDs(r *http.Request) provider.SessionIDs {
	ids := provider.SessionIDs{
		Session:  nonEmpty(r.Header.Get(headerSessionID), r.Header.Get(headerPlainSessionID)),
		Affinity: nonEmpty(r.Header.Get(headerSession), r.Header.Get(headerPlainSession)),
		Project:  r.Header.Get(headerProject),
		Message:  r.Header.Get(headerRequest),
		Parent:   nonEmpty(r.Header.Get(headerParentID), r.Header.Get(headerPlainParentID)),
	}

	if ids.Session == "" || !isSessionID(ids.Session) {
		ids.Session = newSessionID()
		ids.SessionMinted = true
	}
	if ids.Parent != "" && !isSessionID(ids.Parent) {
		// A parent of the wrong shape is dropped rather than replaced: it only
		// feeds the affinity below, and deriving a parent would invent a
		// hierarchy the client never described.
		ids.Parent = ""
	}
	if ids.Affinity == "" || !isSessionID(ids.Affinity) {
		// Without a parent, affinity equals the session identifier — that is
		// what the client does too.
		ids.Affinity = nonEmpty(ids.Parent, ids.Session)
	}
	if ids.Message == "" || isRejectedRequestID(ids.Message) {
		// The header wants the identifier of the MESSAGE being answered and
		// refuses a per-request one. A client that sent a `req_` value was
		// following the naming convention of a different identifier, so the
		// value is replaced rather than forwarded: the prefix alone fails the
		// whole call, and failing it over a header nobody reads is worse than
		// deriving a proper one.
		ids.Message = newMessageID()
	}
	if ids.Project == "" || isRejectedProjectID(ids.Project) {
		// Outside a git project the client sends the literal "global", and a
		// `prj_`-prefixed value is refused regardless of what follows it. The
		// project identifier belongs in the request body or path, never here.
		ids.Project = "global"
	}
	return ids
}

// Prefixes the service refuses to see in these two headers.
//
// MEASURED, one header at a time against the live service:
//
//	x-opencode-project: prj_f698f87ea99d390458196363 -> refused
//	x-opencode-project: global                       -> admitted
//	x-opencode-project: <absent>                     -> admitted
//	x-opencode-request: req_2cb728ee0be94ed0c7b20920b21fde87 -> refused
//	x-opencode-request: msg_13b2d53f2001ia66lVkDLegkmV       -> admitted
//	x-opencode-request: <absent>                             -> admitted
//
// What is refused is the PREFIX, not the value: any `prj_`-prefixed string is
// rejected regardless of what follows it. The check is therefore on the marker
// rather than on the contents.
const (
	rejectedProjectPrefix = "prj_"
	rejectedRequestPrefix = "req_"
)

// isRejectedProjectID reports whether a project header value carries the
// refused marker.
func isRejectedProjectID(v string) bool { return strings.HasPrefix(v, rejectedProjectPrefix) }

// isRejectedRequestID reports whether a request header value carries the
// refused marker.
func isRejectedRequestID(v string) bool { return strings.HasPrefix(v, rejectedRequestPrefix) }
