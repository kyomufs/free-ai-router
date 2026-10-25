package tui

import (
	"strings"
	"testing"
	"time"
)

// poolObservation is an observation of a deployment behind the pool transport.
func poolObservation() Observation {
	obs := observation()
	at := obs.At
	obs.Pool = &PoolState{
		Relevant: 3,
		Nodes: []PoolNode{
			{Label: "de-1", Host: "de.example", Port: 443, Proto: "vless", RTT: 180 * time.Millisecond, ExitIP: "203.0.113.7", Geo: "DE"},
			{Label: "fr-1", Host: "fr.example", Port: 443, Proto: "trojan", RTT: 240 * time.Millisecond, ExitIP: "203.0.113.8", Geo: "FR"},
			{Label: "nl-1", Host: "nl.example", Port: 8443, Proto: "vmess", RTT: 320 * time.Millisecond, ExitIP: "203.0.113.9", Geo: "NL"},
		},
		Active:      PoolNode{Label: "de-1", Host: "de.example", Port: 443, Proto: "vless", RTT: 180 * time.Millisecond, ExitIP: "203.0.113.7", Geo: "DE"},
		Quarantined: 2,
		EvalState:   "ready",
		EvaluatedAt: at.Add(-3 * time.Hour),
		NextEvalAt:  at.Add(5 * time.Hour),
	}
	return obs
}

// TestThePoolTileSaysWhichServerIsServing is the tile's reason to exist: a proxy
// that answers slowly is a question about the server in use, and the answer has
// to be on the dashboard.
func TestThePoolTileSaysWhichServerIsServing(t *testing.T) {
	got := draw(monitor(poolObservation()))

	if !strings.Contains(got, "pool") {
		t.Fatalf("the pool tile is missing:\n%s", got)
	}
	if !strings.Contains(got, "de-1") {
		t.Fatalf("the serving server is not named:\n%s", got)
	}
	if !strings.Contains(got, "3 servers") || !strings.Contains(got, "2 quarantined") {
		t.Fatalf("the list's size is not reported:\n%s", got)
	}
	if !strings.Contains(got, "203.0.113.7") {
		t.Fatalf("the address the provider sees is not shown:\n%s", got)
	}
}

// TestThePoolTileIsAbsentInDirectMode: a tile describing a transport that is not
// in use would be an empty frame asking to be explained.
func TestThePoolTileIsAbsentInDirectMode(t *testing.T) {
	got := draw(monitor(observation()))
	if strings.Contains(got, "next check") {
		t.Fatalf("the pool tile is drawn without a pool:\n%s", got)
	}
}

// TestThePoolTileReportsAnEmptyList: the empty pool is the state that answers 503,
// and the frame has to say so instead of showing a healthy blank.
func TestThePoolTileReportsAnEmptyList(t *testing.T) {
	obs := poolObservation()
	obs.Pool.Nodes = nil
	obs.Pool.Relevant = 0
	obs.Pool.Active = PoolNode{}
	obs.Pool.EvalState = "error"
	obs.Pool.Error = "no subscription answered"

	got := draw(monitor(obs))
	if !strings.Contains(got, "no subscription answered") {
		t.Fatalf("the failure reason is not shown:\n%s", got)
	}
	if !strings.Contains(got, "nothing serving") {
		t.Fatalf("the empty state is not named:\n%s", got)
	}
}
