// Package capability holds the reusable circumvention toolkit.
//
// WHY A SEPARATE PACKAGE. Different free-model providers often defend themselves
// in similar ways, and a single technique helps several of them at once. Key
// rotation works for anyone who hands out keys. Request-shape substitution is
// needed by everyone who filters clients by the look of the request. A response
// cache is useful wherever the provider counts requests.
//
// If every provider carried its own copy of such code, two problems would
// follow: similar providers would end up with identical copies, and a fix found
// in one would never reach the others. So the mechanisms live here, and a
// provider REFERENCES the ones it needs — by name, from the configuration.
//
// The package deliberately depends on neither provider nor gateway: it knows
// nothing about requests or models. Each mechanism is a small function over a
// shared data type, and the mechanisms can be chained together.
package capability

import (
	"fmt"
	"sort"
	"sync"
)

// Kind distinguishes the sorts of mechanisms, so they can be selected by name
// from the configuration without declaring a separate type for each.
type Kind string

const (
	// KindKeyRotation — replacing credentials when the key is refused.
	KindKeyRotation Kind = "key_rotation"
	// KindDisguise — substituting the request shape for that of another client.
	KindDisguise Kind = "disguise"
	// KindRetry — retrying the request under its own policy.
	KindRetry Kind = "retry"
)

// Info describes a mechanism for the interfaces and the help text.
//
// The description exists so that `free-ai-router capabilities` and the TUI can
// show what the system has at all, without knowing a single implementation.
type Info struct {
	// Kind — the sort of mechanism.
	Kind Kind
	// Name — the name by which the mechanism is selected in the configuration.
	Name string
	// Summary — one line about what the mechanism does.
	Summary string
	// Providers — the providers the mechanism is meant for. Empty means
	// "good for any".
	Providers []string
}

// Factory creates an instance of a mechanism.
//
// The mechanism receives its settings as map[string]any: different mechanisms
// have different parameters, and a shared type would describe only the
// intersection. Parsing the values is the mechanism's own business, and it is
// likewise the mechanism that reports the error.
type Factory func(cfg map[string]any) (any, error)

// Registry holds the available mechanisms.
//
// The registry exists so a provider does not build mechanisms itself: it merely
// declares which ones it needs and receives them ready-made. That way one and
// the same mechanism is shared by several providers in a single process.
type Registry struct {
	mu        sync.RWMutex
	factories map[Kind]map[string]Factory
	infos     map[Kind]map[string]Info
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		factories: make(map[Kind]map[string]Factory),
		infos:     make(map[Kind]map[string]Info),
	}
}

// Register adds a mechanism.
func (r *Registry) Register(info Info, factory Factory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.factories[info.Kind] == nil {
		r.factories[info.Kind] = make(map[string]Factory)
		r.infos[info.Kind] = make(map[string]Info)
	}
	r.factories[info.Kind][info.Name] = factory
	r.infos[info.Kind][info.Name] = info
}

// Build creates a mechanism by kind and name.
func (r *Registry) Build(kind Kind, name string, cfg map[string]any) (any, error) {
	r.mu.RLock()
	factory, ok := r.factories[kind][name]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("capability: %s %q is not registered", kind, name)
	}
	return factory(cfg)
}

// Infos returns the descriptions of the mechanisms of one kind in a stable
// order.
func (r *Registry) Infos(kind Kind) []Info {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Info, 0, len(r.infos[kind]))
	for _, info := range r.infos[kind] {
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Names returns the names of the mechanisms of one kind.
func (r *Registry) Names(kind Kind) []string {
	infos := r.Infos(kind)
	out := make([]string, 0, len(infos))
	for _, info := range infos {
		out = append(out, info.Name)
	}
	return out
}
