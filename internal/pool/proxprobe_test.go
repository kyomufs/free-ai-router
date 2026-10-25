package pool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writePass lays down the two artifacts a finished pass leaves behind.
func writePass(t *testing.T, dir string, merged, http string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "merged.jsonl"), []byte(merged), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "http.jsonl"), []byte(http), 0o644); err != nil {
		t.Fatal(err)
	}
}

const passMerged = `{"id":"n1","raw":{"clash":{"name":"nl-1","type":"vless","server":"nl.example","port":443,"uuid":"u1","tls":true,"servername":"nl.example","network":"tcp"}}}
{"id":"n2","raw":{"clash":{"name":"de-1","type":"vless","server":"de.example","port":8443,"uuid":"u2","tls":true,"servername":"de.example","client-fingerprint":"chrome","packet-encoding":"xudp","reality-opts":{"public-key":"k","short-id":"s"}}}}
{"id":"n3","raw":{"xray":{}}}
`

const passHTTP = `{"id":"n1","ok":true,"delay_ms":300}
{"id":"n2","ok":true,"delay_ms":120}
{"id":"n3","ok":true,"delay_ms":50}
`

// TestLoadPassTakesOnlyTheHTTPOKNodes: the pass's http stage is the gate, and the
// order the probe spends its budget in is the pass's own measured delay.
func TestLoadPassTakesOnlyTheHTTPOKNodes(t *testing.T) {
	dir := t.TempDir()
	writePass(t, dir, passMerged, passHTTP)

	pass, err := LoadPass(dir, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if pass.Counts.Parsed != 3 || pass.Counts.HTTPOK != 3 || pass.Counts.Unsupported != 1 {
		t.Fatalf("counts %+v", pass.Counts)
	}
	if len(pass.Nodes) != 2 {
		t.Fatalf("nodes %+v", pass.Nodes)
	}
	// Cheapest first: the probe spends its budget in the pass's own order.
	if pass.Nodes[0].ID != "n2" || pass.Nodes[1].ID != "n1" {
		t.Fatalf("order %s, %s", pass.Nodes[0].ID, pass.Nodes[1].ID)
	}
	if pass.Nodes[0].ProbeRTT != 120*time.Millisecond || pass.Nodes[1].ProbeRTT != 300*time.Millisecond {
		t.Fatalf("delays %v %v", pass.Nodes[0].ProbeRTT, pass.Nodes[1].ProbeRTT)
	}

	// The reality node is the one with the interesting profile.
	n := pass.Nodes[0]
	if n.Label != "de-1" || n.SNI != "de.example" || n.Security != "reality" || n.Port != 8443 {
		t.Fatalf("node %+v", n)
	}
	if pass.Nodes[1].Transport != "tcp" {
		t.Fatalf("transport %q", pass.Nodes[1].Transport)
	}
	if !strings.Contains(n.Raw, `"packet-encoding":"xudp"`) {
		t.Fatalf("the profile was not kept verbatim: %s", n.Raw)
	}
}

// TestLoadPassRefusesAStalePass: a pass from the previous slot describes servers
// that were measured before the last rotation.
func TestLoadPassRefusesAStalePass(t *testing.T) {
	dir := t.TempDir()
	writePass(t, dir, passMerged, passHTTP)
	old := time.Now().Add(-3 * time.Hour)
	for _, name := range []string{"merged.jsonl", "http.jsonl"} {
		if err := os.Chtimes(filepath.Join(dir, name), old, old); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := LoadPass(dir, time.Hour, time.Now()); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("a stale pass must be refused: %v", err)
	}
}

// TestLoadPassWithoutArtifacts: an unrun pass is a readable error, not an empty list
// that looks like a pass which found nothing.
func TestLoadPassWithoutArtifacts(t *testing.T) {
	_, err := LoadPass(t.TempDir(), time.Hour, time.Now())
	if err == nil || !strings.Contains(err.Error(), "merged.jsonl") {
		t.Fatalf("err %v", err)
	}
}

// TestThePassProfileIsRenderedAsItIs: the pass already speaks the core's dialect, and
// the long tail (xhttp, packet-encoding) survives only when it is not re-derived.
func TestThePassProfileIsRenderedAsItIs(t *testing.T) {
	dir := t.TempDir()
	writePass(t, dir, passMerged, passHTTP)
	pass, err := LoadPass(dir, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := NewMihomo().Config(pass.Nodes[0], 17890)
	if err != nil {
		t.Fatal(err)
	}
	var rendered struct {
		Proxies []map[string]any `json:"proxies"`
	}
	if err := json.Unmarshal(cfg, &rendered); err != nil {
		t.Fatal(err)
	}
	proxy := rendered.Proxies[0]
	if proxy["name"] != mihomoProxyName {
		t.Fatalf("the profile must be renamed for the single-node config: %v", proxy["name"])
	}
	if proxy["packet-encoding"] != "xudp" {
		t.Fatalf("the profile was re-derived instead of kept: %v", proxy)
	}
	if _, ok := proxy["reality-opts"]; !ok {
		t.Fatalf("reality options lost: %v", proxy)
	}
}

// TestNodeFromClashRefusesWhatTheCoreCannotRender: the pass may hand over a protocol
// the core has no branch for, and such a node must be counted, not installed.
func TestNodeFromClashRefusesWhatTheCoreCannotRender(t *testing.T) {
	if _, err := nodeFromClash("x", json.RawMessage(`{"type":"wireguard","server":"h","port":1}`)); err == nil {
		t.Fatal("an unrenderable protocol must be refused")
	}
	if _, err := nodeFromClash("x", json.RawMessage(`{}`)); err == nil {
		t.Fatal("an empty profile must be refused")
	}
}

// TestLoadPassTakesTheLastVerdict: a node the pass probed twice is in the list only
// if the later attempt succeeded.
func TestLoadPassTakesTheLastVerdict(t *testing.T) {
	dir := t.TempDir()
	writePass(t, dir, passMerged, `{"id":"n2","ok":true,"delay_ms":90}
{"id":"n2","ok":false,"error":"EOF"}
{"id":"n1","ok":true,"delay_ms":10}
`)
	pass, err := LoadPass(dir, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if pass.Counts.HTTPOK != 1 || len(pass.Nodes) != 1 || pass.Nodes[0].ID != "n1" {
		t.Fatalf("counts %+v nodes %+v", pass.Counts, pass.Nodes)
	}
}
