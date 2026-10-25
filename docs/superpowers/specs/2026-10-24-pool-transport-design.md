# Pool transport (`transport = "pool"`)

Status: draft for review
Date: 2026-10-24

## 1. Problem

The provider refuses a request with an ADDRESS-level refusal, not a credential
one. Measured wording, already classified in `internal/provider/opencode/classify.go`
as `freeLaneRefusal`:

```
detected abusive traffic
free models aren't available from your current network
can only be used from within opencode
```

The refusal is about the source address, so the only way past it is to change the
source address. A VPN on the host changes it for the whole machine and for every
provider at once; it also gives one address, so the moment that address is
flagged the router has nothing to fall back to.

The router already has the right axis for this: `transport` answers "where does
the traffic leave from". Pool mode makes that axis mean "leave from a rotating
set of third-party servers, chosen by measurement".

## 2. Goals

1. Build a set of usable servers from the proxprobe pass over subscriptions, not
   from hand-written config.
2. Test each candidate for what actually matters — is this address acceptable to
   the provider — not merely "is the port open".
3. Keep a ranked list of usable servers and rotate between them without the
   gateway losing a request or inventing a retry policy.
4. Re-evaluate on a schedule tied to quota reset, and on demand when everything
   in the list has gone bad.
5. Show the state of that machinery in the project's own surfaces (TUI, control
   API, CLI) — including the "nothing usable yet" state, which must not look like
   a hang.

## 3. Non-goals

- No general-purpose VPN client. Only HTTP(S) proxying for provider traffic: the
  router makes no other kind of connection.
- No subscription parsing of our own: proxprobe owns the formats (share links, clash
  yaml) and the pool reads only its artifacts. A format proxprobe cannot read is
  its business, and the pool never sees a half-parsed list.
- No rules, GeoIP databases, DNS tricks, or per-site routing inside the core. One
  node per core process, one rule (`MATCH,node`).
- No per-country preference, no latency-based auto-testing inside the core. The
  selection is the router's job.
- No new retry policy. See §9.

## 4. Core selection — measured, not assumed

Three candidates exist in nixpkgs with the protocols these lists carry. Versions
were read from nixpkgs, coverage was checked with each core's own config
validator, not from documentation.

| Property | mihomo 1.19.24 | sing-box 1.13.19 | xray 26.3.27 |
| --- | --- | --- | --- |
| vless: no transport / ws / grpc / http / httpupgrade | yes | yes | yes |
| vless + reality | yes | yes | yes |
| vless + xhttp | yes (`network: xhttp`) | no | yes |
| vmess / ss / trojan | yes | yes | yes |
| hysteria2 / tuic | yes | yes | no |
| ssr | yes | no | no |

Evidence:

- `sing-box check` on a vless outbound with `"transport":{"type":"xhttp"}` →
  `FATAL decode config: outbounds[0].transport: unknown transport type: xhttp`
  (same for `raw` and `tcp`; those share-link types mean "no transport block" in
  sing-box).
- `mihomo -t -f` on vless+xhttp, vless+grpc, hysteria2 and ss →
  `configuration file ... test is successful`.
- `sing-box check` on reality vless, ws vless, grpc vless, vmess+ws, ss and
  trojan outbounds → exit 0. The shapes this spec generates are accepted.

What the real subscriptions contain (fetched, 619 lines across the three URLs):

| Scheme | Count |
| --- | --- |
| vless | 412 |
| vmess | 149 |
| ss | 33 |
| trojan | 26 |

Within vless: `security` reality 201, tls 152, none 36 (2 lines carry no
`security` key at all); `type` ws 173, tcp 118, raw 74, grpc 24, xhttp 16,
httpupgrade 1. Unique `host:port` pairs: 382 out of 619 lines — the lists overlap
heavily, so deduplication is not optional.

**Decision: mihomo is the default core.** It is the only one of the three that
covers the full set, including the 16 xhttp nodes that sing-box rejects outright.
Measured on this host: RSS 33 MB per process, listening socket ready in under
100 ms.

