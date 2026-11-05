package pool

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
	TCPOK       int
	NoTCP       int
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

// tcpRecord is the part of a tcp.jsonl line the pool reads.
//
// The pass's TCP stage is the only measurement pull mode admits, so this record —
// not the http one — is what ranks the list. DialMs is time to a completed
// handshake, which is what "ping over TCP" means here.
type tcpRecord struct {
	ID      string  `json:"id"`
	Server  string  `json:"server"`
	Port    int     `json:"port"`
	OK      bool    `json:"ok"`
	DialMs  float64 `json:"dial_ms"`
	Pool    string  `json:"pool,omitempty"`
	Address string  `json:"resolved_ip,omitempty"`
}

// LoadPass reads a finished proxprobe pass.
//
// WHO MEASURED WHAT. Pull mode does not spend a provider request to rank its
// servers: the pass's TCP stage is the measurement, and the order is its dial time,
// cheapest first. The HTTP stage, when present, is deliberately NOT consulted — a
// GET through every server of a public subscription costs the very allowance the
// pool exists to preserve, and its verdict is stale by the time a request needs it.
// What the provider thinks of a server is settled by the first real request through
// it, and that verdict is the switcher's to act on (a refusal marks the server).
//
// maxAge is checked against the pass's own artifact: a list from an hour ago
// describes servers the subscription has since moved on from.
func LoadPass(dir string, maxAge time.Duration, now time.Time) (Pass, error) {
	pass := Pass{Dir: dir}
	if strings.TrimSpace(dir) == "" {
		return pass, errors.New("pool: proxprobe work directory is empty")
	}

	records, mergedAt, err := readClashProxies(clashProfilePath(dir))
	if err != nil {
		return pass, err
	}
	pass.Counts.Parsed = len(records)

	alive, tcpAt, err := readTCP(passArtifact(dir, "tcp.jsonl"))
	if err != nil {
		return pass, err
	}
	pass.Counts.TCPOK = len(alive)

	pass.RefreshedAt = mergedAt
	if tcpAt.Before(mergedAt) {
		pass.RefreshedAt = tcpAt
	}
	if maxAge > 0 && now.Sub(pass.RefreshedAt) > maxAge {
		return pass, fmt.Errorf("pool: proxprobe pass is stale: %s old, max %s",
			now.Sub(pass.RefreshedAt).Round(time.Minute), maxAge)
	}

	for _, rec := range records {
		// THE JOIN KEY IS server:port, not the pass's node id.
		//
		// proxprobe's id (proto|server|port|12-hex-of-credential-network-trust) cannot
		// be rebuilt from the exported profile: the credential and the reality trust
		// are inside the share link, and the profile spells them differently. The
		// address pair is what BOTH artifacts name, and it is also the pool key the
		// exit-pool deduplication already works on.
		node, err := nodeFromClash(rec.ID, rec.Raw.Clash)
		if err != nil {
			pass.Counts.Unsupported++
			continue
		}
		measure, found := alive[poolKeyOf(node)]
		if !found {
			// The pass's TCP stage did not admit this address, so it is not a
			// candidate. It is NOT "unrenderable": the profile rendered it perfectly
			// well. The two counts answer different questions — one is about this
			// loader, the other about the pass — and conflating them made a healthy run
			// report a hundred unrenderable nodes.
			pass.Counts.NoTCP++
			continue
		}
		node.ProbeRTT = time.Duration(measure.dialMS * float64(time.Millisecond))
		// The pass already resolved the name (and refused a fake-IP answer), so its
		// record is the authority on where this node exits. Carrying it here is what
		// lets the exit pool be deduplicated without a second lookup.
		node.ExitIP = measure.address
		pass.Nodes = append(pass.Nodes, node)
	}
	// The pass ranks nothing itself beyond reachability; the order the probe spends
	// its budget in is the pass's own measured dial time, cheapest first.
	sort.SliceStable(pass.Nodes, func(i, j int) bool {
		return pass.Nodes[i].ProbeRTT < pass.Nodes[j].ProbeRTT
	})
	return pass, nil
}

