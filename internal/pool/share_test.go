package pool

import (
	"strings"
	"testing"
)

func TestParseLinkVLESSGRPC(t *testing.T) {
	raw := "vless://8dc7722c-2767-4eea-a28b-2f8daacc07e3@sca12.myfymain.com:8880?mode=gun&security=none&encryption=none&type=grpc#🇮🇹"
	n, err := ParseLink(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if n.Proto != "vless" || n.Host != "sca12.myfymain.com" || n.Port != 8880 {
		t.Fatalf("got %+v", n)
	}
	if n.Transport != "grpc" || n.Security != "none" {
		t.Fatalf("transport %q security %q", n.Transport, n.Security)
	}
	if n.Label != "🇮🇹" || n.ExitGeo != "🇮🇹" {
		t.Fatalf("label %q geo %q", n.Label, n.ExitGeo)
	}
	if n.Raw != raw {
		t.Fatal("raw link must be kept verbatim")
	}
}

func TestParseLinkVariants(t *testing.T) {
	cases := []struct {
		name, raw string
		proto     string
		transport string
		security  string
		host      string
		port      int
	}{
		{"ws tls", "vless://c35050ce-01ab-45a9-91b8-54cec637d0a8@172.64.33.162:443?path=%2Fudfyfws&security=tls&encryption=none&insecure=0&host=octopusss5.info&type=ws&allowInsecure=0&sni=octopusss5.info#🇫🇷",
			"vless", "ws", "tls", "172.64.33.162", 443},
		{"reality", "vless://7e544a9d-1111-2222-3333-444455556666@104.16.72.101:443?security=reality&type=ws&sni=shegeftihaaa.net&pbk=jNXHt1yRo0vDuchQlIP6Z0ZvjT3KtzVI-T4E7RoLJS0&fp=chrome#Tel",
			"vless", "ws", "reality", "104.16.72.101", 443},
		{"tcp is no transport", "vless://1de37726-744b-46e1-9101-c926b3402e82@104.17.72.206:80?type=tcp&host=v2.workers.dev#🇨🇦",
			"vless", "", "none", "104.17.72.206", 80},
		{"raw is no transport", "vless://1de37726-744b-46e1-9101-c926b3402e82@104.17.72.206:80?type=raw&security=tls#🇨🇦",
			"vless", "", "tls", "104.17.72.206", 80},
		{"xhttp", "vless://1de37726-744b-46e1-9101-c926b3402e82@h.example:443?type=xhttp&security=tls&path=%2Fx#x",
			"vless", "xhttp", "tls", "h.example", 443},
		{"trojan", "trojan://secret@t.example:443?security=tls&sni=t.example#🇩🇪",
			"trojan", "", "tls", "t.example", 443},
		{"hysteria2", "hysteria2://pw@hy.example:8443?sni=hy.example#hy",
			"hysteria2", "", "none", "hy.example", 8443},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n, err := ParseLink(c.raw)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if n.Proto != c.proto || n.Transport != c.transport || n.Security != c.security {
				t.Fatalf("got proto %q transport %q security %q", n.Proto, n.Transport, n.Security)
			}
			if n.Host != c.host || n.Port != c.port {
				t.Fatalf("got %s:%d", n.Host, n.Port)
			}
		})
	}
}

func TestParseLinkVmessAndSS(t *testing.T) {
	vmess := "vmess://eyJhZGQiOiJqcDQuOTk4OTk4LmJlc3QiLCJhaWQiOjAsImhvc3QiOiJuYXQxMi45OTY2MzM4Lnh5eiIsImlkIjoiNTUwNGVhYTgtMTM5NS00MDJmLThlMGUtNzVlY2Q3NjJlNjI3IiwibmV0Ijoid3MiLCJwYXRoIjoiL3Z2dnZ2dnZ2diIsInBvcnQiOiI0NDMiLCJwcyI6IkpQIiwidGxzIjoidGxzIn0="
	n, err := ParseLink(vmess)
	if err != nil {
		t.Fatalf("vmess: %v", err)
	}
	if n.Proto != "vmess" || n.Host != "jp4.998998.best" || n.Port != 443 || n.Transport != "ws" || n.Security != "tls" {
		t.Fatalf("got %+v", n)
	}

	ss := "ss://YWVzLTI1Ni1nY206cGFzcw==@ss.example:8388#SS"
	n, err = ParseLink(ss)
	if err != nil {
		t.Fatalf("ss: %v", err)
	}
	if n.Proto != "ss" || n.Host != "ss.example" || n.Port != 8388 || n.params["method"] != "aes-256-gcm" {
		t.Fatalf("got %+v params %v", n, n.params)
	}
}

func TestParseLinkIPv6Literal(t *testing.T) {
	n, err := ParseLink("vless://u@[2001:db8::1]:443?security=tls&type=ws#x")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if n.Host != "2001:db8::1" || n.Port != 443 {
		t.Fatalf("got %+v", n)
	}
	if got := dialAddress(n); got != "[2001:db8::1]:443" {
		t.Fatalf("dial address %q", got)
	}
}

func TestParseLinkRejectsGarbage(t *testing.T) {
	for _, raw := range []string{"", "http://example.com/x", "vless://", "vless://u@host:notaport"} {
		if _, err := ParseLink(raw); err == nil {
			t.Fatalf("expected an error for %q", raw)
		}
	}
}

func TestParseListCountsSkipped(t *testing.T) {
	body := strings.Join([]string{
		"vless://a@one.example:443?type=ws#one",
		"",
		"<html>not a link</html>",
		"vless://b@two.example:443?type=ws#two",
	}, "\n")
	nodes, skipped, err := ParseList(strings.NewReader(body))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(nodes) != 2 || len(skipped) != 1 {
		t.Fatalf("nodes %d skipped %d", len(nodes), len(skipped))
	}
}

func TestNodeKeyIgnoresLabel(t *testing.T) {
	a, err := ParseLink("vless://a@one.example:443?type=ws&path=%2Fx#🇮🇹")
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseLink("vless://a@one.example:443?type=ws&path=%2Fx#🇫🇷")
	if err != nil {
		t.Fatal(err)
	}
	if NodeKey(a) != NodeKey(b) {
		t.Fatalf("labels changed the key: %s vs %s", NodeKey(a), NodeKey(b))
	}
	c, err := ParseLink("vless://a@one.example:443?type=ws&path=%2Fy#🇮🇹")
	if err != nil {
		t.Fatal(err)
	}
	if NodeKey(a) == NodeKey(c) {
		t.Fatal("a different path must be a different node")
	}
}
