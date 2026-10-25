package tui

import (
	"fmt"
	"time"
)

// PoolState is the pool transport as the monitor shows it.
//
// The numbers come from the state the transport writes between runs, not from the
// daemon's memory: the monitor is a separate process, and the file is the only thing
// both can see. What the file does not hold — the probe stage in progress — is
// reported as unknown rather than guessed.
type PoolState struct {
	// Active is the server currently serving, empty when none is.
	Active PoolNode
	// Relevant is how many servers are in the list.
	Relevant int
	// Nodes are the relevant servers, best first.
	Nodes []PoolNode
	// Quarantined is how many failed in service and wait for the next check.
	Quarantined int
	// EvalState is what the last evaluation came to: ready, error or never.
	EvalState string
	// EvaluatedAt is when the list was last rebuilt.
	EvaluatedAt time.Time
	// NextEvalAt is when the next check is due.
	NextEvalAt time.Time
	// Error is why the last evaluation failed, empty when it did not.
	Error string
	// Source is where the list came from: a proxprobe pass or the builtin pipeline.
	Source string
	// Pass is the last pass's own counts, zero when the state file predates it.
	Pass PassCounts
}

// PassCounts is what the last pass measured before the pool's own admission.
type PassCounts struct {
	Parsed      int
	HTTPOK      int
	Unsupported int
	Accepted    int
}

// PoolNode is one measured server.
type PoolNode struct {
	Label string
	Host  string
	Port  int
	Proto string
	// RTT is how long the probe took through this server. Zero means it was not
	// measured.
	RTT time.Duration
	// ExitIP is the address the provider saw through this server.
	ExitIP string
	// Geo is the country from the subscription label, when it names one.
	Geo string
}

// poolTile is the pool as a tile.
//
// The first question about a pool is whether anything is serving, and the second is
// which server it is and how it was measured. Both are answered in the first two
// lines, because a reader looking at a proxy that answers slowly needs to see the
// list's state before its contents.
func (m Model) poolTile() tileSpec {
	p := m.obs.Pool
	meta := ""
	if p != nil {
		meta = plural(p.Relevant, "server", "servers")
		if p.Quarantined > 0 {
			meta += " · " + plural(p.Quarantined, "quarantined", "quarantined")
		}
	}

	return tileSpec{
		title:  "pool",
		meta:   meta,
		weight: 1,
		body: func(inner int) []string {
			if p == nil {
				return []string{m.pal.Dim.Render("the pool transport is not in use")}
			}

			out := make([]string, 0, 8+len(p.Nodes))
			out = append(out, m.kvLine("state", 10, m.poolStateText(p)))

			if p.Source != "" {
				out = append(out, m.kvLine("source", 10, m.pal.Value.Render(p.Source)))
			}
			if p.Pass.Parsed > 0 || p.Pass.HTTPOK > 0 {
				out = append(out, m.kvLine("pass", 10, m.pal.Value.Render(fmt.Sprintf(
					"%d parsed · %d http · %d accepted", p.Pass.Parsed, p.Pass.HTTPOK, p.Pass.Accepted))))
			}

			next := "unknown"
			if !p.NextEvalAt.IsZero() {
				next = p.NextEvalAt.Local().Format("15:04") + " (" + untilPhrase(p.NextEvalAt) + ")"
			}
			out = append(out, m.kvLine("next check", 10, m.pal.Value.Render(next)))

			active := "nothing serving"
			if p.Active.Host != "" {
				active = m.pal.Value.Bold(true).Render(m.poolNodeName(p.Active))
			}
			out = append(out, m.kvLine("active", 10, active))

			if p.Error != "" {
				out = append(out, m.kvLine("last error", 10, m.pal.ErrorText.Render(clip(p.Error, max(12, inner-12)))))
			}

			if len(p.Nodes) == 0 {
				out = append(out, "", m.pal.Dim.Render("no server is relevant yet"),
					m.pal.Hint.Render("the list fills in after the first proxprobe pass"))
				return out
			}

			out = append(out, "", m.pal.Rule.Render(repeat("─", max(8, inner))))
			// The list is cut to what the tile can show: a monitor is a screen, and
			// the whole list lives in the control surface for whoever needs it.
			for i, n := range p.Nodes {
				if i >= max(3, min(len(p.Nodes), 6)) {
					out = append(out, m.pal.Dim.Render(fmt.Sprintf("… %d more", len(p.Nodes)-i)))
					break
				}
				rtt := "—"
				if n.RTT > 0 {
					rtt = n.RTT.Round(time.Millisecond).String()
				}
				out = append(out, m.pal.Label.Render(padTo(clip(m.poolNodeName(n), 24), 24))+" "+
					m.pal.Value.Render(padLeft(rtt, 6))+" "+
					m.pal.Dim.Render(clip(m.poolNodeWhere(n), max(8, inner-32))))
			}
			return out
		},
	}
}

// poolStateText is the evaluation state in the words a reader needs.
func (m Model) poolStateText(p *PoolState) string {
	switch p.EvalState {
	case "ready":
		at := "at an unknown time"
		if !p.EvaluatedAt.IsZero() {
			at = since(p.EvaluatedAt) + " ago"
		}
		return m.pal.SuccessText.Render("ready") + " " + m.pal.Dim.Render(at)
	case "error":
		return m.pal.ErrorText.Render("failed")
	case "evaluating":
		return m.pal.WarningText.Render("checking")
	case "", "never":
		return m.pal.Dim.Render("never checked")
	default:
		return m.pal.Dim.Render(p.EvalState)
	}
}

// poolNodeName is the server as the pass names it.
func (m Model) poolNodeName(n PoolNode) string {
	if n.Label != "" {
		return n.Label
	}
	if n.Host == "" {
		return "unknown"
	}
	return n.Host + ":" + itoa(n.Port)
}

// poolNodeWhere is where the traffic leaves from, and by which protocol.
func (m Model) poolNodeWhere(n PoolNode) string {
	where := n.ExitIP
	if n.Geo != "" {
		if where != "" {
			where = n.Geo + " " + where
		} else {
			where = n.Geo
		}
	}
	if where == "" {
		where = n.Host + ":" + itoa(n.Port)
	}
	return where + " · " + n.Proto
}

// untilPhrase says how far away a moment is, in the words the frame uses.
func untilPhrase(t time.Time) string {
	d := time.Until(t)
	if d <= 0 {
		return "due now"
	}
	if d < time.Minute {
		return "in " + d.Round(time.Second).String()
	}
	return "in " + d.Round(time.Minute).String()
}