// passArtifact locates one of the pass's files inside its work directory.
//
// THE LAYOUT MOVED. proxprobe keeps its artifacts under a per-step subdirectory
// (probe/ for merged.jsonl and tcp.jsonl, clash/ for the profiles) — the layout
// package says so and the pass writes them there. This loader used to look in the
// work directory's root, where nothing had been written since the move: a run left
// fresh files in probe/ while the pool read a five-day-old merged.jsonl from the
// root and reported "the pass is stale" on every evaluation.
//
// Both spellings are accepted rather than only the new one: an operator who has
// artifacts from an older proxprobe, or who points the work directory at a copy
// laid out flat, gets the same reading. The subdirectory wins when both exist,
// because that is where the current pass writes.
func passArtifact(dir, name string) string {
	nested := filepath.Join(dir, "probe", name)
	if _, err := os.Stat(nested); err == nil {
		return nested
	}
	return filepath.Join(dir, name)
}

// clashProfilePath is the profile the pass exports, with its proxies.
//
// WHY THIS FILE AND NOT merged.jsonl. A pass run WITHOUT the probe step — which is
// how this pool runs it, because the probe spends one provider request per node —
// leaves merged.jsonl carrying only `raw.link`, the share link as it was
// subscribed. The Clash profile of a node is not in there at all: it is built by
// the export step (proxprobe's emit), which renders the core's own dialect into
// clash/merged.yaml. Reading merged.jsonl was therefore reading a file that could
// not answer, and every node came out "unrenderable".
func clashProfilePath(dir string) string {
	nested := filepath.Join(dir, "clash", "merged.yaml")
	if _, err := os.Stat(nested); err == nil {
		return nested
	}
	return filepath.Join(dir, "clash.yaml")
}

// clashSequenceKeys are the fields the exporter writes as a YAML sequence inside a
// proxy. Only `alpn` exists today; the list is closed for the same reason the nested
// one is — a key this reader was not written for must not be guessed at.
var clashSequenceKeys = map[string]bool{"alpn": true}

// isClashSequenceKey reports whether a head with no value opens a sequence.
func isClashSequenceKey(key string) bool { return clashSequenceKeys[key] }

// clashNestedKeys are the mappings the exporter nests inside a proxy.
//
// The list is closed on purpose. proxprobe's emit builds a proxy from a fixed field
// set, and these three are the only objects in it — a fourth would mean the profile
// has a shape this reader was not written for, and interpreting it would put a
// proxy into the core's config that the exporter never wrote.
var clashNestedKeys = map[string]bool{
	"ws-opts":      true,
	"grpc-opts":    true,
	"reality-opts": true,
}

// readClashProxies reads the proxies of the pass's exported Clash profile.
//
// WHY A HAND-WRITTEN READER AND NOT A YAML LIBRARY. The profile is written by
// proxprobe's own exporter, and the shape it produces is bounded and known: a
// `proxies:` key whose value is a list of flat mappings (`name`, `type`, `server`,
// `port`, the credential, the transport, the security flags), with exactly two
// nested mappings in the whole schema — `ws-opts` and `grpc-opts`. A YAML library
// would be a new dependency for free-ai-router to parse a file this project's
// sibling writes, and the two spellings it cannot express are simply refused
// rather than guessed at.
//
// The reader keeps each proxy as a JSON object, which is what nodeFromClash
// consumes and what the pool renders into the core's config VERBATIM — a proxy
// re-derived from typed fields would lose the long tail (packet-encoding, the
// reality options' spelling) that only exists in the profile.
func readClashProxies(path string) ([]passRecord, time.Time, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, time.Time{}, fmt.Errorf("pool: proxprobe has no Clash profile in %s: run the pass (proxprobe export) first", filepath.Dir(path))
		}
		return nil, time.Time{}, fmt.Errorf("pool: proxprobe Clash profile: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("pool: proxprobe Clash profile: %w", err)
	}

	records, err := parseClashProxies(file)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("pool: proxprobe Clash profile: %w", err)
	}
	return records, info.ModTime(), nil
}

