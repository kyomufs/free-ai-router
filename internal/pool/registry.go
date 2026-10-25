package pool

import (
	"sort"
	"sync"
	"time"
)

// Status is where a node stands.
type Standing string

const (
	// StandingActive is a node in the relevant list.
	StandingActive Standing = "active"
	// StandingQuarantined is a node that failed in service. It keeps its
	// measurements and is retried at the next evaluation, which is the only
	// thing allowed to clear it.
	StandingQuarantined Standing = "quarantined"
)

// Entry is a node with its standing, for the state file and the interfaces.
type Entry struct {
	Node     Node      `json:"node"`
	Standing Standing  `json:"status"`
	Reason   string    `json:"reason,omitempty"`
	Since    time.Time `json:"since,omitempty"`
}

// Snapshot is the registry as the surfaces see it.
type Snapshot struct {
	Active      string  `json:"active,omitempty"`
	Relevant    int     `json:"relevant"`
	Quarantined []Entry `json:"quarantined,omitempty"`
	Nodes       []Node  `json:"nodes,omitempty"`
}

// Registry is the ranked list of usable servers plus the rotation cursor.
//
// It is the only place that decides which node serves next, and it does so without
// consulting any core: the measurements were made before a node got here.
type Registry struct {
	mu         sync.Mutex
	maxNodes   int
	threshold  int
	nodes      []Node
	active     string
	cursor     int
	failures   map[string]int
	quarantine map[string]Entry
}

// NewRegistry takes the size of the list and how many consecutive failures
// quarantine a node.
func NewRegistry(maxNodes, deadThreshold int) *Registry {
	if maxNodes < 1 {
		maxNodes = 1
	}
	if deadThreshold < 1 {
		deadThreshold = 1
	}
	return &Registry{
		maxNodes:   maxNodes,
		threshold:  deadThreshold,
		failures:   make(map[string]int),
		quarantine: make(map[string]Entry),
	}
}

// Replace installs a freshly evaluated list.
//
// It is called only after a complete evaluation, so a failed one cannot destroy
// what is serving. The exit pool is what gets deduplicated: servers behind one
// address network share the provider's budget, and counting them twice would
// spend the pool on one budget. The fastest node of a pool survives; the rest are
// not spare capacity, they are the same capacity measured twice.
func (r *Registry) Replace(nodes []Node) {
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := dedupeByPool(nodes)
	sort.SliceStable(kept, func(i, j int) bool { return rank(kept[i]) < rank(kept[j]) })
	r.failures = make(map[string]int)
	r.quarantine = make(map[string]Entry)
	r.truncate(kept)
}

// Restore installs a list read from disk, quarantine included.
func (r *Registry) Restore(nodes []Node, quarantined []Entry, active string) {
	r.Replace(nodes)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range quarantined {
		if e.Node.ID != "" {
			r.quarantine[e.Node.ID] = e
		}
	}
	// The serving node is restored only if it is still in the list: the file may
	// describe a node the subscription has since dropped.
	for _, n := range r.nodes {
		if n.ID == active {
			r.active = active
			return
		}
	}
}

// Active reports the node currently serving.
func (r *Registry) Active() (Node, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.activeNode()
}

// Next advances the cursor and returns the node to use.
//
// Rotation is a cursor rather than a random pick: a random pick can return the
// same node twice in a row, which defeats the point of rotating.
func (r *Registry) Next() (Node, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.nodes) == 0 {
		r.active = ""
		return Node{}, false
	}
	for i := 0; i < len(r.nodes); i++ {
		idx := (r.cursor + i) % len(r.nodes)
		n := r.nodes[idx]
		if _, bad := r.quarantine[n.ID]; bad {
			continue
		}
		r.cursor = (idx + 1) % len(r.nodes)
		r.active = n.ID
		return n, true
	}
	r.active = ""
	return Node{}, false
}

