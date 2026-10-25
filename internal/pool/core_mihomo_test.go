package pool

import (
	"strings"
	"testing"
)

// TestConfigRendersThePassProfile: the config carries the pass's own profile, name
// forced to the single rule's target, and nothing re-derived from parsed fields.
func TestConfigRendersThePassProfile(t *testing.T) {
	n := Node{
		Label: "de-1",
		Proto: "vless",
		Host:  "de.example",
		Port:  8443,
		Raw: `{"name":"de-1","type":"vless","server":"de.example","port":8443,` +
			`"uuid":"u","tls":true,"network":"xhttp","xhttp-opts":{"path":"/x"},` +
			`"flow":"xtls-rprx-vision","packet-encoding":"xudp"}`,
	}
	cfg, err := NewMihomo().Config(n, 12345)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	text := string(cfg)
	for _, want := range []string{
		`"name": "node"`,
		`"mixed-port": 12345`,
		`"network": "xhttp"`,
		`"xhttp-opts"`,
		`"flow": "xtls-rprx-vision"`,
		`"packet-encoding": "xudp"`,
		`"MATCH,node"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("%s is missing from the core config: %s", want, text)
		}
	}
}

// TestConfigRefusesAProfileTheCoreCannotRender: no profile means no core, and the
// error names the node rather than an empty config.
func TestConfigRefusesAProfileTheCoreCannotRender(t *testing.T) {
	n := Node{Label: "broken", Proto: "vless", Host: "h", Port: 1, Raw: "not json"}
	if _, err := NewMihomo().Config(n, 12345); err == nil {
		t.Fatal("a node without a core profile must not render")
	} else if !strings.Contains(err.Error(), "broken") {
		t.Fatalf("err %v", err)
	}
}
