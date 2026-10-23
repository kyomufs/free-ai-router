package pool

import (
	"encoding/json"
	"fmt"
	"strings"
)

// mihomoProxyName is the only name in a rendered config: one node, one rule.
const mihomoProxyName = "node"

type mihomo struct{}

// NewMihomo is the default core. It is the only one of the three candidates that
// renders every scheme these subscriptions carry, xhttp included; see the design
// document for the measured comparison.
func NewMihomo() Core { return mihomo{} }

func (mihomo) ID() string { return "mihomo" }

func (mihomo) Supports(n Node) bool {
	switch n.Proto {
	case "vless", "vmess", "ss", "trojan", "hysteria2", "tuic":
		return true
	}
	return false
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

func mihomoProxy(n Node) (map[string]any, error) {
	p := map[string]any{
		"name":   mihomoProxyName,
		"type":   n.Proto,
		"server": n.Host,
		"port":   n.Port,
	}
	insecure := truthy(n.params["insecure"]) || truthy(n.params["allowInsecure"]) || truthy(n.params["skip-cert-verify"])

	switch n.Proto {
	case "vless":
		uuid := n.params["secret"]
		if uuid == "" {
			return nil, fmt.Errorf("vless: no uuid")
		}
		p["uuid"] = uuid
		applyTLS(p, n, insecure)
	case "vmess":
		uuid := n.params["secret"]
		if uuid == "" {
			return nil, fmt.Errorf("vmess: no uuid")
		}
		p["uuid"] = uuid
		p["alterId"] = 0
		p["cipher"] = "auto"
		applyTLS(p, n, insecure)
	case "trojan":
		if n.params["secret"] == "" {
			return nil, fmt.Errorf("trojan: no password")
		}
		p["password"] = n.params["secret"]
		if n.SNI != "" {
			p["sni"] = n.SNI
		}
		if insecure {
			p["skip-cert-verify"] = true
		}
	case "ss":
		method := n.params["method"]
		if method == "" {
			return nil, fmt.Errorf("ss: no cipher")
		}
		p["cipher"] = method
		p["password"] = n.params["secret"]
	case "hysteria2":
		if n.params["secret"] == "" {
			return nil, fmt.Errorf("hysteria2: no password")
		}
		p["password"] = n.params["secret"]
		if n.SNI != "" {
			p["sni"] = n.SNI
		}
		if insecure {
			p["skip-cert-verify"] = true
		}
	case "tuic":
		p["token"] = n.params["secret"]
		if n.SNI != "" {
			p["sni"] = n.SNI
		}
		if insecure {
			p["skip-cert-verify"] = true
		}
	default:
		return nil, fmt.Errorf("unsupported protocol %q", n.Proto)
	}

	if n.Transport != "" {
		p["network"] = n.Transport
		applyNetworkOpts(p, n)
	}
	return p, nil
}

// applyTLS is shared by vless and vmess because both spell the fields the same
// way. Note that trojan and hysteria2 put the server name under "sni" instead of
// "servername"; getting that wrong silently disables the handshake's hostname.
func applyTLS(p map[string]any, n Node, insecure bool) {
	if n.Security == "tls" || n.Security == "reality" {
		p["tls"] = true
		if n.SNI != "" {
			p["servername"] = n.SNI
		}
		fp := n.params["fp"]
		if fp == "" {
			fp = "chrome"
		}
		p["client-fingerprint"] = fp
	}
	if n.Security == "reality" {
		opts := map[string]any{}
		if pbk := n.params["pbk"]; pbk != "" {
			opts["public-key"] = pbk
		}
		if sid := n.params["sid"]; sid != "" {
			opts["short-id"] = sid
		}
		if len(opts) > 0 {
			p["reality-opts"] = opts
		}
	}
	if insecure {
		p["skip-cert-verify"] = true
	}
}

func applyNetworkOpts(p map[string]any, n Node) {
	path := n.params["path"]
	host := n.params["host"]
	if host == "" {
		host = n.SNI
	}
	switch n.Transport {
	case "ws", "httpupgrade":
		opts := map[string]any{}
		if path != "" {
			opts["path"] = path
		}
		if host != "" {
			opts["headers"] = map[string]any{"Host": host}
		}
		if len(opts) > 0 {
			p["ws-opts"] = opts
		}
	case "grpc":
		name := n.params["serviceName"]
		if name == "" {
			name = strings.TrimPrefix(path, "/")
		}
		if name != "" {
			p["grpc-opts"] = map[string]any{"grpc-service-name": name}
		}
	case "http":
		opts := map[string]any{}
		if path != "" {
			opts["path"] = []any{path}
		}
		if host != "" {
			opts["headers"] = map[string]any{"Host": []any{host}}
		}
		if len(opts) > 0 {
			p["http-opts"] = opts
		}
	case "xhttp":
		if path != "" {
			p["xhttp-opts"] = map[string]any{"path": path}
		}
	}
}

func truthy(s string) bool {
	switch strings.ToLower(s) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
