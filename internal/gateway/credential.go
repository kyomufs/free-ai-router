package gateway

import (
	"free-ai-router/internal/capability"
	"free-ai-router/internal/provider"
)

// Issuing credentials.
//
// WHY THIS IS HERE AND NOT IN THE PROVIDER. The provider assembles the HEADERS
// but does not decide which key to put in them: keys are issued by the shared
// rotation mechanism, and there is one of it for all key-based providers. All
// that is left to the provider is the shape — the header name and the prefix.
//
// THE CREDENTIAL AXIS DECIDES THE SOURCE, and the pool only supplies the value.
// That is the whole point of the axis: "anonymous" is not a statement about
// whether a key happens to be lying around, it is an instruction not to present
// one. A deployment that listed keys and then switched the credential to
// anonymous asked to reach the service without them — a pool left in the file is
// not consent to spend it.
//
// This used to work the other way: the axis gated rotation only, and any key
// present was sent regardless. The result was a mode called "anonymous" that
// quietly presented a key, which is a lie in the log and in the monitor, and a
// surprising bill for whoever put a key in the file and expected it to be
// unused.

// CredentialSource issues the credentials for a request.
type CredentialSource interface {
	// Next issues the next key.
	Next() (string, bool)
}

// credentialFor returns the key for a request.
//
// The credential axis is consulted first: under "anonymous" the answer is an
// empty string without the pool being asked at all, and the provider substitutes
// its own anonymity marker. Under "account" the key comes from the pool, and a
// missing pool is a configuration error rather than a silent fallback —
// validation refuses to start such a deployment, so reaching this point without
// a key means the pool was emptied after the start.
//
// An absent resolver keeps the older behaviour, which is the pool's presence.
// That is for tests that wire a registry without a configuration; the daemon
// always sets the resolver.
func (h *Handler) credentialFor(p provider.Provider) string {
	switch h.credentialKind(p) {
	case credentialAnonymous:
		return ""
	case credentialAccount:
		return h.nextKey(p)
	}

	// Not told which credential is in force: fall back to what the pool says.
	return h.nextKey(p)
}

// The credential kinds, spelled as the configuration spells them.
//
// Duplicated as constants rather than imported: the gateway must not depend on
// the configuration package, or the axis would be read in two places at once.
// The values are part of the configuration's public surface, so a change there
// is a change here, and the test that compares them is what keeps them together.
const (
	credentialAnonymous = "anonymous"
	credentialAccount   = "account"
)

// credentialKind reports which credential is in force for the provider.
func (h *Handler) credentialKind(p provider.Provider) string {
	if h.Credential == nil {
		return ""
	}
	return h.Credential(p.ID())
}

// nextKey takes the next key from the provider's pool.
func (h *Handler) nextKey(p provider.Provider) string {
	if h.KeyRotation == nil {
		return ""
	}
	kr, ok := h.KeyRotation[string(p.ID())]
	if !ok || kr == nil {
		return ""
	}
	key, _ := kr.Next()
	return key
}

// issuesCredentials reports whether requests to the provider carry a key.
//
// It is the question the monitor asks, and it is answered by the same function
// that hands out the key — otherwise the two can disagree, and the monitor would
// claim a mode the request path does not follow.
func (h *Handler) issuesCredentials(p provider.Provider) bool {
	if h.credentialKind(p) == credentialAnonymous {
		return false
	}
	if h.KeyRotation == nil {
		return false
	}
	kr, ok := h.KeyRotation[string(p.ID())]
	return ok && kr != nil && kr.Len() > 0
}

// alternativeKey looks for another key of the same provider.
//
// It is needed on a retry: the current key has been rejected, and it is
// precisely a different one that is needed. If there is no other, the retry is
// pointless, and the caller must hand the refusal to the client.
//
// UNDER "anonymous" THERE IS NEVER AN ALTERNATIVE. The mechanism may hold keys
// and may have rotation switched on, but the credential in force says no key is
// presented, so there is nothing to replace — and reporting an alternative would
// make the retry loop reach for a key the mode forbids. This is where the axis
// and the rotation switch meet, and they are not the same question: the axis says
// whether a key is presented at all, the switch says whether a refused key is
// replaced.
//
// The answer also carries the rotation policy: a mechanism with rotation
// switched off never has an alternative. That is deliberate — "is there another
// key to try" is one question, and splitting it in two would leave the retry
// loop checking a setting the mechanism already knows about.
func (h *Handler) alternativeKey(p provider.Provider, current string) (string, bool) {
	if h.credentialKind(p) == credentialAnonymous {
		return "", false
	}
	if h.KeyRotation == nil {
		return "", false
	}
	kr, ok := h.KeyRotation[string(p.ID())]
	if !ok || kr == nil {
		return "", false
	}
	return kr.Take(current)
}

// retryFor returns the provider's retry policy, or nil when it has none.
//
// nil is not an error state. It says the provider did not ask for its refusals to
// be repeated, and the refusal then goes to the client as it arrived.
func (h *Handler) retryFor(p provider.Provider) *capability.Retry {
	if h.Retry == nil {
		return nil
	}
	policy, ok := h.Retry[string(p.ID())]
	if !ok {
		return nil
	}
	return policy
}

// KeyRotationRegistry is the management of rotation mechanisms.
//
// An interface rather than a concrete type: the control API and the interfaces
// must not depend on how key storage is arranged. It is enough for them to be
// able to ask and to change.
type KeyRotationRegistry interface {
	// Keys returns the provider's keys.
	Keys(id provider.ID) []string
	// KeyFingerprints returns the provider's key fingerprints.
	KeyFingerprints(id provider.ID) []string
	// HasRotation reports whether the mechanism is wired in for the provider.
	HasRotation(id provider.ID) bool
	// AddKey adds a key and reports whether it was new.
	AddKey(id provider.ID, key string) bool
	// RemoveFingerprint removes a key by fingerprint.
	RemoveFingerprint(id provider.ID, fingerprint string) bool
}