**Decision: the core is behind an interface and is not load-bearing for the
architecture.** The pool reduces a core to one operation: *take one share link,
produce one local proxy on the port I name*. Nothing else about a core is used
(`pool.Core` in §5). A second core is then a second renderer, not a second
subsystem, and a protocol table decides per node which renderer applies.

Rejected alternative, recorded because it was the obvious one: a single long-lived
core holding all nodes plus a `select` group, switched by `PUT /proxies/<group>`.
Both mihomo and sing-box implement it and it works (measured: `204` and the
`now` field changes; `PUT /configs` reloads with `204`). It was rejected anyway:
it pins the pool to one core's API surface and to one core's protocol coverage, so
the 16 xhttp nodes and any future hysteria2 node would be unusable without a
second mechanism. The process-per-node model costs one process spawn per switch
and buys complete independence from the core's control API.

## 5. Where the code lives

```
internal/pool/            new subsystem
  node.go                 Node: the pass record plus its clash profile
  proxprobe.go            reading the pass artifacts, SourceProxprobe
  evaluate.go             the evaluation pipeline (§8)
  probe.go                provider stage
  registry.go             ranked list, cursor, quarantine
  runner.go               Core: one node, one process, one port
  core_mihomo.go          mihomo config rendering
  switcher.go             http.RoundTripper with atomic tunnel swap
  store.go                pool.json
  transport.go            adapter implementing transport.Transport
internal/egress/          add exported Observe(); reuse for exit-IP
internal/provider/        add exported ApplyCredential(); reuse for the probe
internal/gateway/         recognise pool.ErrNotReady → 503 (three lines)
internal/axes/            the new axis entries
internal/tui/             the pool tile and the empty-pool note
internal/control/         the pool snapshot
cmd/free-ai-router/       wiring, `pool` read command
```

Dependency direction, which is the point of the split:

```
internal/pool      → internal/provider, internal/egress, internal/transport (iface only)
internal/transport → (nothing provider-related, unchanged)
cmd/…              → internal/pool  (registration into the transport registry)
```

`internal/transport` must not learn about providers, so the provider-aware probe
is injected into the pool from `cmd/`, where the provider registry already lives.

The core interface:

```go
// Core turns one node into one local proxy. Nothing else about a core is used:
// no groups, no control API, no reload. Keeping the surface this small is what
// makes the core replaceable and what keeps the pool independent of one
// project's API decisions.
type Core interface {
    // ID is the core's name in configuration.
    ID() string
    // Supports reports whether this core can render the node at all.
    Supports(n Node) bool
    // Config renders the core's configuration for one node on one port.
    Config(n Node, port int) ([]byte, error)
}
```

## 6. Data model

```go
type Node struct {
    ID        string // sha256(proto|host|port|uuid|params)[:16]
    Proto     string // vless|vmess|ss|trojan|hysteria2|tuic
    Transport string // ws|grpc|http|httpupgrade|xhttp|""
    Security  string // reality|tls|none
    Host      string
    Port      int
    SNI       string
    Label     string // decoded #fragment, display only
    Raw       string // the clash profile verbatim: the source of truth for rendering

    ProbeRTT  time.Duration
    ExitIP    string
    ExitGeo   string // the flag from the label, display only
}
```

`Raw` is kept because rendering is the core's business and a lossy intermediate
representation would eventually lose a field the core needs.

## 7. The source: a proxprobe pass

The pool does not fetch, parse, dedupe, ping or sort. A pass does that
(`proxprobe all`), and its artifacts are the contract:

- `merged.jsonl` — one node per line: `id, proto, name, server, port, uuid,
  network, security, sni, source[]` plus `raw.clash`, the core's own profile.
- `http.jsonl` — the pass's own GET verdict per node: `delay_ms`, `ok`, `error`.

Read rules:

- A node enters the candidate list only when its `http.jsonl` record is
  `ok: true`. A repeated id takes the last verdict: an artifact appended to by an
  interrupted pass must not resurrect a server the pass later failed.
