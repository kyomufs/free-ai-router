package pool

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStateRoundTripKeepsTheLink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pool.json")
	n := Node{
		ID:       "a5f0",
		Proto:    "vless",
		Host:     "h.example",
		Port:     443,
		SNI:      "h.example",
		Label:    "DE",
		Raw:      `{"name":"DE","type":"vless","server":"h.example","port":443,"uuid":"11111111-2222-3333-4444-555555555555","network":"ws","tls":true,"servername":"h.example","ws-opts":{"path":"/ws"}}`,
		ProbeRTT: 120 * time.Millisecond,
		ExitIP:   "203.0.113.9",
	}

	at := time.Date(2026, 10, 24, 3, 0, 0, 0, time.UTC)
	state := State{
		Version:     StateVersion,
		EvaluatedAt: at,
		NextEvalAt:  at.Add(8 * time.Hour),
		EvalState:   "ready",
		Nodes:       []Node{n},
		Quarantined: []Entry{{Node: n, Standing: StandingQuarantined, Reason: "silence"}},
	}
	if err := Save(path, state); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Nodes) != 1 {
		t.Fatalf("nodes %d", len(got.Nodes))
	}
	if got.Nodes[0].ID != n.ID || got.Nodes[0].ProbeRTT != n.ProbeRTT || got.Nodes[0].ExitIP != n.ExitIP {
		t.Fatalf("node %+v", got.Nodes[0])
	}
	// The profile is stored verbatim: the renderer hands it to the core as it is.
	if got.Nodes[0].Raw != n.Raw || got.Nodes[0].SNI != "h.example" {
		t.Fatalf("profile lost: %+v", got.Nodes[0])
	}
	if len(got.Quarantined) != 1 || got.Quarantined[0].Node.ID != n.ID {
		t.Fatalf("quarantine %+v", got.Quarantined)
	}

	// The file holds every share link the user owns, UUIDs included: a world-readable
	// pool.json would hand the subscription to every local account.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode %04o, want 0600", mode)
	}
}

// A cache that cannot be read must not stop the daemon: the list is regenerable.
func TestLoadIgnoresATruncatedOrForeignFile(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"truncated.json": "{\"version\":1,\"nodes\":[{\"id\":\"a\"",
		"foreign.json":   "{\"version\":99,\"nodes\":[]}",
		"empty.json":     "",
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		state, err := Load(path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(state.Nodes) != 0 || state.Version != StateVersion {
			t.Fatalf("%s: %+v", name, state)
		}
	}
	if _, err := Load(filepath.Join(dir, "absent.json")); err != nil {
		t.Fatalf("a missing file is not an error: %v", err)
	}
}

func TestNextEvaluationFollowsTheThreeSlots(t *testing.T) {
	first := time.Date(2026, 10, 24, 3, 0, 0, 0, time.UTC)
	cases := []struct {
		now  time.Time
		want time.Time
	}{
		{time.Date(2026, 10, 24, 0, 30, 0, 0, time.UTC), first},
		{time.Date(2026, 10, 24, 3, 0, 0, 0, time.UTC), first.Add(8 * time.Hour)},
		{time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC), first.Add(8 * time.Hour)},
		{time.Date(2026, 10, 24, 11, 0, 1, 0, time.UTC), first.Add(16 * time.Hour)},
		{time.Date(2026, 10, 24, 19, 30, 0, 0, time.UTC), first.Add(24 * time.Hour)},
		{time.Date(2026, 11, 5, 23, 59, 0, 0, time.UTC), time.Date(2026, 11, 6, 3, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		if got := NextEvaluation(c.now, first, 8*time.Hour); !got.Equal(c.want) {
			t.Fatalf("from %s want %s got %s", c.now, c.want, got)
		}
	}
}
