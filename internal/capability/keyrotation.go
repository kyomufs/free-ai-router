package capability

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
)

// Fingerprint returns a short fingerprint of a credential.
//
// It exists so that a key can be named in the journal, in the counters and on
// the monitor without the key itself being written anywhere. Sixteen hex
// characters would be enough to be unique; eight are what fits beside a counter
// without pushing it off the screen, and a collision between two keys of one
// deployment is not a real risk.
//
// The value is trimmed first because a key that arrived from an environment
// variable or a file commonly carries a trailing newline, and a fingerprint that
// differed from the one used for the counters would break exactly the
// correlation it exists to make possible.
func Fingerprint(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:4])
}

// KeyRotation — the mechanism that replaces credentials when the key is
// refused.
//
// SCOPE. The mechanism does not know which provider it serves or what its
// models are called. It is responsible for exactly one thing: to hand out the
// next credential string and to say whether any alternatives remain. That is
// why it suits any provider that works with keys, and asks nothing of the
// provider beyond the sign "it was the key that got rejected".
//
// WHY NOT IN THE PROVIDER. Rotation is not a property of a service but a way to
// get around a per-key request limit. The services differ; the technique is one.
// Keeping it in the provider would mean copying identical code into places that
// merely happened to look alike.
type KeyRotation struct {
	mu sync.Mutex

	// keys — the available keys. An empty list means "no key", and then the
	// mechanism honestly reports that there is nothing to change.
	keys []string

	// next — the index of the next key. The mechanism hands out keys in a
	// cycle, so that usage is spread evenly and none of them burns out first.
	next int

	// lastUsed — the index of the last key handed out. Needed so that the same
	// key is not handed out twice in a row.
	lastUsed int

	// rotate — whether a refusal that names the key is answered by taking
	// another one.
	//
	// A switch on the MECHANISM, not a policy held outside it. Issuing a key and
	// replacing a refused one are two different questions: with rotation off the
	// mechanism still has to hand out the key on every request — the deployment
	// asked for a credential — it just stops treating a refusal as a reason to
	// reach for the next one.
	//
	// Keeping the switch outside would mean every caller of Take repeating the
	// same check, and one of them forgetting it.
	rotate bool
}

// KeyRotationConfig — the mechanism's settings, parsed from the configuration.
type KeyRotationConfig struct {
	// Keys — the list of keys.
	Keys []string
	// StartAt — which key to start from. A negative value means "the first".
	StartAt int
	// Rotate — replace a refused key. Absent means on: a deployment that listed
	// several keys wanted them spent, and the mechanism's whole reason for
	// existing is to spend them.
	Rotate *bool
}

// NewKeyRotation creates the mechanism from the configuration.
//
// An empty key list is not an error: a provider may work without keys, and then
// the mechanism simply always answers "nothing to change". It would be an error
// to refuse to register the mechanism for want of keys — that would deprive the
// operator of the ability to add them later.
func NewKeyRotation(cfg map[string]any) (any, error) {
	var parsed KeyRotationConfig

	raw, ok := cfg["keys"]
	if ok {
		switch v := raw.(type) {
		case []string:
			parsed.Keys = append(parsed.Keys, v...)
		case []any:
			for i, item := range v {
				s, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("key_rotation: keys[%d] is %T, want a string", i, item)
				}
				parsed.Keys = append(parsed.Keys, s)
			}
		default:
			return nil, fmt.Errorf("key_rotation: keys is %T, want a list of strings", raw)
		}
	}

	if raw, ok := cfg["startAt"]; ok {
		switch v := raw.(type) {
		case int:
			parsed.StartAt = v
		case float64:
			parsed.StartAt = int(v)
		default:
			return nil, fmt.Errorf("key_rotation: startAt is %T, want a number", raw)
		}
	}

	if raw, ok := cfg["rotate"]; ok {
		v, ok := raw.(bool)
		if !ok {
			return nil, fmt.Errorf("key_rotation: rotate is %T, want a boolean", raw)
		}
		parsed.Rotate = &v
	}

	start := parsed.StartAt
	if start < 0 || start >= len(parsed.Keys) {
		start = 0
	}

	// Drop empty keys: an empty string in the list is almost always the trace of
	// a stray comma in the configuration, and keeping it would mean handing out
	// a key that is bound to be rejected.
	//
	// Drop repeats as well. A repeat is not a rarity but the ordinary outcome of
	// assembly: the daemon counts the keys of a section from three places at
	// once (api_key, api_key_env, settings.keys), and an operator who wrote the
	// same key in two of them meant one key, not two. Keeping the repeat would
	// make Len lie and turn HasAlternatives into a false "there is another one"
	// — while Next and Take, facing two equal strings, would hand back the very
	// key that had just been refused. Rotation would stop working exactly where
	// it is switched on, and silently.
	keys := make([]string, 0, len(parsed.Keys))
	for _, k := range parsed.Keys {
		if k == "" {
			continue
		}
		duplicate := false
		for _, existing := range keys {
			if existing == k {
				duplicate = true
				break
			}
		}
		if !duplicate {
			keys = append(keys, k)
		}
	}

	rotate := parsed.Rotate == nil || *parsed.Rotate
	return &KeyRotation{keys: keys, next: start, lastUsed: -1, rotate: rotate}, nil
}