- The profile is kept verbatim and handed to the core as it is. Every field the
  parser does not know (xhttp options, packet-encoding, reality spellings) is
  therefore not a loss. A node whose profile the core cannot render is counted as
  unrenderable and named in the log, never silently dropped.
- Freshness is the older of the two files' mtimes. A pass older than
  `proxprobe_max_age` (default: one `interval`) is refused with the age and the
  limit, and the current list keeps serving: a stale pass describes servers
  measured before the last rotation.
- The daemon runs the pass itself when `proxprobe_command` is set — an argv, not
  a shell line — and otherwise trusts an outside scheduler. With no command and
  no artifacts the evaluation fails with `proxprobe has no merged.jsonl in
  <dir>: run proxprobe all first`.
- Counts, never secrets: the log carries parsed/http-ok/unrenderable, and the
  pass's own log tail on failure.

The pool therefore has exactly one hard dependency it does not install:
`proxprobe` on `PATH` (plus the core it starts). The NixOS module supplies both
through `poolPackages` and `pkgs.mihomo`.


## 8. Evaluation pipeline

Three stages, each of which can only shrink the candidate set: the pass has
already removed the cheap failures, so every remaining candidate costs a process
and a provider request.

```
read      merged.jsonl ∩ http.jsonl ok:true      → ~398 candidates
admit     core per node, 6 workers, one minimal completion, 12s timeout
          + exit-IP observation                  → accepted + RTT + ExitIP
publish   dedupe by exit network, sort by probe RTT, cap at max_nodes
```

**Admission stage.** For each candidate, in the pass's order, at most
`max_probe` candidates (0 = all):

1. Start a `Core` for this node on a free loopback port (§9 for the port rule).
2. Observe the exit address through that tunnel by asking
   `https://1.1.1.1/cdn-cgi/trace` with the tunnel's round tripper — the same
   observation `internal/egress` already performs, exported as `egress.Observe`.
   This is what "does this address have problems" means concretely, and it is
   also the key for deduplication and quarantine: two nodes behind one exit
   network are one budget (/24, /64 — see §8, Promotion).
3. Send ONE minimal completion through the tunnel. The request is built by
   `provider.Build` — the wire contract is the provider's, not the pool's — with
   `Stream: true`, `MaxTokens: 1`, one short user message, on the cheapest free
   model of the catalog (`probe_model` overrides).
4. Judge the answer with `provider.Classify`:
   - 2xx → accepted.
   - `freeLaneRefusal` → **the address is refused**. Reject the node; this is the
     whole point of the stage.
   - any other non-2xx (auth, quota, unavailable) → the node is alive and the
     address is not the problem; keep it, but record why, because a node whose
     probe was inconclusive should rank below one that answered.
5. Stop the core.

**The probe must be the client's request, down to the handshake (measured
2026-10-24).** The lane admits the reference client's TLS ClientHello and only
`/inference/openai/v1/chat/completions`; a plain `crypto/tls` handshake or the
legacy `/inference/v1/chat/completions` is answered `429 FreeUsageLimitError`
whatever the address is. A probe of the wrong shape therefore rejects every node
with "the free quota of this address is spent" — the exact failure it exists to
detect, reported for a reason that has nothing to do with the address. So the
pool builds no HTTP client of its own: it takes `transport.ClientTransport` (the
same uTLS hello the daemon serves with) and `provider.Build`, and reaches the node
through a CONNECT dialer (§9).

The probe reuses the gateway's credential rules through
`provider.ApplyCredential(p, prepared, key)`. The header name, the prefix and the
anonymity marker are properties of the provider, so the helper moves from
`internal/gateway` into `internal/provider` and the gateway's method delegates to
it — the direction matters, since `internal/pool` may not import
`internal/gateway`. In anonymous mode the key is empty and the provider
substitutes its anonymity marker exactly as it does for a client request.

