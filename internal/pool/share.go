package pool

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Node is one server from a subscription.
//
// Raw is kept alongside the parsed fields on purpose: rendering a core config is
// the core's business, and a lossy intermediate representation would eventually
// lose a field the core needs. The parsed fields exist for ranking, dialling and
// display only.
type Node struct {
	ID        string        `json:"id"`
	Proto     string        `json:"proto"`
	Transport string        `json:"transport,omitempty"`
	Security  string        `json:"security,omitempty"`
	Host      string        `json:"host"`
	Port      int           `json:"port"`
	SNI       string        `json:"sni,omitempty"`
	Label     string        `json:"label,omitempty"`
	Raw       string        `json:"raw"`
	TCPRTT    time.Duration `json:"tcp_rtt,omitempty"`
	ProbeRTT  time.Duration `json:"probe_rtt,omitempty"`
	ExitIP    string        `json:"exit_ip,omitempty"`
	ExitGeo   string        `json:"exit_geo,omitempty"`

	// params carries the link's query string and credentials for the renderer.
	// Unexported because it is derived: it is rebuilt from Raw on load, so a
	// stored pool never depends on it.
	params map[string]string
}

// ParseLink turns one share link into a node.
//
// The query string is parsed by hand rather than through url.Values where the
// shape does not survive it: vmess carries base64 JSON, ss carries base64
// credentials, and both use '#' for a label that is frequently an unescaped
// emoji.
func ParseLink(raw string) (Node, error) {
	line := strings.TrimSpace(raw)
	i := strings.Index(line, "://")
	if i <= 0 {
		return Node{}, fmt.Errorf("not a share link: %.40q", line)
	}
	scheme := strings.ToLower(line[:i])
	payload := line[i+3:]

	switch scheme {
	case "vless", "trojan", "hysteria2", "tuic":
		return parseURI(scheme, line, payload)
	case "vmess":
		return parseVmess(line, payload)
	case "ss":
		return parseShadowSocks(line, payload)
	}
	return Node{}, fmt.Errorf("unsupported scheme %q", scheme)
}

// ParseList reads a subscription body: one link per line.
//
// A line that cannot be parsed is reported, not fatal. These lists are scraped
// from the open internet and rot constantly; one broken line must not cost the
// operator the other six hundred.
func ParseList(r io.Reader) (nodes []Node, skipped []string, err error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		n, perr := ParseLink(line)
		if perr != nil {
			skipped = append(skipped, line)
			continue
		}
		nodes = append(nodes, n)
	}
	if err := sc.Err(); err != nil {
		return nodes, skipped, err
	}
	return nodes, skipped, nil
}

// NodeKey identifies a node independently of its label.
func NodeKey(n Node) string { return n.ID }

func parseURI(scheme, raw, payload string) (Node, error) {
	body, frag := splitFragment(payload)
	u, err := url.Parse(scheme + "://" + body)
	if err != nil {
		return Node{}, fmt.Errorf("parse %s: %w", scheme, err)
	}
	host := u.Hostname()
	if host == "" {
		return Node{}, fmt.Errorf("%s: no host", scheme)
	}
	port, err := portOf(u.Port(), scheme)
	if err != nil {
		return Node{}, err
	}

	q := u.Query()
	params := make(map[string]string, len(q)+2)
	for k, v := range q {
		if len(v) > 0 {
			params[k] = v[0]
		}
	}

	secret := ""
	if ui := u.User; ui != nil {
		secret = ui.Username()
		if pw, ok := ui.Password(); ok {
			secret += ":" + pw
		}
	}
	params["secret"] = secret

	n := Node{
		Proto:     scheme,
		Transport: transportOf(params["type"]),
		Security:  securityOf(params["security"]),
		Host:      host,
		Port:      port,
		SNI:       params["sni"],
		Label:     frag,
		Raw:       raw,
		params:    params,
	}
	n.ExitGeo = geoFromLabel(frag)
	n.ID = nodeID(n)
	return n, nil
}

