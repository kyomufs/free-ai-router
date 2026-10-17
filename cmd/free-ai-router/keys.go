package main

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"free-ai-router/internal/capability"
	"free-ai-router/internal/config"
	"free-ai-router/internal/gateway"
	"free-ai-router/internal/provider"
)

// keyRegistry holds the key rotation mechanisms per provider.
//
// A SEPARATE MECHANISM PER PROVIDER, not one shared. The implementation is
// identical, but the state is separate: having mixed the keys, the gateway would
// send one service's key in requests to another, and the usage counters would be
// attributed to the wrong one.
//
// The mechanism is attached only where keys exist at all. A provider without
// keys works anonymously, and that is a NORMAL mode, not a missing setting:
// services with a free tier are built exactly that way.
type keyRegistry struct {
	byProvider map[provider.ID]*capability.KeyRotation
	order      []provider.ID
}

// newKeyRegistry assembles the mechanisms from the configuration.
//
// A provider's keys come from its section: first the value in the file, then the
// variable named alongside it. The fingerprints for reference are computed right
// here, so that the key itself does not travel further through the code.
func newKeyRegistry(cfg *config.Config, providers *provider.Registry) *keyRegistry {
	r := &keyRegistry{byProvider: make(map[provider.ID]*capability.KeyRotation)}

	for _, p := range providers.All() {
		id := p.ID()
		pc, ok := cfg.Providers[string(id)]
		if !ok {
			continue
		}
		r.order = append(r.order, id)

		keys := keysFromConfig(pc)
		if len(keys) == 0 {
			// No keys: the mechanism is not created, and the provider
			// will run anonymously.
			continue
		}

		built, err := capability.NewKeyRotation(map[string]any{"keys": keys})
		if err != nil {
			continue // the settings shape has already been checked by the catalog
		}
		kr, ok := built.(*capability.KeyRotation)
		if !ok {
			continue
		}
		r.byProvider[id] = kr
	}
	return r
}

// keysFromConfig extracts a provider's list of keys.
func keysFromConfig(pc config.ProviderConfig) []any {
	// The primary source is the section fields. The list may also live in the
	// settings: that is more convenient when there are many keys and they are
	// kept separately.
	var raw []any
	if k := pc.ProviderKey(); k != "" {
		raw = append(raw, k)
	}
	if extra, ok := pc.Settings["keys"].([]any); ok {
		raw = append(raw, extra...)
	}
	if extra, ok := pc.Settings["keys"].([]string); ok {
		for _, k := range extra {
			raw = append(raw, k)
		}
	}
	return raw
}

// rotationMap hands out the map for the request surface.
func (r *keyRegistry) rotationMap() map[string]*capability.KeyRotation {
	out := make(map[string]*capability.KeyRotation, len(r.byProvider))
	for id, kr := range r.byProvider {
		out[string(id)] = kr
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// firstProvider returns the default provider.
//
// The first in configuration order: when the client did not name a provider, it
// is more reasonable to take a predictable one than a random one.
func (r *keyRegistry) firstProvider() provider.ID {
	if len(r.order) == 0 {
		return ""
	}
	return r.order[0]
}

// Keys implements gateway.KeyRotationRegistry.
func (r *keyRegistry) Keys(id provider.ID) []string {
	kr, ok := r.byProvider[id]
	if !ok {
		return nil
	}
	return kr.KeysSnapshot()
}

// KeyFingerprints implements gateway.KeyRotationRegistry.
//
// FINGERPRINTS are handed out, not keys: the reference is needed to learn how
// many keys are in use, not to see the secret. A fingerprint is short — enough
// to tell keys apart and not enough to reconstruct one.
func (r *keyRegistry) KeyFingerprints(id provider.ID) []string {
	kr, ok := r.byProvider[id]
	if !ok {
		return nil
	}
	keys := kr.KeysSnapshot()
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, fingerprint(k))
	}
	return out
}

// HasRotation implements gateway.KeyRotationRegistry.
func (r *keyRegistry) HasRotation(id provider.ID) bool {
	kr, ok := r.byProvider[id]
	return ok && kr.Len() > 1
}

// AddKey implements gateway.KeyRotationRegistry.
func (r *keyRegistry) AddKey(id provider.ID, key string) bool {
	kr, ok := r.byProvider[id]
	if !ok {
		return false
	}
	return kr.Add(key)
}

// RemoveFingerprint implements gateway.KeyRotationRegistry.
//
// Removal goes by fingerprint: the remover is not obliged to know the secret —
// what the reference showed is enough for it.
func (r *keyRegistry) RemoveFingerprint(id provider.ID, fp string) bool {
	kr, ok := r.byProvider[id]
	if !ok {
		return false
	}
	for _, k := range kr.KeysSnapshot() {
		if fingerprint(k) == fp {
			return kr.Remove(k)
		}
	}
	return false
}

// fingerprint returns a short fingerprint of the key.
//
// The length is chosen so that an accidental collision would be improbable while
// the value stays convenient for the eye and for copying.
func fingerprint(key string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return hex.EncodeToString(sum[:4])
}

// Build-time check: the registry is obliged to satisfy the interface the control
// surface expects.
var _ gateway.KeyRotationRegistry = (*keyRegistry)(nil)