**Promotion.** Accepted nodes are deduplicated by exit NETWORK — /24 for IPv4, /64
for IPv6 — and the best probe RTT of a network wins. Sorted by probe RTT and
truncated to `max_nodes`, that list is the relevant list. The property being
maximised is the number of independent allowances, and five servers behind one
hosting pool have the allowance of one: the provider counts per address, but the
pool it hands out is burned as a whole. The same key is used on the way out: a
refusal for one address drops the network's already-accepted nodes, and any node
of that network that answers later in the same run is dropped as well — the
allowance it would draw on is already gone.

Cost on the measured data (2026-10-25): 398 candidates → 398 probes, 145 s wall
clock with 6 workers, 0 accepted — the pass had done its job, the provider
refused every address, and the report says so per address (`336 of 398` were a
spent free quota). Peak memory stays around 200 MB for six core processes.

**Evaluation is never destructive.** It builds a new list and swaps it in at the
end. While it runs, the previous list keeps serving. A crash mid-evaluation leaves
the previous list on disk untouched.

## 9. Runtime: rotation

One tunnel = one core process on one loopback port + the node it serves.

```go
type tunnel struct {
    node  Node
    port  int
    cmd   *exec.Cmd
    proxy *url.URL  // http://127.0.0.1:<port>
}
```

`switcher` holds `atomic.Pointer[tunnel]` and returns a round tripper built by
`transport.ClientTransport` whose dialer reads that pointer. It does **not** use
`Transport.Proxy`, for two measured reasons:

> With `Proxy` set, `net/http` asks `hasCustomTLSDialer()` **before** it sends the
> CONNECT, and `cm.addr()` is the **proxy's** address
> (`$GOROOT/src/net/http/transport.go`, `dialConn`). `DialTLSContext` would then
> receive `127.0.0.1:<port>` as the server name, and the ClientHello would name
> the tunnel instead of the provider. The lane reads that hello (§8), so this is
> not a cosmetic difference.
>
> The lane admits only the reference client's TLS shape, so a plain
> `crypto/tls` dialer is answered `429` whichever node sits behind it. The pool
> therefore dials with the provider's own client transport and speaks the CONNECT
> itself inside `DialContext`.

Rotation still reaches the request: the dialer reads the tunnel of the moment, and
`CloseIdleConnections` runs on every swap. That call is load-bearing — the
transport keys idle connections by **target host**, not by tunnel, so without it a
new request could be handed a socket through the old server. A connection that
goes idle after the swap and before the old core stops may still use the old
tunnel during `grace`; that is accepted, and it is why the old core is not stopped
at once.

Unverified: that an already-open streaming response survives the swap in
practice. It should, because the swap does not touch open connections, but this
was not measured and the plan must test it rather than assume it.

**Switch procedure.**

1. Pick the next node from the relevant list (cursor order, skipping quarantined).
2. Allocate a free port, render the config, start the core, wait for the port to
   accept a connection (5 s budget).
3. Swap the pointer, call `CloseIdleConnections`.
4. After `grace` (30 s), stop the previous core: `SIGTERM`, then `SIGKILL` after
   2 s, and reap the child.

If the new core does not become ready, the pointer is not swapped: the old tunnel
keeps serving and the node is quarantined. A failed switch must never take the
router down.

**Port allocation.** The pool owns the range `38200–38399`, tries the lowest free
port by binding `127.0.0.1:0`, closing, and passing the number to the core. The
bind-then-release window is a race against nothing else on a personal host; the
alternative — parsing the core's own port report — does not exist for a fixed
`mixed-port`. Documented as a trade-off, not an oversight.

**A failed request is the only signal from the far end.** The health loop sees the
core, not the node: a core that keeps listening while its server stopped
forwarding looks perfectly healthy. So the round tripper reports transport errors
to the switcher, which moves to the next node — and one tunnel asks for one
switch, because the gateway alone makes three attempts and each of them would
otherwise burn a node. A response, whatever its status, is not a tunnel failure:
it means the node carried the request there and the answer came back.

**Failure taxonomy.**

| Signal | Where it is seen | Reaction |
| --- | --- | --- |
| dial / TLS / reset before headers | switcher's round tripper | node suspect, one switch per tunnel, the error returned unchanged |
| provider went silent (no first event) | gateway watchdog | request aborted with `504` after `silenceAttempts`; the node is **not** moved — a silent provider cannot be told apart from a silent path, and quarantining the list on it would empty the pool for hours |
| refusal classified `freeLaneRefusal` | gateway classify | the refusal reaches the client; the pool does not see it, and the address is dropped by the next evaluation, which is where the address is judged |
| health check of the active node fails | pool's own loop, every 30 s | switch, before a client pays for it |
| `dead_threshold` failures with no success in between | registry | quarantine until the next evaluation |

The gateway counts what happened exactly as it does today: these become transport
aborts (`recordAbort`), not provider refusals, because a broken path is not the
provider refusing.

**Quarantine** is a per-node status, not deletion: the node keeps its measurements
and is retried at the next evaluation, which is the only thing allowed to clear it.

## 10. Empty pool contract

When the relevant list is empty (nothing evaluated yet, everything quarantined,
or the pass left nothing usable):

- `switcher.RoundTripper` returns `pool.ErrNotReady`.
- The gateway recognises that error (three lines in the transport-failure branch)
  and answers:

```json
{"error":{"code":"pool","type":"pool",
  "message":"the pool has no usable server yet: 0 of 398 servers accepted; the free quota of this address is spent; next check at 2026-10-25T19:00:00Z"}}
```

with `Retry-After: 30`. The number is real: it is the progress of the running
evaluation, so the operator sees a job in progress instead of a hang. A stray
`502 transport is unreachable` here would be a lie — nothing was unreachable, the
pool simply had nothing to send through.

- An out-of-band evaluation starts immediately, throttled to `reeval_min_gap`
  (10 min) so a failing pass cannot turn into a permanent evaluation
  loop.
- While the pool is empty the TUI carries the same sentence in its notes band.

## 11. Schedule

Three evaluations per day, the first after the quota reset:

```
03:00 UTC   (06:00 MSK)
11:00 UTC   (14:00 MSK)
19:00 UTC   (22:00 MSK)
```

Implemented as "the next instant among {03, 11, 19} UTC" computed from the wall
clock, persisted as `next_eval_at` so a restart does not re-run an evaluation it
just ran. No cron expression, no new dependency. An evaluation also runs at
startup when the stored list is empty or older than 26 hours (one missed slot),
and on demand via the control surface.

Two evaluations never overlap: a running evaluation makes a new request wait, and
its result is the one that lands.

## 12. Persistence

`$XDG_STATE_HOME/free-ai-router/pool.json` (next to `state.json`):

```json
{"version": 1,
 "evaluated_at": "2026-10-24T03:00:12Z",
 "next_eval_at": "2026-10-24T11:00:00Z",
 "eval_state": "idle",
 "nodes": [{"id": "…", "proto": "vless", "host": "…", "port": 443,
            "raw": "vless://…", "exit_ip": "…", "probe_rtt_ms": 812,
            "status": "active"}],
 "quarantined": [{"id": "…", "reason": "pool: address refused", "at": "…"}]}
```

Written atomically (temp file + rename) after every evaluation and after every
quarantine change, so a `kill -9` cannot leave a half-written pool. Share links
are public data; no secrets are written.

## 13. Configuration and axes

All keys live under `transport_settings.pool`, so the existing `transport` axis
mechanism carries them with no new top-level config keys. Each gets an entry in
`internal/axes` (`ScopeGlobal`, `ConfigKey: transport_settings.<key>`), a flag
`--pool-*`, and an env name `FREE_AI_ROUTER_POOL_*`, following the file →
environment → flag precedence that already exists.

| Key | Default | Meaning |
| --- | --- | --- |
| `source` | `proxprobe` | the only source: anything else is refused at startup |
| `proxprobe_workdir` | `""` (required) | where the pass leaves `merged.jsonl` and `http.jsonl` |
| `proxprobe_command` | `[]` | argv of the pass the daemon runs itself; empty means an outside scheduler |
| `proxprobe_max_age` | `interval` | oldest accepted pass; older keeps the current list |
| `pass_timeout` | `10m` | budget for one pass run |
| `core` | `mihomo` | which core renders nodes |
| `core_path` | `""` | explicit binary path; empty means look up on `PATH` |
| `first_eval_utc` | `03:00` | first daily evaluation |
| `interval` | `8h` | spacing between evaluations |
| `probe_timeout` | `12s` | budget for one provider probe |
| `probe_workers` | `6` | concurrent core processes during evaluation |
| `max_probe` | `60` | candidates admitted, 0 = all |
| `probe_model` | `""` | empty means the first free model of the catalog |
| `max_nodes` | `20` | size of the relevant list |
| `health_interval` | `30s` | liveness check of the active node |
| `dead_threshold` | `2` | consecutive failures that quarantine a node |
| `reeval_min_gap` | `10m` | throttle for out-of-band evaluation |
| `grace` | `30s` | how long a replaced core keeps running |

```json
{
  "transport": "pool",
  "transport_settings": {
    "pool": {
      "source": "proxprobe",
      "proxprobe_workdir": "/var/lib/free-ai-router/proxprobe",
      "proxprobe_command": [
        "proxprobe", "all",
        "-config", "/etc/free-ai-router/proxprobe.yaml",
        "-workdir", "/var/lib/free-ai-router/proxprobe",
        "-core", "mihomo",
        "-target", "best"
      ],
      "proxprobe_max_age": "8h",
      "max_probe": 0,
      "max_nodes": 5
    }
  }
}
```

`family` (`auto`/`v4`/`v6`) stays parsed and validated but stops affecting model
traffic under pool: the family of the exit is whatever the node provides, and
the router cannot choose it. The egress resolver reports the exit address it
observes, which is the honest answer. Flagging the axis as inert in `axes` output
is part of the work; silently ignoring it is not allowed.

## 14. Surfaces

**TUI.** A `pool` tile group in the dashboard, next to the existing provider
tiles:

```
pool        evaluating  37/120        active  🇮🇹 45.86.159.134:2085 vless/grpc
            ready in 41s                       exit 194.36.37.59  832ms
            relevant 18  quarantine 4          next eval 22:00 MSK
```

While the pool is empty, the notes band carries
`pool: no usable server yet — evaluating 37 of 120`. The dashboard must not show
an empty tile with no explanation.

**Control API.** `GET /_control/pool` returns the persisted snapshot plus live
state (active node, evaluation progress, next evaluation), shaped like the rest of
`control.Status`. `POST /_control/pool/recheck` triggers an evaluation and answers
with the same snapshot. Both sit behind the existing token check in server mode —
they spend the operator's quota and open outbound connections.

**CLI.** `free-ai-router pool` prints the persisted snapshot and works with the
daemon down (the read commands deliberately work locally). `free-ai-router pool
recheck` calls the control surface of the running daemon.

## 15. Egress wiring fix

`cmd/free-ai-router/up.go` currently builds the egress resolver with
`transport.Pinned(family)` — a **fresh direct transport**, i.e. the observed
address is the host's, not the path's, and in pool mode that is simply wrong. The
resolver must be handed the configured transport's round tripper, which for pool
mode is the switcher: then the reported address is the active node's exit address,
and it changes when the node changes. This is a defect in the existing wiring, not
a pool feature; pool mode only makes it visible.

## 16. Testing

Unit, at the seams:

- `proxprobe.go` against fixtures cut from a real pass: merged records with and
  without `raw.clash`, a proto the core does not render, a repeated id whose last
  verdict is `ok: false`, an http record with no merge counterpart.
- the profile is handed to the core verbatim: an xhttp node keeps its
  `xhttp-opts`, a reality node its public key and short id, and nothing is
  re-derived on the way.
- a stale pass is refused and leaves the current list in place; a missing
  `merged.jsonl` is the empty-pool error, not a panic.
- dedupe by exit network (same /24 wins once, the other does not), the refusal that
  burns its whole network, ranking order; the `max_nodes` cut.
- schedule: next instant for each wall-clock case including a restart just after
  an evaluation.
- quarantine transitions and the cursor's skip behaviour.
- switcher with fake tunnels: swap changes the proxy URL; failed start does not
  swap; the old core is killed after the grace period.

Integration, on this host:

- `store.go` round trip, atomic write surviving a truncated file.
- No test may hit a real provider or a real `proxprobe` in `go test`; the wiring
  is exercised by the live check instead.

Live check, which is the actual acceptance:

1. `CGO_ENABLED=0 go build`, `gofmt -l .` empty, `go vet ./...`, `go test ./...`.
2. `/tmp/free-ai-router up` with `transport_settings.pool` pointing at a finished
   pass; wait for `/_control/pool` to report a non-empty relevant list.
3. A chat completion through the tunnel → 200 with a streamed body, and
   `/_control/status` shows the node's exit address as the egress address.
   `/v1/models` does not prove it: the catalog is answered from memory, with or
   without a tunnel.
4. `kill -9` the active core process → next request still 200, now through the
   next node (address in `/_control/status` changes).
5. Empty the pool (`max_nodes: 0`, or an empty work directory) → 503 with the
   message from §10, and the TUI shows the evaluating note.

## 17. Risks and unverified assumptions

- **mihomo's xhttp support is validated at the config level only.** `mihomo -t`
  accepts it; that it actually tunnels traffic is unverified until the live check
  measures a node from the real xhttp group. If it fails, those 16 nodes drop out
  by measurement and the pool still works — which is exactly why the probe exists.
- **Core-to-core protocol differences** are handled by a table, not discovered at
  runtime. If a node renders into a config the core rejects, the node is
  quarantined with the render error attached; the operator sees it, and the
  evaluation continues.
- **The share of candidates that survive the provider probe is unknown** — the
  pass was measured, the provider's answer was not (2026-10-25: 0 of 398). If the
  survivors are few, `max_probe` and `max_nodes` are the knobs, and the honest
  consequence is a small pool, not a silent expansion of concurrency.
- **In-flight behaviour across a swap** is argued from Go's pooling, not measured
  (§9).
- **The provider's free lane may impose per-address limits tighter than one
  request**, in which case a pool of many nodes behind few addresses has fewer
  usable slots than its length suggests. The exit-network dedupe is the mitigation;
  the real number is only learnable by running it.
- **Quota cost**: one minimal completion per candidate, 60 per evaluation, three
  evaluations per day. Small but not zero, and it is spent on the operator's
  behalf.

## 18. One contract addition

`provider.Failure` gains one boolean, `AddressRejected`. The pool's promotion
criterion is "the provider refused this SOURCE ADDRESS", and today that judgement
exists only inside the opencode classifier, reachable by the provider itself. The
provider is the only party that can make that judgement — the same status carries
different meanings at different services, which is the same argument the package
already makes for `KeyRejected` and `Retryable`. The field is set where
`freeLaneRefusal` matches and is inert for every other consumer, so the gateway
behaviour is unchanged.

**What a slot does, and what it does not do.** A slot re-evaluates: it runs the
pass (or reads the one an outside scheduler left), measures, re-ranks and
replaces the list. The serving node is kept if
it is still on the new list and still passes a health check (`applyList`); it moves
only when it stopped working. This follows from the measured allowance: the free lane
counts requests per **egress network per day**, reset at 00:00 UTC, so moving at
11:00 to another address of the same day buys no fresh allowance and costs an
interruption — and a refusal burns the whole network anyway.
The three slots exist to keep the list fresh, not to spend three addresses a day. If
the intent is a different exit at every slot regardless of health, `applyList` is the
one place to change, and the cost is that a stream open at the slot may be cut.
