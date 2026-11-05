// Package control — managing a running daemon.
//
// WHY A SEPARATE SURFACE. The daemon holds the provider registry, the rotation
// mechanisms and the counters; the operator needs to be able to look at them
// and to change what changes without a restart. The Control API provides one
// set of routes for that, and the CLI, the TUI and third-party scripts use it
// alike.
//
// WHAT CHANGES ON A LIVE DAEMON and what does not. The set of keys changes, and
// so does which of them are in use. Nothing relating to the socket or to the
// provider registry changes: the listen address is already taken, and the
// registry was assembled from the configuration at startup. An attempt to
// change them on a live daemon would return a state that nobody enforces — and
// that is worse than a refusal.
//
// TOKEN. In server mode the check wraps this surface too: control can burn
// through a provider's keys, so an open control would nullify the gateway's
// check.
package control

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"free-ai-router/internal/config"
	"free-ai-router/internal/gateway"
	"free-ai-router/internal/provider"
	"free-ai-router/internal/state"
)

// Prefix — the prefix of all management routes.
const Prefix = "/_control/"

// Handler serves the management routes.
type Handler struct {
	// Providers — the provider registry.
	Providers *provider.Registry
	// KeyRotation — the key-rotation mechanisms by provider.
	KeyRotation gateway.KeyRotationRegistry
	// State — the counters.
	State *state.Store
	// Log — the journal.
	Log *state.Logger
	// Config — the active configuration. Read-only: changing values that affect
	// the socket requires a restart.
	Config *config.Config

	// Transports — the available transports. Filled in by the entry point,
	// because the list of dictionaries lives in one place instead of being
	// copied into the interfaces.
	Transports []string

	// Egress — the observed egress addresses. May be nil, which means the daemon
	// does not know its address; the status then carries no address block rather
	// than an empty one.
	Egress EgressStatus

	// Token and RequireToken — the access check.
	Token        string
	RequireToken bool
}

// Routes registers the routes.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.HandleFunc(Prefix, h.serve)
}

// serve parses the route and calls the handler.
func (h *Handler) serve(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"error": "a token is required",
		})
		return
	}

	switch strings.TrimPrefix(r.URL.Path, Prefix) {
	case "status":
		h.handleStatus(w, r)
	case "stats":
		h.handleStats(w, r)
	case "models":
		h.handleModels(w, r)
	case "keys":
		h.handleKeys(w, r)
	case "keys/add":
		h.handleKeyAdd(w, r)
	case "keys/remove":
		h.handleKeyRemove(w, r)
	case "options":
		h.handleOptions(w, r)
	default:
		http.NotFound(w, r)
	}
}

// authorized checks the token.
//
// The comparison is no longer byte-by-byte with an early exit: a constant-time
// comparison is used, because a byte-by-byte exit leaks the secret's prefix
// through the response time.
func (h *Handler) authorized(r *http.Request) bool {
	if !h.RequireToken {
		return true
	}
	presented := bearerToken(r)
	return subtleEquals(presented, h.Token)
}

// bearerToken pulls the token out of the header.
func bearerToken(r *http.Request) string {
	v := r.Header.Get("authorization")
	if len(v) > 7 && strings.EqualFold(v[:7], "bearer ") {
		return v[7:]
	}
	return v
}

// decodeBody parses the request body.
func decodeBody(r *http.Request, dst any) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dst)
}

// writeJSON answers with a JSON body.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// subtleEquals compares strings in constant time.
//
// The length leaks regardless: values of different lengths cannot be compared
// in constant time without knowing the length in advance. The content can be,
// and it is the content that is the secret: a byte-by-byte exit with an early
// return would allow the token to be guessed from the response time.
func subtleEquals(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
