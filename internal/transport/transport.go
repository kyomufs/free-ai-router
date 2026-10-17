// Package transport describes WHERE the traffic to the provider goes.
//
// WHY A SEPARATE AXIS. The transport depends neither on which service it is nor
// on how that service defends itself. Proxying through someone else's server
// works the same for all providers: only the address the request egresses
// through changes. That is why the transport is taken out of the provider —
// otherwise every provider would carry a copy of the same mechanism.
//
// What the transport does NOT do:
//   - does not know about the provider's models, keys and headers;
//   - does not decide whether to repeat a request;
//   - does not parse the response.
//
// It answers exactly one question: how to get an http.RoundTripper through
// which the request will go.
package transport

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"
)

// ID identifies one transport implementation. It appears in configuration and
// in the CLI, so it must be stable.
type ID string

const (
	// Direct is direct host routing. The only transport that exists today.
	Direct ID = "direct"
)

// Transport is what implements a particular way of reaching the network.
//
// The interface is deliberately small: one transport factory and a lifecycle.
// Everything that makes egress complicated (helper processes, address
// rotation, connection warm-up) stays inside the implementation.
type Transport interface {
	// ID returns a stable identifier.
	ID() ID
	// RoundTripper returns a transport for one request.
	//
	// It is returned per request rather than stored: an implementation may have
	// several lanes, and choosing the lane is its internal business.
	RoundTripper() (http.RoundTripper, error)
	// Start brings up everything the transport needs to work. Called once at
	// daemon startup.
	Start(ctx context.Context) error
	// Stop releases resources.
	Stop() error
	// Info describes the transport for interfaces.
	Info() Info
}

// Info describes the transport for reference and for interfaces.
type Info struct {
	// ID is the identifier.
	ID ID
	// Summary is one line about what the transport does.
	Summary string
	// NeedsExternalProcess reports whether the transport brings up a third-party
	// process.
	//
	// This field exists so that interfaces and setup checks can warn the
	// operator: a transport that needs an external binary requires more than
	// configuration.
	NeedsExternalProcess bool
}

// Factory creates a transport from configuration.
type Factory func(cfg map[string]any) (Transport, error)

// Registry stores the available transports.
type Registry struct {
	mu        sync.RWMutex
	factories map[ID]Factory
	infos     map[ID]Info
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{factories: make(map[ID]Factory), infos: make(map[ID]Info)}
}

// Register adds a transport.
func (r *Registry) Register(info Info, factory Factory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[info.ID] = factory
	r.infos[info.ID] = info
}

// Build creates a transport by identifier.
func (r *Registry) Build(id ID, cfg map[string]any) (Transport, error) {
	r.mu.RLock()
	factory, ok := r.factories[id]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("transport: %q is not registered", id)
	}
	if factory == nil {
		return nil, fmt.Errorf("transport: %q has no factory", id)
	}
	return factory(cfg)
}

// Infos returns the descriptions in a stable order.
func (r *Registry) Infos() []Info {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Info, 0, len(r.infos))
	for _, info := range r.infos {
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// IDs returns the identifiers in a stable order.
func (r *Registry) IDs() []ID {
	infos := r.Infos()
	out := make([]ID, 0, len(infos))
	for _, info := range infos {
		out = append(out, info.ID)
	}
	return out
}