// parseClashProxies walks the `proxies:` list and returns one record per entry.
//
// The parser is a line reader with one level of indentation tracking, because that
// is all the exporter's output needs. Two things are refused rather than
// interpreted: a line it cannot place, and a nested mapping inside a proxy other
// than the two that exist in the schema. Guessing at either would put a proxy into
// the core's config that the exporter never wrote.
func parseClashProxies(r io.Reader) ([]passRecord, error) {
	var (
		out      []passRecord
		current  map[string]any
		nested   map[string]any // the ws-opts / grpc-opts object of the current proxy
		inList   bool
		nestedOn string
	)

	flush := func() {
		if current == nil {
			return
		}
		key, ok := current["name"].(string)
		if !ok || key == "" {
			key = stringOr(current["server"], "")
		}
		raw, err := json.Marshal(current)
		if err == nil {
			out = append(out, passRecord{ID: proxyID(key, current), Raw: struct {
				Clash json.RawMessage `json:"clash"`
			}{Clash: raw}})
		}
		current = nil
	}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))

		switch {
		case indent == 0:
			// A top-level key ends the list.
			flush()
			nested, nestedOn = nil, ""
			inList = strings.HasPrefix(trimmed, "proxies:")
		case !inList:
			// Some other top-level document: ignore it.
		case strings.HasPrefix(trimmed, "- ") && indent <= 2:
			// A new list item begins a new proxy. The exporter writes the fields in
			// Go's map order, so the FIRST field is often the nested object itself
			// (`- grpc-opts:`), and that line opens both the item and the nesting.
			//
			// The indent guard is what keeps a YAML SEQUENCE VALUE from being read as a
			// new proxy: `- alpn:` is followed by its items (`- http/1.1`) indented at 6,
			// and without the guard every item of every list became a proxy of its own —
			// which is how a profile of 202 entries produced a hundred empty ones.
			flush()
			nested, nestedOn = nil, ""
			current = map[string]any{}
			rest := trimmed[2:]
			if key, value := splitClashPair(rest); key != "" && value == "" {
				// A head with no value: either a nested object (`ws-opts:`) or the head
				// of a sequence (`alpn:`, whose items are the `- ...` lines that
				// follow). Both are recorded as an empty mapping here; a sequence turns
				// into a list when its first item arrives (appendNestedItem), and a
				// nested object is filled in place.
				nested = map[string]any{}
				nestedOn = key
				current[key] = nested
			} else {
				applyClashScalar(current, rest)
			}
		case current != nil && indent == 4:
			// A field of the proxy itself. It CLOSES any nesting the previous line
			// opened: `alpn:` followed by `- h2` is a sequence, and the next line at this
			// level (`name:`) is a field of the proxy again — not another item of that
			// sequence, and not a field of the nested object.
			nested, nestedOn = nil, ""
			key, value := splitClashPair(trimmed)
			if key == "" {
				continue
			}
			if value == "" {
				if !clashNestedKeys[key] && key != "alpn" && key != "skip-cert-verify" && !isClashSequenceKey(key) {
					// An unknown head is refused rather than guessed at: a nested object
					// this reader was not written for must not reach the core's config.
					return nil, fmt.Errorf("unsupported nested key %q in a proxy", key)
				}
				nested = map[string]any{}
				nestedOn = key
				current[key] = nested
				continue
			}
			applyClashScalar(current, trimmed)
		case current != nil && strings.HasPrefix(trimmed, "- "):
			// An item of a YAML sequence that belongs to a field of the current proxy
			// (`alpn:` followed by `- h2`, `- http/1.1`). The fields the core needs are
			// scalars, and a list is carried for reference only — it is recorded as the
			// list it is, so nothing is invented for the core's config.
			//
			// The target is looked up in the proxy rather than held in a variable: the
			// head line records an empty mapping when the list is empty, and the first
			// item REPLACES that value with a list, so a variable captured at the head
			// would go on filling an object nothing points at any more.
			if nestedOn != "" {
				if list, ok := current[nestedOn]; ok {
					current[nestedOn] = appendClashItem(list, trimmed[2:])
					nested, _ = current[nestedOn].(map[string]any)
				}
			}
		case current != nil && nestedOn != "" && indent >= 6:
			// A field of the nested object, one level deeper than the proxy's own
			// fields: ws-opts.path (6), grpc-opts.grpc-service-name (6),
			// ws-opts.headers.Host (8). The threshold is 6, not 4 — a proxy's own
			// fields sit at 4, and treating those as nested folded whole proxies into
			// the object that preceded them.
			applyNestedScalar(nested, trimmed, indent)
		case current != nil && nestedOn == "":
			// A line deeper than a field, with no object open: nothing in the exporter's
			// output looks like this, so it is refused rather than ignored.
			return nil, fmt.Errorf("unsupported indentation in a proxy: %q", trimmed)
		case current != nil:
			applyNestedScalar(nested, trimmed, indent)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	flush()
	if len(out) == 0 {
		return nil, errors.New("the profile declares no proxies")
	}
	return out, nil
}

