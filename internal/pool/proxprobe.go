package pool

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SourceProxprobe reads a finished proxprobe pass from its work directory. The pool
// does no fetching, parsing, sorting or reachability testing of its own: that is the
// pass's job, and its artifacts are the contract.
const SourceProxprobe = "proxprobe"

// PassCounts is one proxprobe pass in the terms the interfaces speak.
type PassCounts struct {
	Parsed      int
	HTTPOK      int
	Unsupported int
}

// Pass is a finished proxprobe run read back from its work directory.
type Pass struct {
	Dir         string
	RefreshedAt time.Time
	Nodes       []Node
	Counts      PassCounts
}

// passRecord is the part of a merged.jsonl line the pool reads: the identifier the
// two artifacts are joined by, and the proxy object the pass built for the core.
type passRecord struct {
	ID  string `json:"id"`
	Raw struct {
		Clash json.RawMessage `json:"clash"`
	} `json:"raw"`
}

// httpRecord is the part of an http.jsonl line the pool reads.
type httpRecord struct {
	ID      string  `json:"id"`
	OK      bool    `json:"ok"`
	DelayMS float64 `json:"delay_ms"`
}

// LoadPass reads a finished proxprobe pass.
//
// Only the servers that passed the pass's own http stage are taken: an unverified
// server must not reach the probe, because the probe spends a request of the
// provider's allowance on whatever it is given.
//
// maxAge is checked against the older of the two artifacts: a merged list from an
// hour ago beside a probe from yesterday describes two different sets of servers.
func LoadPass(dir string, maxAge time.Duration, now time.Time) (Pass, error) {
	pass := Pass{Dir: dir}
	if strings.TrimSpace(dir) == "" {
		return pass, errors.New("pool: proxprobe work directory is empty")
	}

	records, mergedAt, err := readMerged(filepath.Join(dir, "merged.jsonl"))
	if err != nil {
		return pass, err
	}
	pass.Counts.Parsed = len(records)

	ok, httpAt, err := readHTTP(filepath.Join(dir, "http.jsonl"))
	if err != nil {
		return pass, err
	}
	pass.Counts.HTTPOK = len(ok)

	pass.RefreshedAt = mergedAt
	if httpAt.Before(mergedAt) {
		pass.RefreshedAt = httpAt
	}
	if maxAge > 0 && now.Sub(pass.RefreshedAt) > maxAge {
		return pass, fmt.Errorf("pool: proxprobe pass is stale: %s old, max %s",
			now.Sub(pass.RefreshedAt).Round(time.Minute), maxAge)
	}

	for _, rec := range records {
		delay, found := ok[rec.ID]
		if !found {
			continue
		}
		node, err := nodeFromClash(rec.ID, rec.Raw.Clash)
		if err != nil {
			pass.Counts.Unsupported++
			continue
		}
		node.ProbeRTT = time.Duration(delay * float64(time.Millisecond))
		pass.Nodes = append(pass.Nodes, node)
	}
	// The pass ranks nothing itself beyond reachability; the order the probe spends
	// its budget in is the pass's own measured delay, cheapest first.
	sort.SliceStable(pass.Nodes, func(i, j int) bool {
		return pass.Nodes[i].ProbeRTT < pass.Nodes[j].ProbeRTT
	})
	return pass, nil
}

// readMerged reads every node the pass produced, in file order.
func readMerged(path string) ([]passRecord, time.Time, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, time.Time{}, fmt.Errorf("pool: proxprobe has no merged.jsonl in %s: run proxprobe all first", filepath.Dir(path))
		}
		return nil, time.Time{}, fmt.Errorf("pool: proxprobe merged.jsonl: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("pool: proxprobe merged.jsonl: %w", err)
	}

	var records []passRecord
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec passRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			// A truncated last line is a pass that died mid-write; the rest of the
			// file is still evidence, and the freshness check will judge the rest.
			continue
		}
		records = append(records, rec)
	}
	if err := scanner.Err(); err != nil {
		return nil, time.Time{}, fmt.Errorf("pool: proxprobe merged.jsonl: %w", err)
	}
	return records, info.ModTime(), nil
}

