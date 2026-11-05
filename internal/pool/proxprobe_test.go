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
//
// The second artifact is the TCP stage, not the http one: pull mode ranks by the
// pass's dial measurement and never consults the http stage, because that stage
// spends one provider request per node.
//
// The profile goes where the pass writes it — clash/merged.yaml — because that is
// the file the loader reads: a run without the probe step leaves merged.jsonl
// carrying only the share link, and the Clash profile is built by the export step.
func writePass(t *testing.T, dir string, profile, tcp string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "clash"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "probe"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "clash", "merged.yaml"), []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "probe", "tcp.jsonl"), []byte(tcp), 0o644); err != nil {
		t.Fatal(err)
	}
}

// passProfile is the exported Clash profile, in the shape proxprobe's exporter
// writes: a `proxies:` list of flat mappings, a nested object for the transport, and
// a sequence value (`alpn`) that is not a proxy of its own.
const passProfile = `mixed-port: 7890
proxies:
  - name: "nl-1"
    type: vless
    server: nl.example
    port: 443
    uuid: u1
    tls: true
    servername: nl.example
    network: tcp
  - alpn:
      - h2
      - http/1.1
    client-fingerprint: chrome
    name: "de-1"
    network: grpc
    grpc-opts:
      grpc-service-name: svc
    port: 8443
    reality-opts:
      public-key: k
      short-id: s
    server: de.example
    servername: de.example
    tls: true
    type: vless
    uuid: u2
  - name: "gone-1"
    type: vless
    server: gone.example
    port: 1
    uuid: u3
proxy-groups:
  - name: PROXY
`

// passTCP carries a verdict for two of the profile's three entries. The third
// (gone.example) has none: that is what the no-tcp count reports, and its absence is
// why the address must not appear here at all.
const passTCP = `{"id":"x","server":"nl.example","port":443,"ok":true,"dial_ms":300,"resolved_ip":"203.0.113.10"}
{"id":"y","server":"de.example","port":8443,"ok":true,"dial_ms":120,"resolved_ip":"198.51.100.20"}
`

// TestLoadPassTakesOnlyTheTCPOKNodes: the pass's TCP stage is the gate, and the
// order the probe spends its budget in is the pass's own measured dial time.
//
// The http stage is deliberately NOT consulted: pull mode ranks by TCP and lets the
// provider speak through the first real request, because the http stage costs one
// provider request per node.
func TestLoadPassTakesOnlyTheTCPOKNodes(t *testing.T) {
	dir := t.TempDir()
	writePass(t, dir, passProfile, passTCP)

	pass, err := LoadPass(dir, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if pass.Counts.Parsed != 3 || pass.Counts.TCPOK != 2 || pass.Counts.NoTCP != 1 {
		t.Fatalf("counts %+v", pass.Counts)
	}
	if len(pass.Nodes) != 2 {
		t.Fatalf("nodes %+v", pass.Nodes)
	}
	// Cheapest first: the probe spends its budget in the pass's own order.
	if pass.Nodes[0].Host != "de.example" || pass.Nodes[1].Host != "nl.example" {
		t.Fatalf("order %s, %s", pass.Nodes[0].Host, pass.Nodes[1].Host)
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
	// The nested objects the exporter writes survive the reader as the profile wrote
	// them: the core parses them itself.
	if !strings.Contains(n.Raw, `"grpc-service-name":"svc"`) {
		t.Fatalf("the nested transport object was lost: %s", n.Raw)
	}
	if !strings.Contains(n.Raw, `"short-id":"s"`) {
		t.Fatalf("the nested reality object was lost: %s", n.Raw)
	}
}

// TestLoadPassRefusesAStalePass: a pass from the previous slot describes servers
// that were measured before the last rotation.
func TestLoadPassRefusesAStalePass(t *testing.T) {
	dir := t.TempDir()
	writePass(t, dir, passProfile, passTCP)
	old := time.Now().Add(-3 * time.Hour)
	for _, name := range []string{filepath.Join("clash", "merged.yaml"), filepath.Join("probe", "tcp.jsonl")} {
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
	if err == nil || !strings.Contains(err.Error(), "Clash profile") {
		t.Fatalf("err %v", err)
	}
}

// TestThePassProfileIsRenderedAsItIs: the pass already speaks the core's dialect, and
// the long tail (xhttp, packet-encoding) survives only when it is not re-derived.
func TestThePassProfileIsRenderedAsItIs(t *testing.T) {
	dir := t.TempDir()
	writePass(t, dir, passProfile, passTCP)
	pass, err := LoadPass(dir, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	// The reality node, not the first of the list: the list is ranked by dial time and
	// the fixture's cheapest entry is the one with nothing interesting in it.
	var node Node
	for _, n := range pass.Nodes {
		if n.Label == "de-1" {
			node = n
		}
	}
	if node.Host == "" {
		t.Fatalf("de-1 is not in the list: %+v", pass.Nodes)
	}

	cfg, err := NewMihomo().Config(node, 17890)
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
	if proxy["client-fingerprint"] != "chrome" {
		t.Fatalf("the profile was re-derived instead of kept: %v", proxy)
	}
	if _, ok := proxy["reality-opts"]; !ok {
		t.Fatalf("reality options lost: %v", proxy)
	}
	if _, ok := proxy["grpc-opts"]; !ok {
		t.Fatalf("the nested transport object lost: %v", proxy)
	}
	// A sequence value inside a proxy is a value, not a proxy of its own: mistaking it
	// for one produced a hundred empty profiles on a real subscription.
	if items, ok := proxy["alpn"].([]any); !ok || len(items) != 2 {
		t.Fatalf("the sequence value was mishandled: %v", proxy["alpn"])
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

// TestLoadPassTakesTheLastVerdict: an address the pass dialled twice keeps its
// verdict only when the later attempt succeeded.
//
// The key is the address pair, so the two records are two verdicts about one
// candidate rather than two candidates.
func TestLoadPassTakesTheLastVerdict(t *testing.T) {
	dir := t.TempDir()
	writePass(t, dir, passProfile, `{"id":"a","server":"de.example","port":8443,"ok":true,"dial_ms":90}
{"id":"b","server":"de.example","port":8443,"ok":false,"error":"EOF"}
{"id":"c","server":"nl.example","port":443,"ok":true,"dial_ms":10,"resolved_ip":"203.0.113.10"}
`)
	pass, err := LoadPass(dir, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// de.example was withdrawn by its second verdict; only nl.example is left.
	if pass.Counts.TCPOK != 1 || len(pass.Nodes) != 1 {
		t.Fatalf("counts %+v nodes %+v", pass.Counts, pass.Nodes)
	}
	if pass.Nodes[0].Host != "nl.example" {
		t.Fatalf("wrong survivor: %+v", pass.Nodes[0])
	}
}
