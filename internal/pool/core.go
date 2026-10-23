package pool

// Core turns one node into one local proxy.
//
// The surface is deliberately this small. There is no group, no selector, no
// reload and no control API here, because none of those are needed to route one
// connection through one server — and depending on them would pin the pool to one
// project's API decisions. A core that grows a second renderer is a second
// implementation of this interface, nothing more.
type Core interface {
	// ID is the name the core is configured by.
	ID() string
	// Supports reports whether this core can render the node at all.
	Supports(n Node) bool
	// Config renders a configuration that routes every connection of one local
	// port through n.
	Config(n Node, port int) ([]byte, error)
}