func parseVmess(raw, payload string) (Node, error) {
	body, frag := splitFragment(payload)
	dec, err := decodeBase64(body)
	if err != nil {
		return Node{}, fmt.Errorf("vmess: %w", err)
	}
	var v struct {
		PS   string `json:"ps"`
		Add  string `json:"add"`
		Port any    `json:"port"`
		ID   string `json:"id"`
		Net  string `json:"net"`
		Host string `json:"host"`
		Path string `json:"path"`
		SNI  string `json:"sni"`
		TLS  string `json:"tls"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(dec, &v); err != nil {
		return Node{}, fmt.Errorf("vmess json: %w", err)
	}
	port, err := portOf(fmt.Sprint(v.Port), "vmess")
	if err != nil {
		return Node{}, err
	}

	params := map[string]string{
		"secret": v.ID,
		"type":   v.Net,
		"host":   v.Host,
		"path":   v.Path,
		"sni":    v.SNI,
		"tls":    v.TLS,
		"header": v.Type,
	}
	label := v.PS
	if frag != "" {
		label = frag
	}
	n := Node{
		Proto:     "vmess",
		Transport: transportOf(v.Net),
		Security:  securityOf(v.TLS),
		Host:      v.Add,
		Port:      port,
		SNI:       v.SNI,
		Label:     label,
		Raw:       raw,
		params:    params,
	}
	if n.Host == "" {
		return Node{}, fmt.Errorf("vmess: no host")
	}
	n.ExitGeo = geoFromLabel(label)
	n.ID = nodeID(n)
	return n, nil
}

// parseShadowSocks accepts both shapes in circulation: the SIP002 form with the
// method and password base64 encoded before the '@', and the older fully encoded
// form where the whole body is one base64 blob.
func parseShadowSocks(raw, payload string) (Node, error) {
	body, frag := splitFragment(payload)
	creds, hostport := "", ""
	if at := strings.LastIndex(body, "@"); at >= 0 {
		creds, hostport = body[:at], body[at+1:]
	} else {
		dec, err := decodeBase64(body)
		if err != nil {
			return Node{}, fmt.Errorf("ss: %w", err)
		}
		at := strings.LastIndex(string(dec), "@")
		if at < 0 {
			return Node{}, fmt.Errorf("ss: no credentials")
		}
		creds, hostport = string(dec[:at]), string(dec[at+1:])
	}
	if dec, err := decodeBase64(creds); err == nil {
		creds = string(dec)
	}
	method, password, ok := strings.Cut(creds, ":")
	if !ok {
		return Node{}, fmt.Errorf("ss: credentials are not method:password")
	}

	host, portStr, err := splitHostPort(hostport)
	if err != nil {
		return Node{}, err
	}
	port, err := portOf(portStr, "ss")
	if err != nil {
		return Node{}, err
	}

	params := map[string]string{"secret": password, "method": method}
	n := Node{
		Proto:  "ss",
		Host:   host,
		Port:   port,
		Label:  frag,
		Raw:    raw,
		params: params,
	}
	n.ExitGeo = geoFromLabel(frag)
	n.ID = nodeID(n)
	return n, nil
}

func splitFragment(s string) (body, frag string) {
	body, frag, _ = strings.Cut(s, "#")
	if decoded, err := url.PathUnescape(frag); err == nil {
		frag = decoded
	}
	return body, frag
}

func splitHostPort(hostport string) (host, port string, err error) {
	i := strings.LastIndex(hostport, ":")
	if i < 0 {
		return "", "", fmt.Errorf("no port in %q", hostport)
	}
	return strings.Trim(hostport[:i], "[]"), hostport[i+1:], nil
}

func portOf(s, scheme string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("%s: no port", scheme)
	}
	p, err := strconv.Atoi(s)
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("%s: bad port %q", scheme, s)
	}
	return p, nil
}

// transportOf maps share-link vocabulary onto the neutral one.
//
// "tcp" and "raw" both mean "no transport wrapper", which is why they collapse to
// the empty string: a renderer then has nothing to emit for them.
func transportOf(t string) string {
	switch strings.ToLower(t) {
	case "", "tcp", "raw", "none":
		return ""
	}
	return strings.ToLower(t)
}

func securityOf(s string) string {
	switch strings.ToLower(s) {
	case "tls":
		return "tls"
	case "reality":
		return "reality"
	case "":
		return "none"
	}
	return strings.ToLower(s)
}

func decodeBase64(s string) ([]byte, error) {
	s = strings.NewReplacer("\n", "", "\r", "", " ", "").Replace(s)
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
	} {
		if dec, err := enc.DecodeString(s); err == nil {
			return dec, nil
		}
	}
	return nil, fmt.Errorf("not base64")
}

// nodeID hashes everything that makes two lines different servers. The label is
// excluded: the same server appears in several subscriptions under different
// names, and treating those as separate nodes would spend the pool's budget on
// one address.
func nodeID(n Node) string {
	h := sha256.New()
	for _, part := range []string{
		n.Proto, strings.ToLower(n.Host), strconv.Itoa(n.Port),
		n.params["secret"], n.params["uuid"], n.params["password"],
		n.Transport, n.params["path"], n.params["serviceName"], n.SNI,
	} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// geoFromLabel takes the flag out of the label. Display only: the pool never
// decides anything by country.
func geoFromLabel(label string) string {
	rs := []rune(label)
	for i := 0; i+1 < len(rs); i++ {
		if isRegional(rs[i]) && isRegional(rs[i+1]) {
			return string(rs[i : i+2])
		}
	}
	return ""
}

func isRegional(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }
