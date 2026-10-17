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
// Rotation is wired in through the KeyRotation field. If the mechanism is
// absent, the gateway runs anonymously: the provider gets an empty key and
// substitutes its own anonymity marker. That is a normal mode, not a missing
// setting.

// CredentialSource issues the credentials for a request.
type CredentialSource interface {
	// Next issues the next key.
	Next() (string, bool)
}

// credentialFor returns the key for a request.
//
// If a rotation mechanism is wired in, the key comes from it. Otherwise an
// empty string is returned: the provider will substitute the anonymity marker
// itself, and that is the right behaviour for a deployment that did not ask to
// spend keys.
func (h *Handler) credentialFor(p provider.Provider) string {
	if h.KeyRotation == nil {
		return ""
	}
	name := string(p.ID())
	kr, ok := h.KeyRotation[name]
	if !ok || kr == nil {
		return ""
	}
	key, _ := kr.Next()
	return key
}

// alternativeKey looks for another key of the same provider.
//
// It is needed on a retry: the current key has been rejected, and it is
// precisely a different one that is needed. If there is no other, the retry is
// pointless, and the caller must hand the refusal to the client.
func (h *Handler) alternativeKey(p provider.Provider, current string) (string, bool) {
	if h.KeyRotation == nil {
		return "", false
	}
	kr, ok := h.KeyRotation[string(p.ID())]
	if !ok || kr == nil {
		return "", false
	}
	return kr.Take(current)
}

// hasKeyRotation reports whether the mechanism is wired in for the provider.
//
// A separate method, because the question "are there keys" is asked twice: when
// issuing and when deciding on a retry.
func (h *Handler) hasKeyRotation(p provider.Provider) bool {
	if h.KeyRotation == nil {
		return false
	}
	kr, ok := h.KeyRotation[string(p.ID())]
	return ok && kr != nil && kr.HasAlternatives()
}

// keyRotationRegistry is the store of rotation mechanisms by provider.
//
// The key is the provider identifier: each has its own keys, and they must not
// be mixed. The mechanism is shared in implementation but separate in state:
// otherwise the keys of one service would end up in requests to another.
type keyRotationRegistry map[string]*capability.KeyRotation

// NewKeyRotationRegistry creates an empty store.
func NewKeyRotationRegistry() keyRotationRegistry { return keyRotationRegistry{} }

// Add registers a mechanism for a provider.
func (r keyRotationRegistry) Add(id provider.ID, kr *capability.KeyRotation) {
	if kr == nil {
		return
	}
	r[string(id)] = kr
}

// Keys returns the provider's keys for reference and for interfaces.
func (r keyRotationRegistry) Keys(id provider.ID) []string {
	kr, ok := r[string(id)]
	if !ok || kr == nil {
		return nil
	}
	return kr.KeysSnapshot()
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
