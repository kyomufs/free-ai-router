package opencode

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"

	"free-ai-router/internal/provider"
)

// The session identifier of a conversation whose client sends none.
//
// WHY IT MATTERS. The client we imitate mints a session id once and reuses it
// for every turn of the conversation; this service reads that header and counts
// traffic by it. A request carrying a fresh identifier every time is a new
// anonymous session on every call, and that is the one shape the reference
// client never produces — measured on its own traffic, its turns share one
// `ses_` value per conversation.
//
// WHAT WAS WRONG. Our client sends no identifier headers at all, so the
// provider minted a new session per request (sessionids.go). Nothing in the
// answer broke, which is why it went unnoticed: the loss is invisible, and it
// is the cache affinity the same header exists for.
//
// THE FINGERPRINT IS THE CONVERSATION PREFIX. The system part and the first
// user message do not change while the conversation grows, so together they
// identify it across turns without the client telling us anything. Two
// conversations that open with byte-identical system and first user message
// share an identifier — a shared cache affinity between two of our own
// sessions, never a mix-up of content: the value is an opaque label.
//
// A conversation compacted mid-flight changes its prefix and so gets a new
// identifier. The cost is a lost affinity, not a failed call.
const (
	// defaultSessionCacheSize bounds what the mapping may cost the process. One
	// conversation is a hash and one short string; the ceiling is set so that a
	// day of agent work with subagents does not grow it without limit.
	defaultSessionCacheSize = 1024
)

// sessionCache maps a conversation fingerprint to the identifier minted for it.
type sessionCache struct {
	mu    sync.Mutex
	limit int
	order []string
	ids   map[string]string
}

// newSessionCache creates a cache holding at most limit conversations.
func newSessionCache(limit int) *sessionCache {
	if limit <= 0 {
		limit = defaultSessionCacheSize
	}
	return &sessionCache{limit: limit, ids: make(map[string]string, limit)}
}

// get returns the identifier remembered for the fingerprint.
func (c *sessionCache) get(fingerprint string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id, ok := c.ids[fingerprint]
	return id, ok
}

// put remembers the identifier, dropping the oldest conversation at the
// ceiling.
//
// The eviction order is insertion order rather than least-recently-used: a
// conversation returns new turns for minutes, and the oldest entry is the one
// least likely to be resumed.
func (c *sessionCache) put(fingerprint, id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.ids[fingerprint]; ok {
		return
	}
	for len(c.order) >= c.limit {
		delete(c.ids, c.order[0])
		c.order = c.order[1:]
	}
	c.ids[fingerprint] = id
	c.order = append(c.order, fingerprint)
}

// size reports how many conversations are remembered.
func (c *sessionCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.ids)
}

// stableSession returns the identifiers to put into the headers.
//
// An identifier the client supplied is the client's and travels unchanged; only
// a minted one is replaced by the identifier of the conversation it belongs to.
func (p *Provider) stableSession(req provider.Request) provider.SessionIDs {
	ids := req.Session
	if !ids.SessionMinted || p.sessions == nil {
		return ids
	}

	fingerprint := conversationFingerprint(req)
	if fingerprint == "" {
		return ids
	}

	stable, known := p.sessions.get(fingerprint)
	if !known {
		stable = ids.Session
		p.sessions.put(fingerprint, stable)
	}

	// The affinity header carries the affinity and falls back to the session,
	// so it is rewritten only when it was the minted identifier itself: a value
	// the client sent is never touched.
	if ids.Affinity == ids.Session {
		ids.Affinity = stable
	}
	ids.Session = stable
	return ids
}

// conversationFingerprint identifies a conversation by the part of it that the
// next turn does not change.
func conversationFingerprint(req provider.Request) string {
	first := ""
	for _, m := range req.Messages {
		if m.Role == "user" {
			first = m.Text
			break
		}
	}
	if first == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(req.System + "\x00" + first))
	return hex.EncodeToString(sum[:16])
}