// Len reports how many keys are available.
func (k *KeyRotation) Len() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.keys)
}

// HasAlternatives reports whether there is a key different from the current
// one.
//
// False means a retry makes no sense: either there is only one key, or there
// are none at all, or rotation is switched off. That is the case where the
// gateway must hand the refusal back to the client instead of spending time on a
// plainly fruitless attempt.
func (k *KeyRotation) HasAlternatives() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.rotate && len(k.keys) > 1
}

// Rotate reports whether replacing a refused key is in force.
func (k *KeyRotation) Rotate() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.rotate
}

// Next hands out the next key and the sign that there is a key.
//
// Keys are handed out in a cycle. The key returned is remembered as used, so
// that the next call yields a different one: on a retry the point is exactly to
// get a different key, not the same one.
func (k *KeyRotation) Next() (string, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.keys) == 0 {
		return "", false
	}

	if len(k.keys) == 1 {
		key := k.keys[0]
		k.lastUsed = 0
		return key, true
	}

	// Skip the key handed out last time: the retry must proceed with a
	// different one.
	for i := 0; i < len(k.keys); i++ {
		idx := k.next % len(k.keys)
		k.next++
		if idx == k.lastUsed {
			continue
		}
		k.lastUsed = idx
		return k.keys[idx], true
	}

	// Reachable only with a single key, which is covered above.
	return "", false
}

// Take returns a key different from the one named.
//
// Used on a retry: the current key is known, and it is precisely another one
// that is needed. If there is no other — or rotation is switched off — the
// second result is false, and the retry is pointless.
func (k *KeyRotation) Take(current string) (string, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.rotate || len(k.keys) <= 1 {
		return "", false
	}
	for i := 0; i < len(k.keys); i++ {
		idx := k.next % len(k.keys)
		k.next++
		if k.keys[idx] == current {
			continue
		}
		k.lastUsed = idx
		return k.keys[idx], true
	}
	return "", false
}

// Add adds a key if it is not there yet.
//
// Returns false if the key was already present: adding it again must change
// neither the traversal order nor the counters.
func (k *KeyRotation) Add(key string) bool {
	if key == "" {
		return false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, existing := range k.keys {
		if existing == key {
			return false
		}
	}
	k.keys = append(k.keys, key)
	return true
}

// Remove deletes a key.
func (k *KeyRotation) Remove(key string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	for i, existing := range k.keys {
		if existing != key {
			continue
		}
		k.keys = append(k.keys[:i], k.keys[i+1:]...)
		if k.lastUsed >= len(k.keys) {
			k.lastUsed = -1
		}
		if k.next > len(k.keys) {
			k.next = 0
		}
		return true
	}
	return false
}

// KeysSnapshot returns a copy of the key list.
//
// A copy, precisely, and not a slice: the caller must not gain access to the
// internal state, otherwise the lock would stop protecting anything.
func (k *KeyRotation) KeysSnapshot() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make([]string, len(k.keys))
	copy(out, k.keys)
	return out
}

// Info describes the mechanism for the help text and the interfaces.
func (k *KeyRotation) Info() Info {
	return Info{
		Kind:    KindKeyRotation,
		Name:    "pool",
		Summary: "switches to another key from the list when the provider rejects the current one",
	}
}