// Suspect records a failure. The node stays in service until it reaches the
// threshold: a single broken connection is normal on these servers and
// quarantining on it would empty the list in a minute.
func (r *Registry) Suspect(id, reason string) {
	if id == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures[id]++
	if r.failures[id] < r.threshold {
		return
	}
	entry := Entry{Node: r.nodeLocked(id), Standing: StandingQuarantined, Reason: reason, Since: time.Now()}
	entry.Node.ID = id
	r.quarantine[id] = entry
	delete(r.failures, id)
}

// Succeed clears the failure count of a node that answered.
func (r *Registry) Succeed(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.failures, id)
}

// Snapshot copies the registry for the surfaces.
func (r *Registry) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := Snapshot{Active: r.active, Relevant: len(r.nodes), Nodes: append([]Node(nil), r.nodes...)}
	out.Quarantined = make([]Entry, 0, len(r.quarantine))
	for _, e := range r.quarantine {
		out.Quarantined = append(out.Quarantined, e)
	}
	sort.Slice(out.Quarantined, func(i, j int) bool {
		return out.Quarantined[i].Since.Before(out.Quarantined[j].Since)
	})
	return out
}

// Empty reports whether there is nothing to serve from.
func (r *Registry) Empty() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, n := range r.nodes {
		if _, bad := r.quarantine[n.ID]; !bad {
			return false
		}
	}
	return true
}

func (r *Registry) nodeLocked(id string) Node {
	for _, n := range r.nodes {
		if n.ID == id {
			return n
		}
	}
	return Node{ID: id}
}

// rank puts the unmeasured last: a node with no probe time is a node nobody has
// heard answer.
func rank(n Node) time.Duration {
	if n.ProbeRTT <= 0 {
		return time.Duration(1 << 62)
	}
	return n.ProbeRTT
}

func dedupeByPool(nodes []Node) []Node {
	byPool := make(map[string]int, len(nodes))
	out := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		key := exitPrefix(n.ExitIP)
		if key == "" {
			key = "node:" + n.ID
		}
		idx, seen := byPool[key]
		if !seen {
			byPool[key] = len(out)
			out = append(out, n)
			continue
		}
		if rank(n) < rank(out[idx]) {
			out[idx] = n
		}
	}
	return out
}

// truncate applies the size limit and decides whether the serving node keeps its
// place. A node that survived a re-evaluation keeps serving: restarting a working
// tunnel on a schedule serves nothing.
func (r *Registry) truncate(kept []Node) {
	if len(kept) > r.maxNodes {
		kept = kept[:r.maxNodes]
	}
	r.nodes = kept
	r.cursor = 0
	if _, ok := r.activeNode(); !ok {
		r.active = ""
	}
}

// activeNode reports whether the serving node is still in the list.
func (r *Registry) activeNode() (Node, bool) {
	if r.active == "" {
		return Node{}, false
	}
	for _, n := range r.nodes {
		if n.ID == r.active {
			return n, true
		}
	}
	return Node{}, false
}

// View is a node as the surfaces show it: the measurements, without the link.
//
// The link carries the node's credentials, and a status response is not the place
// for them; the interfaces need the address, the protocol and the timings.
type View struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
	Proto string `json:"proto"`
	Host  string `json:"host"`
	Port  int    `json:"port"`
	// Milliseconds, not the duration it is measured as: the surface says "ms" in
	// the name, and a duration marshals as nanoseconds.
	ProbeRTTMs int64  `json:"probe_rtt_ms,omitempty"`
	ExitIP     string `json:"exit_ip,omitempty"`
	ExitGeo    string `json:"exit_geo,omitempty"`
}

// NodeView is the node as the surfaces see it.
func NodeView(n Node) View {
	return View{
		ID:         n.ID,
		Label:      n.Label,
		Proto:      n.Proto,
		Host:       n.Host,
		Port:       n.Port,
		ProbeRTTMs: n.ProbeRTT.Milliseconds(),
		ExitIP:     n.ExitIP,
		ExitGeo:    n.ExitGeo,
	}
}

// QuarantineView is a quarantined node for the surfaces.
type QuarantineView struct {
	Node   View      `json:"node"`
	Reason string    `json:"reason"`
	Since  time.Time `json:"since"`
}