// applyClashScalar writes one `key: value` pair into a proxy.
func applyClashScalar(target map[string]any, pair string) {
	key, value := splitClashPair(pair)
	if key == "" {
		return
	}
	target[key] = clashScalar(key, value)
}

// appendClashItem appends one item to a sequence value, turning the empty mapping
// the head line left behind into a list on first use.
//
// The head of a sequence is written as `alpn:` with its items below it, and nothing in
// the line says "list" — so the reader records an empty mapping at the head and
// converts it here, when the first item makes the kind known.
func appendClashItem(current any, item string) any {
	switch v := current.(type) {
	case map[string]any:
		if len(v) == 0 {
			return []any{clashScalar("alpn", item)}
		}
		return current
	case []any:
		return append(v, clashScalar("alpn", item))
	default:
		return []any{clashScalar("alpn", item)}
	}
}

// findClashObject returns the first nested object of a proxy, for the readers that
// need to descend one level without naming the key.
func findClashObject(proxy map[string]any) map[string]any {
	for _, value := range proxy {
		if obj, ok := value.(map[string]any); ok {
			return obj
		}
	}
	return nil
}

// applyNestedScalar writes one pair into a nested object.
//
// The exporter nests two levels at most: `<x>-opts` → a scalar field, and
// `ws-opts.headers` → `Host`. A line indented deeper than the object's own fields is
// therefore a field of `headers` and goes there rather than overwriting it; at the
// object's own level it is a plain field.
func applyNestedScalar(target map[string]any, pair string, indent int) {
	if target == nil {
		return
	}
	if strings.HasSuffix(strings.TrimSpace(pair), ":") {
		// The head of a deeper mapping (`headers:`), which the exporter writes at one
		// level below the object's fields.
		key := strings.TrimSuffix(strings.TrimSpace(pair), ":")
		if _, ok := target[key]; !ok {
			target[key] = map[string]any{}
		}
		return
	}
	key, value := splitClashPair(pair)
	if key == "" {
		return
	}
	// A line deeper than the object's own fields belongs to the mapping that was
	// opened last (the `headers` object).
	if indent >= 8 {
		for _, nestedValue := range target {
			if headers, ok := nestedValue.(map[string]any); ok {
				headers[key] = clashScalar(key, value)
				return
			}
		}
	}
	target[key] = clashScalar(key, value)
}

// splitClashPair splits a `key: value` line, trimming the quotes the exporter puts
// around values that need them (names with emoji, paths).
func splitClashPair(line string) (key, value string) {
	at := strings.Index(line, ":")
	if at < 0 {
		return "", ""
	}
	key = strings.TrimSpace(line[:at])
	value = strings.TrimSpace(line[at+1:])
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		value = value[1 : len(value)-1]
	}
	return key, value
}

