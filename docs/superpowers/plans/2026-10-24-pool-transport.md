# Pool Transport Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `transport = "pool"` to free-ai-router: build a ranked list of third-party servers from subscriptions, verify each one the way the provider verifies it, and rotate between them between requests.

**Architecture:** A new `internal/pool` package owns the whole subsystem: subscription parsing, a TCP stage, a provider probe through a per-node core process (mihomo), a persisted ranked registry, and an `http.RoundTripper` that atomically swaps to the next tunnel. It registers into the existing `transport.Registry` as one more `transport.Transport`; the gateway learns nothing except one error value.

**Tech Stack:** Go 1.26 (stdlib only — no new third-party dependencies), mihomo as an external process from `PATH`, existing packages `internal/transport`, `internal/provider`, `internal/egress`, `internal/control`, `internal/tui`.

**Spec:** `docs/superpowers/specs/2026-10-24-pool-transport-design.md`

## Global Constraints

- `CGO_ENABLED=0` for every build and test run.
- `gofmt -l .` must print nothing; `go vet ./...` clean; `go test ./... -count=1` green.
- Code, comments, docs, test names and commit prefixes in English; comments explain *why*, never *what*.
- No new third-party dependencies. Standard library only.
- `internal/transport` must not import `internal/provider`. `internal/pool` must not import `internal/gateway`.
- The gateway does not retry provider refusals and returns refusals verbatim; pool switching happens between requests, never as a retry loop.
- Every value that reaches the provider's wire contract comes from `internal/provider` (`Build`, `Classify`, `ApplyCredential`), never re-implemented in `internal/pool`.
- Schedule slots are 03:00, 11:00, 19:00 UTC; the pool owns loopback ports 38200–38399.
- Evaluation is non-destructive: a new list replaces the old one only after it is complete.
- Live checks in this plan hit real subscriptions and the real provider; `go test` must not.

## Review Focus

- A subscription URL answers with an HTML error page or a captive-portal body: the fetcher must count it as a bad subscription and keep the pool from wiping a good list.
- A share link carries an IPv6 literal (`vless://uuid@[2001:db8::1]:443?...`): parsing and later dialling must work without bracket loss.
- A node's exit address changes between the probe and the request (mobile NAT): the registry must not quarantine a node for a stale measurement.
- A core binary renders a config it then rejects at runtime (config validates, transport fails): the node is quarantined with the render error, and the router keeps serving through the previous tunnel.
- `pool.json` written by a newer version, or a truncated file after `kill -9`: loading must fall back to an empty pool and re-evaluate instead of failing the daemon start.

---

### Task 1: Share-link parser

**Files:**
- Create: `internal/pool/share.go`
- Create: `internal/pool/share_test.go`
- Create: `internal/pool/testdata/links.txt` (fixtures copied from the real subscriptions)

**Interfaces:**
- Consumes: nothing.
- Produces: `type Node struct{ ID, Proto, Transport, Security, Host string; Port int; SNI, Label, Raw string; TCPRTT, ProbeRTT time.Duration; ExitIP, ExitGeo string }`, `func ParseLink(raw string) (Node, error)`, `func ParseList(r io.Reader) (nodes []Node, skipped []string, err error)`, `func NodeKey(n Node) string`.

- [ ] **Step 1: Write the failing test**

```go
func TestParseLinkVLESSReality(t *testing.T) {
	raw := "vless://8dc7722c-2767-4eea-a28b-2f8daacc07e3@sca12.myfymain.com:8880?mode=gun&security=none&encryption=none&type=grpc#🇮🇹"
	n, err := ParseLink(raw)
	if err != nil { t.Fatalf("parse: %v", err) }
	if n.Proto != "vless" || n.Host != "sca12.myfymain.com" || n.Port != 8880 { t.Fatalf("got %+v", n) }
	if n.Transport != "grpc" || n.Security != "none" { t.Fatalf("got %+v", n) }
	if n.Label != "🇮🇹" || n.ExitGeo != "🇮🇹" { t.Fatalf("label %q geo %q", n.Label, n.ExitGeo) }
	if n.Raw != raw { t.Fatal("raw link must be kept verbatim") }
}

func TestParseLinkVariants(t *testing.T) { /* table: vless ws+tls+path+host, vless reality+sni+pbk+fp,
	vless type=tcp with no transport, vless type=raw, xhttp, vmess base64 JSON, ss method/password,
	trojan, hysteria2; each asserts Proto/Transport/Security/Host/Port */ }

func TestParseLinkIPv6Literal(t *testing.T) {
	n, err := ParseLink("vless://u@[2001:db8::1]:443?security=tls&type=ws#x")
	if err != nil { t.Fatalf("parse: %v", err) }
	if n.Host != "2001:db8::1" || n.Port != 443 { t.Fatalf("got %+v", n) }
}

func TestParseListCountsSkipped(t *testing.T) { /* one good line, one garbage line, one empty line →
	1 node, 1 skipped (empty lines are not skipped) */ }

func TestNodeKeyIgnoresLabel(t *testing.T) { /* same link with two different #fragments → equal keys */ }
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/kyomufs/Projects/free-ai-router && CGO_ENABLED=0 go test ./internal/pool/ -run TestParse -v`
Expected: FAIL — `package internal/pool` is not defined / `undefined: ParseLink`.

- [ ] **Step 3: Implement the parser**

Parse with `net/url` after splitting the scheme by hand (`url.Parse` mangles `vmess://<base64>` and unescaped fragment emoji). Scheme dispatch: `vless`, `trojan`, `hysteria2`, `tuic` are URI-shaped; `vmess` is base64 JSON (`{v,ps,add,port,id,aid,net,type,host,path,tls,sni}` fields); `ss` is `ss://base64(method:password)@host:port` or the fully base64 form — support both. Map share-link vocabulary to neutral fields: `type=` → `Transport` with `tcp`/`raw` → `""`; `security=` → `Security` (`tls`/`reality`/`none`); `sni` → `SNI`; `host` stays in `Raw` only. `NodeKey` = hex of `sha256` over `proto|host|port|uuid-or-password|transport|path|serviceName|sni`, first 16 chars, and it is the `ID`.

- [ ] **Step 4: Run test to verify it passes**

Run: `CGO_ENABLED=0 go test ./internal/pool/ -run TestParse -v && CGO_ENABLED=0 go test ./internal/pool/ -run TestNodeKey`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/pool/share.go internal/pool/share_test.go internal/pool/testdata/links.txt
git commit -m "feat(pool): parse share links into nodes"
```

---

### Task 2: Subscription fetching

**Files:**
- Create: `internal/pool/fetch.go`
- Create: `internal/pool/fetch_test.go`

**Interfaces:**
- Consumes: `ParseList` from Task 1.
- Produces: `type Subscription struct{ URL string; Nodes []Node; Skipped []string; Err error }`, `func FetchAll(ctx context.Context, urls []string, timeout time.Duration, client *http.Client) []Subscription`, `func DecodeBody(b []byte) ([]byte, bool)` (returns plain text, and whether the body is a link list at all), `func Merge(subs []Subscription, cap int) ([]Node, int)` (nodes, unsupported-line count).

- [ ] **Step 1: Write the failing test** — httptest server returning (a) plain text list, (b) base64-encoded list, (c) an HTML error page, (d) 500; assert `FetchAll` fills `Nodes` for a and b, sets `Err` for d, leaves `Nodes` empty and sets `Err` for c; `Merge` deduplicates a node present in two subscriptions with different labels and counts the html/unsupported lines.
- [ ] **Step 2: Run test to verify it fails** — `CGO_ENABLED=0 go test ./internal/pool/ -run TestFetch -v` → FAIL, undefined.
- [ ] **Step 3: Implement** — three attempts with 1 s and 3 s backoff; `DecodeBody` treats a body whose first non-space byte is `<` as not-a-list, otherwise tries schemes, else base64 (std and URL-safe, padded and raw); `Merge` keys by `NodeKey` and keeps the first label seen.
- [ ] **Step 4: Run test to verify it passes.**
- [ ] **Step 5: Commit** — `feat(pool): fetch and merge subscriptions`.

---

### Task 3: TCP stage

**Files:**
- Create: `internal/pool/probe.go`
- Create: `internal/pool/probe_test.go`

**Interfaces:**
- Consumes: `Node` (Task 1).
- Produces: `func ScanTCP(ctx context.Context, nodes []Node, workers int, timeout time.Duration) []Node` — returns nodes whose `host:port` accepted a connection within `timeout`, `TCPRTT` filled, sorted ascending by it; and `func dialAddress(n Node) string` which brackets IPv6 literals via `net.JoinHostPort`.

- [ ] **Step 1: Write the failing test** — start two `net.Listener`s on `127.0.0.1:0`; one node pointing at each plus one pointing at a closed port; assert only the two live ones come back, sorted by RTT, and that `dialAddress` returns `[::1]:443` for an IPv6 host.
- [ ] **Step 2: Run test to verify it fails.**
- [ ] **Step 3: Implement** — a semaphore of `workers`, `net.DialTimeout("tcp", dialAddress(n), timeout)`, results collected into a slice, then `sort.Slice` by `TCPRTT`.
- [ ] **Step 4: Run test to verify it passes.**
- [ ] **Step 5: Commit** — `feat(pool): add the tcp reachability stage`.

---

### Task 4: Core interface, mihomo renderer, process runner

**Files:**
- Create: `internal/pool/core.go`
- Create: `internal/pool/core_mihomo.go`
- Create: `internal/pool/core_mihomo_test.go`
- Create: `internal/pool/runner.go`
- Create: `internal/pool/runner_test.go`

**Interfaces:**
- Consumes: `Node` (Task 1).
- Produces:
  - `type Core interface{ ID() string; Supports(n Node) bool; Config(n Node, port int) ([]byte, error) }`
  - `func NewMihomo() Core`
  - `type Runner struct{ ... }`, `func NewRunner(binary, dir string) *Runner`
  - `type Tunnel struct{ Node Node; Port int; Proxy *url.URL; cmd *exec.Cmd }`
  - `func (r *Runner) Start(ctx context.Context, c Core, n Node) (*Tunnel, error)`
  - `func (t *Tunnel) Stop(grace time.Duration)`
  - `func FreePort(lo, hi int) (int, error)`

- [ ] **Step 1: Write the failing tests**

```go
func TestMihomoConfigIsAcceptedByMihomo(t *testing.T) {
	// skipped when the binary is absent: the renderer's contract is that mihomo itself accepts it
	if _, err := exec.LookPath("mihomo"); err != nil { t.Skip("mihomo not installed") }
	dir := t.TempDir()
	cfg, err := NewMihomo().Config(Node{Raw: "vless://8dc7722c-2767-4eea-a28b-2f8daacc07e3@sca12.myfymain.com:8880?mode=gun&security=none&encryption=none&type=grpc#🇮🇹",
		Proto: "vless", Host: "sca12.myfymain.com", Port: 8880, Transport: "grpc", Security: "none"}, 38201)
	if err != nil { t.Fatal(err) }
	writeConfig(t, dir, cfg)
	if out, err := exec.Command("mihomo", "-t", "-d", dir, "-f", filepath.Join(dir, "config.json")).CombinedOutput(); err != nil {
		t.Fatalf("mihomo rejected the config: %v\n%s", err, out)
	}
}

func TestRunnerStartsAndStops(t *testing.T) { /* fakes: a core whose Config writes a mixed-port-only config with no
	proxies and rules MATCH,DIRECT; Start → Proxy host is 127.0.0.1, Port within range, dial to it succeeds;
	Stop(0) → dial refused within 2s and no zombie (cmd.Wait called) */ }

func TestRunnerRejectsWhenCoreFailsToBecomeReady(t *testing.T) { /* core binary that exits immediately →
	Start returns an error and no tunnel */ }
```

- [ ] **Step 2: Run tests to verify they fail.**
- [ ] **Step 3: Implement.** `NewMihomo().Supports` returns true for `vless|vmess|ss|trojan|hysteria2|tuic`. Config renders JSON (valid YAML for mihomo): `{log-level:"warning", mixed-port:<port>, "allow-lan":false, proxies:[…one node…], rules:["MATCH,node"]}`; the node object is built per protocol with uTLS `client-fingerprint` taken from the link's `fp` when present. `Runner.Start` resolves the binary (`r.binary`, else `exec.LookPath`), allocates a port via `FreePort`, writes the config to a per-tunnel temp dir, sets `cmd.Cancel`/`WaitDelay`, starts it, polls `net.DialTimeout("tcp", "127.0.0.1:<port>", 200ms)` until success or 5 s, and returns a `Tunnel`. `Stop` sends `SIGTERM`, waits `grace` (and at most 2 s more), then `SIGKILL`, always `Wait`s.
- [ ] **Step 4: Run tests to verify they pass** (`-count=1`, mihomo test must actually run, not skip — verify with `-v`).
- [ ] **Step 5: Commit** — `feat(pool): run one core process per node`.

---

### Task 5: Provider probe (credential helper, exit address, verdict)

**Files:**
- Create: `internal/provider/credential.go`
- Modify: `internal/provider/credential_test.go` (create if absent)
- Modify: `internal/provider/provider.go` (`Failure` gains `AddressRejected bool`)
- Modify: `internal/provider/opencode/classify.go` (set it where `freeLaneRefusal` matches)
- Modify: `internal/gateway/gateway.go:1104-1143` (delegate to the provider helper)
- Modify: `internal/egress/egress.go` (export `Observe`)
- Create: `internal/pool/judge.go`
- Create: `internal/pool/judge_test.go`

**Interfaces:**
- Consumes: `provider.Provider`, `provider.Build`, `provider.Classify`, `Node` (Task 1), `Tunnel` (Task 4).
- Produces:
  - `func provider.ApplyCredential(p Provider, prepared Prepared, key string)`
  - `Failure.AddressRejected bool`, set by the opencode classifier when the free-lane address refusal matches
  - `func egress.Observe(ctx context.Context, rt http.RoundTripper, family string) (string, error)`
  - `type Prober struct{ Provider provider.Provider; Model provider.Model; Key func() string; Timeout time.Duration }`
  - `type Verdict struct{ Accepted bool; AddressRefused bool; Status int; RTT time.Duration; ExitIP string; Reason string }`
  - `func (p Prober) Run(ctx context.Context, t *Tunnel) Verdict`

- [ ] **Step 1: Write the failing tests**
  - `internal/provider`: `TestApplyCredentialSetsProviderShape` — a provider whose `CredentialHeader()` returns `("x-api-key", "")` gets `x-api-key: k`; a provider with no shape gets `authorization: Bearer public` when the key is empty.
  - `internal/pool/judge_test.go`: `TestProberAccepts200AndRecordsExitIP` (httptest server returning `data: {"choices":[]}` for the completion and a trace body for `cdn-cgi/trace`, both reached through a fake tunnel round tripper), `TestProberFlagsAddressRefusal` (body `{"error":{"message":"free models aren't available from your current network"}}` → `AddressRefused` true, `Accepted` false), `TestProberKeepsNodeOnQuotaRefusal` (429 with a quota body → `Accepted` true, `AddressRefused` false, `Reason` non-empty).
- [ ] **Step 2: Run tests to verify they fail.**
- [ ] **Step 3: Implement.** Move `credentialValue`/`credentialShape` from `internal/gateway` into `internal/provider/credential.go` as unexported helpers behind the exported `ApplyCredential`; the gateway's `applyCredential` becomes a one-line delegation (behaviour unchanged — the existing gateway tests must stay green). Extract the trace round trip from `egress.resolve` into `Observe` and call it from both places. `Prober.Run` builds the request with `Provider.Build(provider.Request{Model: p.Model, Messages: []provider.Message{{Role: "user", Text: "hi"}}, Stream: true, MaxTokens: 1})`, applies the credential, sends it through an `http.Client{Transport: tunnelRT}`, judges 2xx by reading at most one event, and any other status by `Provider.Classify` (address refusal ⇔ `Failure.AddressRejected`, which the classifier must now set).
- [ ] **Step 4: Run tests to verify they pass**, plus `CGO_ENABLED=0 go test ./internal/gateway/ ./internal/provider/ ./internal/egress/ ./internal/pool/ -count=1`.
- [ ] **Step 5: Commit** — `feat(pool): judge a node by a real provider request`.

---

### Task 6: Registry — ranking, quarantine, cursor

**Files:**
- Create: `internal/pool/registry.go`
- Create: `internal/pool/registry_test.go`

**Interfaces:**
- Consumes: `Node` (Task 1), `Verdict` (Task 5).
- Produces:
  - `type Status string` (`active`, `quarantined`)
  - `type Entry struct{ Node Node; Status Status; Reason string; Since time.Time }`
  - `func NewRegistry(maxNodes, deadThreshold int) *Registry`
  - `func (r *Registry) Replace(nodes []Node)` — dedupe by `ExitIP` keeping the best `ProbeRTT`, sort, cap, drop the previous quarantine
  - `func (r *Registry) Active() (Node, bool)`, `func (r *Registry) Next() (Node, bool)`, `func (r *Registry) Suspect(id, reason string)`, `func (r *Registry) Succeed(id string)`
  - `func (r *Registry) Snapshot() Snapshot` with `type Snapshot struct{ Active string; Relevant int; Quarantined []Entry; Nodes []Node }`
  - all methods safe for concurrent use (`sync.Mutex`)

- [ ] **Step 1: Write the failing tests** — dedupe by exit IP keeps the lower `ProbeRTT`; `Replace` caps at `maxNodes`; `Next` cycles and skips quarantined ones; a node reaching `deadThreshold` consecutive `Suspect` calls becomes quarantined and is not returned by `Next`; one `Succeed` resets the counter; `Replace` clears quarantine.
- [ ] **Step 2: Run tests to verify they fail.**
- [ ] **Step 3: Implement** as above, keeping the cursor index stable across `Replace` by node ID when possible.
- [ ] **Step 4: Run tests to verify they pass.**
- [ ] **Step 5: Commit** — `feat(pool): rank and quarantine servers`.

---

### Task 7: Persistence and the evaluator

**Files:**
- Create: `internal/pool/store.go`
- Create: `internal/pool/store_test.go`
- Create: `internal/pool/evaluate.go`
- Create: `internal/pool/evaluate_test.go`

**Interfaces:**
- Consumes: everything from Tasks 1–6.
- Produces:
  - `type State struct{ Version int; EvaluatedAt, NextEvalAt time.Time; EvalState string; Nodes []Node; Quarantined []Entry }`
  - `func Load(path string) (State, error)` (missing file → zero `State`, nil error; `Version != 1` → zero `State`, nil error and a logged reason)
  - `func Save(path string, s State) error` (temp file in the same dir, `fsync`, rename)
  - `func NextEvaluation(now, first time.Time, interval time.Duration) time.Time` — the next instant among `first`, `first+interval`, `first+2*interval` in UTC strictly after `now`
  - `type Evaluator struct{ ... }`, `func (e *Evaluator) Run(ctx context.Context) Report`, `type Report struct{ Started, Finished time.Time; Fetched, Parsed, TCPAlive, Probed, Accepted int; Err error }`
  - `func (e *Evaluator) Progress() (done, total int)`
- [ ] **Step 1: Write the failing tests** — store round trip; truncated `pool.json` → empty state, no error; `Version: 7` → empty state; `NextEvaluation` for 02:59, 03:00, 03:01, 19:00 and 23:00 UTC; evaluator end-to-end with fake stages (injected `fetchFn`, `scanFn`, `probeFn`) asserting the pipeline order, that a probe refusal with `AddressRefused` does not enter the registry, and that a mid-run error leaves the previous registry untouched.
- [ ] **Step 2: Run tests to verify they fail.**
- [ ] **Step 3: Implement.** Stage functions are struct fields so tests inject fakes (`FetchAll`, `ScanTCP`, `Prober.Run`); the evaluator writes `State` through `Save`, sets `EvalState` to `evaluating`/`idle`/`failed`, updates `NextEvalAt`, and never mutates the registry until every stage succeeded.
- [ ] **Step 4: Run tests to verify they pass.**
- [ ] **Step 5: Commit** — `feat(pool): persist the pool and evaluate it`.

---

### Task 8: Switcher, transport adapter, gateway contract

**Files:**
- Create: `internal/pool/switcher.go`
- Create: `internal/pool/switcher_test.go`
- Create: `internal/pool/transport.go`
- Modify: `internal/gateway/gateway.go` (transport-failure branch, ~line 685-693)
- Modify: `internal/gateway/bounds.go` (`dropConnections` reaches the pool switcher)

**Interfaces:**
- Consumes: `Registry` (Task 6), `Runner`/`Tunnel` (Task 4).
- Produces:
  - `var ErrNotReady = errors.New("pool: no usable server yet")`
  - `type Switcher struct{ ... }`, `func NewSwitcher(cfg SwitcherConfig) *Switcher` with `SwitcherConfig{ Registry *Registry; Runner *Runner; Core Core; Grace time.Duration; Log Logger }`
  - `func (s *Switcher) RoundTripper() (http.RoundTripper, error)`, `func (s *Switcher) Suspect(reason string)`, `func (s *Switcher) Switch(ctx context.Context) error`, `func (s *Switcher) CloseIdleConnections()`
  - `type Transport struct{ ... }` implementing `transport.Transport` (`ID() == "pool"`, `Info()` with `NeedsExternalProcess: true`), plus `func New(cfg map[string]any) (transport.Transport, error)`
  - Gateway: `if errors.Is(err, pool.ErrNotReady) { writeError(w, http.StatusServiceUnavailable, "pool", message); return }` before the 502 branch, with `Retry-After: 30`.
- [ ] **Step 1: Write the failing tests** — switcher with fake tunnels: `RoundTripper` proxy URL changes after `Suspect` + `Switch`; a failed `Start` leaves the previous proxy URL in place; `ErrNotReady` is returned when the registry is empty; gateway test asserting a `RoundTripper` returning `pool.ErrNotReady` produces 503 with `"type":"pool"` and no `recordAbort` as a refusal.
- [ ] **Step 2: Run tests to verify they fail.**
- [ ] **Step 3: Implement.** `atomic.Pointer[tunnel]`; `http.Transport` with `Proxy: func(*http.Request) (*url.URL, error)` reading the pointer, `DialContext` defaults, and `MaxIdleConnsPerHost` from nothing (defaults are fine — say so in a comment); `Switch` starts the new tunnel, swaps, calls `CloseIdleConnections`, then stops the old one after `Grace` in a goroutine; `Suspect` records in the registry and triggers `Switch`. `Transport.Start` kicks off the evaluator and the health loop, `Stop` stops the active tunnel and the evaluator.
- [ ] **Step 4: Run tests to verify they pass** plus the whole suite.
- [ ] **Step 5: Commit** — `feat(pool): switch tunnels between requests`.

---

### Task 9: Configuration, wiring, control API, CLI

**Files:**
- Modify: `internal/axes/axes.go` (sixteen new entries, `ScopeGlobal`, `ConfigKey: transport_settings.<key>`)
- Modify: `cmd/free-ai-router/up.go` (`build()`: register the pool transport; fix the egress resolver to use the configured transport instead of `transport.Pinned`)
- Modify: `cmd/free-ai-router/flags.go` (`--pool-*`)
- Modify: `internal/control/handlers.go` (`GET /_control/pool`, `POST /_control/pool/recheck`)
- Create: `cmd/free-ai-router/pool.go` (read command)
- Modify: `cmd/free-ai-router/main.go` (command dispatch)

**Interfaces:**
- Consumes: Tasks 1–8.
- Produces: `pool.Settings` decoded from `map[string]any` with every default from the spec's table; `pool.Snapshot` exposed through `control.Status`-shaped JSON; `free-ai-router pool` and `free-ai-router pool recheck`.
- [ ] **Step 1: Write the failing tests** — `TestPoolSettingsDefaults`, `TestPoolSettingsFromMap` (every key and its parse), `TestAxesContainsPoolEntries` (the dictionary lists all sixteen with their defaults), `TestNextEvaluationFromFlag` (a `first_eval_utc` different from 03:00 changes the slots).
- [ ] **Step 2: Run tests to verify they fail.**
- [ ] **Step 3: Implement.** Settings decoding follows the pattern already used by `internal/transport/direct.go` for its `cfg map[string]any`; the two control routes sit inside the existing token-checked mux; `pool` prints the persisted state as text and `--json` prints it raw, matching the other read commands.
- [ ] **Step 4: Run tests and the full suite.**
- [ ] **Step 5: Commit** — `feat(pool): configure, wire and expose the pool`.

---

### Task 10: TUI surface

**Files:**
- Modify: `internal/tui/tui.go` (`Observation` gains `Pool *PoolView`, `PoolView{State, Active, ExitIP, RTT, Relevant, Quarantined, Progress, NextEval}`)
- Modify: `internal/tui/sections.go` (pool tile group)
- Modify: `internal/tui/dashboard.go` (render + notes when the pool is empty)

**Interfaces:**
- Consumes: `pool.Snapshot` (Task 6) through the data source in `up.go`.
- Produces: a `pool` tile group and a notes line, both width-aware like the existing sections.
- [ ] **Step 1: Write the failing test** — a render test at 100 and at 60 columns asserting the tile carries state, active node and `next eval`, and that an empty pool produces the note sentence and no empty tile.
- [ ] **Step 2: Run test to verify it fails.**
- [ ] **Step 3: Implement** following the existing tile helpers in `sections.go`; no new styles, use the semantic palette already there.
- [ ] **Step 4: Run test to verify it passes** and `CGO_ENABLED=0 go test ./internal/tui/ -count=1`.
- [ ] **Step 5: Commit** — `feat(pool): show the pool in the dashboard`.

---

### Task 11: Full cycle and live verification

**Files:**
- Modify: `README.md` (a `pool` section: what it does, the three schedule slots, the empty-pool contract)

- [ ] **Step 1:** `gofmt -l .` → empty; `CGO_ENABLED=0 go vet ./...` → clean; `CGO_ENABLED=0 go test ./... -count=1` → all green.
- [ ] **Step 2:** `CGO_ENABLED=0 go build -ldflags "-X main.buildVersion=$(git rev-parse --short HEAD)" -o /tmp/free-ai-router ./cmd/free-ai-router && cp /tmp/free-ai-router ~/.local/bin/free-ai-router`.
- [ ] **Step 3:** live run with the three subscriptions: `/tmp/free-ai-router up --listen 127.0.0.1:8795 --transport pool`; poll `/tmp/free-ai-router pool` until the relevant list is non-empty (up to 3 min); record the observed counts in the commit message.
- [ ] **Step 4:** `curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:8795/v1/models` → 200; `curl -s http://127.0.0.1:8795/_control/status` shows the node's exit address as the egress address (compare with `/_control/pool`).
- [ ] **Step 5:** find the active core (`pgrep -f 'mihomo.*pool'`), `kill -9` it, then `curl … /v1/models` → still 200 and `/_control/pool` reports a different active node.
- [ ] **Step 6:** empty pool: restart with `--pool-subscriptions=` (empty) → `curl /v1/models` → 503 with `"type":"pool"` and the evaluating message; TUI (or `/_control/pool`) shows the same sentence.
- [ ] **Step 7:** commit — `feat(pool): document and verify the pool transport`, with the measured numbers in the body.

## Self-Review

Spec coverage: §4 core choice → Task 4 (+ table in Task 4's `Supports`); §5 layout → the file list across tasks; §6 model → Task 1; §7 fetch/parse → Tasks 1–2; §8 pipeline → Tasks 3–5, 7; §9 rotation → Task 8; §10 empty pool → Tasks 8, 10; §11 schedule → Task 7; §12 persistence → Task 7; §13 config/axes → Task 9; §14 surfaces → Tasks 9, 10; §15 egress wiring → Task 9; §16 testing → every task plus Task 11; §17 risks → the live check in Task 11 is what turns each risk into a measurement.

Every Review Focus line has an owner: HTML body → Task 2; IPv6 literal → Tasks 1, 3; exit address drift → Task 6 (`Replace` re-measures, `Suspect` needs `deadThreshold`, not one miss); core that rejects its own config → Task 4 (`Start` fails, Task 8 keeps the old tunnel); unreadable `pool.json` → Task 7.
