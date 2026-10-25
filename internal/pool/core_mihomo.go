package pool

import (
	"encoding/json"
	"fmt"
)

// mihomoProxyName is the only name in a rendered config: one node, one rule.
const mihomoProxyName = "node"

type mihomo struct{}

// NewMihomo is the default core: it renders what the pass read, which is already
// the core's own profile, for every scheme the pass carries.
func NewMihomo() Core { return mihomo{} }

func (mihomo) ID() string { return "mihomo" }

func (mihomo) Supports(n Node) bool {
	return supportsProto(n.Proto)
}

// Config renders JSON rather than YAML. mihomo reads both, and JSON is what
// encoding/json can produce without a dependency.
func (mihomo) Config(n Node, port int) ([]byte, error) {
	proxy, err := mihomoProxy(n)
	if err != nil {
		return nil, err
	}
	cfg := map[string]any{
		"log-level":  "warning",
		"mixed-port": port,
		"allow-lan":  false,
		"mode":       "rule",
		"ipv6":       true,
		"proxies":    []any{proxy},
		"rules":      []any{"MATCH," + mihomoProxyName},
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// mihomoProxy hands over the profile the pass read from the subscription.
//
// Rendering it again from parsed fields would be a lossy round trip through a
// vocabulary that does not cover xhttp, packet-encoding, reality spellings and the
// rest of the long tail; the pass's copy is the only faithful one.
func mihomoProxy(n Node) (map[string]any, error) {
	if proxy, ok := clashProxy(n.Raw); ok {
		return proxy, nil
	}
	return nil, fmt.Errorf("%s: the pass carries no core profile for this node", n.Label)
}