// readHTTP reads the verdicts of the pass's http stage, keyed by node identifier.
func readHTTP(path string) (map[string]float64, time.Time, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, time.Time{}, fmt.Errorf("pool: proxprobe has no http.jsonl in %s: run proxprobe all first", filepath.Dir(path))
		}
		return nil, time.Time{}, fmt.Errorf("pool: proxprobe http.jsonl: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("pool: proxprobe http.jsonl: %w", err)
	}

	ok := make(map[string]float64)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 16*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec httpRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec.ID == "" {
			continue
		}
		// The last verdict for a node is the pass's answer: a node re-probed after a
		// failure is only in the list if that later attempt succeeded.
		if rec.OK {
			ok[rec.ID] = rec.DelayMS
		} else {
			delete(ok, rec.ID)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, time.Time{}, fmt.Errorf("pool: proxprobe http.jsonl: %w", err)
	}
	return ok, info.ModTime(), nil
}

// nodeFromClash turns the pass's proxy object into a node.
//
// The object is kept verbatim in Raw and rendered as it is: it is already the
// core's own dialect, and re-deriving it from parsed fields would lose the long tail
// (xhttp, packet-encoding, the reality options' exact spelling) that only exists
// there.
func nodeFromClash(id string, raw json.RawMessage) (Node, error) {
	var proxy map[string]any
	if err := json.Unmarshal(raw, &proxy); err != nil {
		return Node{}, fmt.Errorf("no clash profile: %w", err)
	}
	if len(proxy) == 0 {
		return Node{}, errors.New("no clash profile for this node")
	}

	proto, _ := proxy["type"].(string)
	host, _ := proxy["server"].(string)
	port := portNumber(proxy["port"])
	if proto == "" || host == "" || port == 0 {
		return Node{}, errors.New("clash profile is incomplete")
	}
	if !supportsProto(proto) {
		return Node{}, fmt.Errorf("protocol %q is not rendered by the core", proto)
	}

	node := Node{
		ID:    id,
		Proto: proto,
		Host:  host,
		Port:  port,
		Raw:   string(raw),
	}
	if name, ok := proxy["name"].(string); ok {
		node.Label = name
	}
	if sni, ok := proxy["servername"].(string); ok {
		node.SNI = sni
	}
	if node.SNI == "" {
		if sni, ok := proxy["sni"].(string); ok {
			node.SNI = sni
		}
	}
	if network, ok := proxy["network"].(string); ok {
		node.Transport = network
	}
	if tls, ok := proxy["tls"].(bool); ok && tls {
		node.Security = "tls"
	}
	if _, ok := proxy["reality-opts"]; ok {
		node.Security = "reality"
	}
	return node, nil
}

// clashProxy reports whether a node's Raw is a core profile rather than a share
// link, and returns it under the name the rendered config uses.
func clashProxy(raw string) (map[string]any, bool) {
	body := strings.TrimSpace(raw)
	if !strings.HasPrefix(body, "{") {
		return nil, false
	}
	var proxy map[string]any
	if err := json.Unmarshal([]byte(body), &proxy); err != nil {
		return nil, false
	}
	if len(proxy) == 0 {
		return nil, false
	}
	proxy["name"] = mihomoProxyName
	return proxy, true
}

// supportsProto is the core's protocol matrix, shared by the renderer and the pass
// reader so a node is not admitted to a list it can never render.
func supportsProto(proto string) bool {
	switch proto {
	case "vless", "vmess", "ss", "trojan", "hysteria2", "tuic", "anytls":
		return true
	}
	return false
}

// portNumber reads a port out of a decoded JSON object.
func portNumber(raw any) int {
	switch v := raw.(type) {
	case float64:
		if v > 0 && v < 65536 {
			return int(v)
		}
	case int:
		if v > 0 && v < 65536 {
			return v
		}
	case string:
		var port int
		if _, err := fmt.Sscanf(v, "%d", &port); err == nil && port > 0 && port < 65536 {
			return port
		}
	}
	return 0
}
