package pool

import (
	"testing"
	"time"
)

func node(id string, rtt time.Duration, exit string) Node {
	return Node{ID: id, Label: id, Proto: "vless", Host: "h", Port: 443, ProbeRTT: rtt, ExitIP: exit, Raw: "vless://" + id}
}

func TestRegistryRanksAndCaps(t *testing.T) {
	r := NewRegistry(2, 2)
	r.Replace([]Node{
		node("slow", 400*time.Millisecond, "1.1.1.1"),
		node("fast", 90*time.Millisecond, "2.2.2.2"),
		node("mid", 200*time.Millisecond, "3.3.3.3"),
	})
	snap := r.Snapshot()
	if snap.Relevant != 2 {
		t.Fatalf("relevant %d", snap.Relevant)
	}
	if snap.Nodes[0].ID != "fast" || snap.Nodes[1].ID != "mid" {
		t.Fatalf("order %s %s", snap.Nodes[0].ID, snap.Nodes[1].ID)
	}
}

// Two servers behind one address share the provider's budget for that address, so
// only the faster one is worth a place in the list.
func TestRegistryDeduplicatesByExitAddress(t *testing.T) {
	r := NewRegistry(10, 2)
	r.Replace([]Node{
		node("a", 300*time.Millisecond, "9.9.9.9"),
		node("b", 100*time.Millisecond, "9.9.9.9"),
		node("c", 150*time.Millisecond, "8.8.8.8"),
	})
	snap := r.Snapshot()
	if snap.Relevant != 2 {
		t.Fatalf("relevant %d", snap.Relevant)
	}
	if snap.Nodes[0].ID != "b" {
		t.Fatalf("the faster server behind the address must win: %s", snap.Nodes[0].ID)
	}
}

func TestRegistryRotates(t *testing.T) {
	r := NewRegistry(10, 2)
	r.Replace([]Node{
		node("a", 100*time.Millisecond, "1.1.1.1"),
		node("b", 200*time.Millisecond, "2.2.2.2"),
	})
	for _, want := range []string{"a", "b", "a"} {
		got, ok := r.Next()
		if !ok || got.ID != want {
			t.Fatalf("want %s, got %s (%v)", want, got.ID, ok)
		}
	}
}

func TestRegistryQuarantinesAfterThreshold(t *testing.T) {
	r := NewRegistry(10, 2)
	r.Replace([]Node{
		node("a", 100*time.Millisecond, "1.1.1.1"),
		node("b", 200*time.Millisecond, "2.2.2.2"),
	})
	r.Next()
	r.Suspect("a", "silence")
	if snap := r.Snapshot(); len(snap.Quarantined) != 0 {
		t.Fatalf("quarantined on the first failure: %+v", snap.Quarantined)
	}
	r.Suspect("a", "silence")
	snap := r.Snapshot()
	if len(snap.Quarantined) != 1 || snap.Quarantined[0].Node.ID != "a" {
		t.Fatalf("quarantine %+v", snap.Quarantined)
	}
	got, ok := r.Next()
	if !ok || got.ID != "b" {
		t.Fatalf("the quarantined node must not be picked: %s", got.ID)
	}
	// A success clears the counter, but only an evaluation clears the quarantine.
	r.Suspect("b", "silence")
	r.Succeed("b")
	r.Suspect("b", "silence")
	if snap := r.Snapshot(); len(snap.Quarantined) != 1 {
		t.Fatalf("a success must reset the counter: %+v", snap.Quarantined)
	}
}

func TestRegistryKeepsTheServingNodeAcrossEvaluations(t *testing.T) {
	r := NewRegistry(10, 2)
	r.Replace([]Node{
		node("a", 100*time.Millisecond, "1.1.1.1"),
		node("b", 200*time.Millisecond, "2.2.2.2"),
	})
	r.Next()
	r.Replace([]Node{
		node("a", 120*time.Millisecond, "1.1.1.1"),
		node("c", 90*time.Millisecond, "3.3.3.3"),
	})
	if active, ok := r.Active(); !ok || active.ID != "a" {
		t.Fatalf("a working node must keep serving: %+v", active)
	}
	// When it disappears, the next pick comes from the new list.
	r.Replace([]Node{node("c", 90*time.Millisecond, "3.3.3.3")})
	if _, ok := r.Active(); ok {
		t.Fatal("a node that left the list cannot stay active")
	}
	if got, ok := r.Next(); !ok || got.ID != "c" {
		t.Fatalf("next %+v", got)
	}
}

func TestRegistryEmptyWhenEverythingIsQuarantined(t *testing.T) {
	r := NewRegistry(10, 1)
	r.Replace([]Node{node("a", 100*time.Millisecond, "1.1.1.1")})
	r.Suspect("a", "silence")
	if !r.Empty() {
		t.Fatal("an all-quarantined list is empty")
	}
	if _, ok := r.Next(); ok {
		t.Fatal("no node may be picked from an empty list")
	}
}