// clashNumericKeys are the fields the core reads as a NUMBER.
//
// WHY A LIST AND NOT A GUESS. A YAML scalar that happens to look like digits is not
// a number: `short-id: 1` and `short-id: 283918a3` are both strings in the reality
// options, and a reader that infers the type from the text turns the first into a
// number and the second into a string — the same field, two types, and the core
// rejects the connection for the one it read as a number. The kind follows the FIELD,
// which is what this list states.
var clashNumericKeys = map[string]bool{
	"port":    true,
	"alterId": true,
	"mtu":     true,
}

// clashBooleanKeys are the fields the core reads as a BOOLEAN.
var clashBooleanKeys = map[string]bool{
	"tls":             true,
	"udp":             true,
	"skip-cert-verify": true,
	"tfo":             true,
}

// clashScalar turns a written YAML scalar into the Go value the core's config needs.
//
// The type is taken from the KEY, never from how the value happens to read — see
// clashNumericKeys. Three literal forms are recognised regardless of the key, because
// they cannot be anything else: the empty flow mappings `{}` and `[]` that the
// exporter emits for a collection it has nothing to put in.
func clashScalar(key, value string) any {
	switch value {
	case "null", "~", "":
		return nil
	case "{}":
		return map[string]any{}
	case "[]":
		return []any{}
	}
	if clashBooleanKeys[key] {
		return value == "true"
	}
	if clashNumericKeys[key] {
		if n, err := strconv.Atoi(value); err == nil {
			return n
		}
	}
	// Everything else is a string. That includes a port that arrived quoted, a
	// short-id of digits, and a name that is nothing but digits.
	return value
}

// proxyID mints the identifier the TCP artifact is joined by.
//
// The exporter does not carry proxprobe's node id into the profile, so the join
// key is rebuilt from what the profile does name: the server and the port, plus
// the protocol. It matches the way this package's nodes are identified on the wire
// (`proto|server|port`), which is what PoolKey uses downstream.
func proxyID(name string, proxy map[string]any) string {
	proto := stringOr(proxy["type"], "")
	server := stringOr(proxy["server"], "")
	port := fmt.Sprint(proxy["port"])
	if server == "" {
		return name
	}
	return strings.Join([]string{proto, server, port}, "|")
}

// stringOr returns the string value of v, or def.
func stringOr(v any, def string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return def
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

// tcpMeasure is what the pass measured about one node.
type tcpMeasure struct {
	dialMS  float64
	address string
}

// readTCP reads the verdicts of the pass's TCP stage, keyed by node identifier.
//
// The last verdict for a node is the pass's answer: a node re-probed after a failure
// is only in the list if that later attempt succeeded.
func readTCP(path string) (map[string]tcpMeasure, time.Time, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, time.Time{}, fmt.Errorf("pool: proxprobe has no tcp.jsonl in %s: run proxprobe ping first, or proxprobe all", filepath.Dir(path))
		}
		return nil, time.Time{}, fmt.Errorf("pool: proxprobe tcp.jsonl: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("pool: proxprobe tcp.jsonl: %w", err)
	}

	alive := make(map[string]tcpMeasure)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 16*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec tcpRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec.ID == "" {
			continue
		}
		// The key is the address pair, not the pass's id: the exported profile does
		// not carry the id, and server:port is what both artifacts name (see the join
		// in LoadPass).
		key := poolKeyOfRaw(rec.Server, rec.Port)
		if key == "" {
			continue
		}
		if rec.OK {
			alive[key] = tcpMeasure{dialMS: rec.DialMs, address: rec.Address}
		} else {
			delete(alive, key)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, time.Time{}, fmt.Errorf("pool: proxprobe tcp.jsonl: %w", err)
	}
	return alive, info.ModTime(), nil
}

// poolKeyOf is the address pair of a node, as the pass writes it into tcp.jsonl.
func poolKeyOf(n Node) string { return poolKeyOfRaw(n.Host, n.Port) }

// poolKeyOfRaw builds the join key from a host and a port.
//
// Lowercased, because DNS names are case-insensitive and a subscription that spells
// one server two ways would otherwise miss its own measurement.
func poolKeyOfRaw(host string, port int) string {
	if host == "" || port == 0 {
		return ""
	}
	return strings.ToLower(host) + ":" + strconv.Itoa(port)
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
