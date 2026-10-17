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
//
// This answer also carries the rotation policy: a mechanism with rotation
// switched off never has an alternative. That is deliberate — "is there another
// key to try" is one question, and splitting it in two would leave the retry
// loop checking a setting the mechanism already knows about.
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
