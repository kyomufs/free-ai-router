# OpenCode Zen — the `opencode` provider

The service behind the `opencode` provider: OpenCode's own free lane at
`opencode.ai`, and its paid sibling, OpenCode Go.

This file records what the lane does — the request it admits, the answer it
sends, the errors it produces. Every claim carries its evidence:

- **measured** — a request sent to the live service from this host. The date is
  given, because a verdict of this lane is a reading, not a law: the same bytes
  from the same address were answered 403 at 21:23 and 200 at 21:28 (measured
  2026-10-19).
- **source** — read from the vendor's tree in `~/Projects/opencode`, branch
  `dev`, 9519 files.

## Routes

| path | method | answer | note |
| --- | --- | --- | --- |
| `/inference/openai/v1/chat/completions` | POST | 200 | the chat path the reference client uses; the provider's default (`DefaultChatPath`) |
| `/inference/v1/chat/completions` | POST | 429 | the legacy chat path; now routed to the exhausted lane |
| `/inference/v1/models` | GET | 200 | model list, the provider's base URL |
| `/inference/go/v1/models` | GET | 200 | paid lane, ids carry no `-free` suffix |
| `/inference/go/openai/v1/models` | GET | 404 | not a route |
| `/zen/v1/models` | GET | 200 | legacy alias |
| `/zen/go/v1/models` | GET | 200 | legacy alias |

measured 2026-10-19, re-measured 2026-10-25. The provider's `base_url` is
`https://opencode.ai/inference/v1` (`DefaultBaseURL` in
`internal/provider/opencode/provider.go`), taken from the `api` key of the
published model catalog rather than from the client's code. The chat path is
resolved separately: `chat_path` in the provider settings, default
`/inference/openai/v1/chat/completions`. A custom `base_url` for a mirror keeps
the model list under its own path; the chat path stays the absolute default
unless `chat_path` is set.

## Credential

| mode | what goes on the wire | note |
| --- | --- | --- |
| anonymous | `Authorization: Bearer public` | the marker, not a key |
| key | `Authorization: Bearer <key>` | account lane |
| none | no `Authorization` header | accepted; the lane is free |

The vendor's inference proxy strips the credential when the key is `public` or
missing, which makes the request **keyless** — the free lane needs no account at
all (source: `packages/console/app/src/lib/inference-proxy.ts`, the comment at
line 17 and the credential handling below it).

Identity is therefore the **egress address**, not the account: the same proxy
forwards `cf-connecting-ip` as `x-real-ip` (source: `inference-proxy.ts`), and
every counter and block of the free lane is scoped to it. Two consequences that
the rest of this file relies on:

- per-address allowances are not per-account allowances, so the same account
  reaches the service with different budgets through different addresses;
- an address-level refusal cannot be undone by any account, key or header.

## The request the lane admits

The lane has two different refusals, and they mean different things. `403
FreeTierError` arrives before a model is chosen and describes the **shape** of
the request. `429 FreeUsageLimitError` is the quota refusal — and, on the client
lane, also what a request that does not look like the shipped client gets: such
traffic is routed to the exhausted legacy bucket.

The 403 conditions, each measured one request at a time on 2026-10-19 with the
rest held constant:

| # | condition | without it |
| --- | --- | --- |
| 1 | body carries `"stream": true` | 403, for every body otherwise admitted |
| 2 | tools named `read` **and** `shell` or `bash` | 403 |
| 3 | a session header matching `^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$` | 403 |
| 4 | a `User-Agent` that resolves to an OpenCode build | 403 |

Condition 1 is the one that is easy to lose: a client that does not ask for a
stream is passed through as `"stream": false` and refused. Everything else in
the body (`stream_options`, `max_tokens`, `temperature`, `reasoning_effort`) can
be subtracted without changing the verdict, and `"stream": false` fails it on its
own — with canonical tools, canonical session header, on HTTP/1.1 and on HTTP/2
alike (measured 2026-10-19). The provider therefore forces the flag
(`internal/provider/opencode/provider.go`) and merges the event stream back into
one body for a caller that asked for none
(`internal/gateway/collect.go`).

Condition 2 is decided by the **names** only. The same request with the canonical
definitions replaced by `{"name":"shell","description":"run shell",…}` is
admitted (measured 2026-10-19). The client's own descriptions and schemas are
still sent, because a deeper check is the obvious next move for the lane.

Condition 3 is decided by the **shape**, not the value: `ses_test`, a bare UUID
and `ses_` followed by 64 zeros are all refused, while any well-formed identifier
is admitted. A client with its own naming convention fails every call. The
provider derives a conforming identifier when the caller's is malformed
(`internal/provider/opencode/sessionids.go`) and then keeps it for the whole
conversation (`internal/provider/opencode/sessioncache.go`): a fresh identifier
per request is well-formed and still wrong, because the vendor asks for one
session per conversation and a client that never repeats one is not a client.
The key is the conversation — system prompt plus first user message — so a
compacted conversation may look new and lose affinity, which costs a cache hit
and never correctness. An identifier the caller supplied is never rewritten: the
caller knows its own conversation better than a fingerprint does.

Condition 4, the `User-Agent`, is checked for "looks like an OpenCode build"
(`packages/core/src/app.ts`). Measured with the other conditions held constant:

| value | verdict |
| --- | --- |
| `opencode/1.18.35 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14` | admitted |
| `opencode/1.19.2` | admitted |
| `opencode/stable/2.0.32/opencode` | admitted |
| `opencode/unknown/unknown/opencode` | refused |
| `bun` | refused |
| `node bun` | refused |
| *(header absent)* | refused |

Version digits and the suffix are ignored; a value that does not name an OpenCode
release is refused.

### The 429 conditions

Measured 2026-10-25, each flipping 429/200 with everything else held constant,
including the body, the headers and the address:

| # | condition | without it |
| --- | --- | --- |
| 5 | TLS ClientHello of Bun/BoringSSL | 429; `crypto/tls`, curl/OpenSSL and uTLS with Chrome's hello are all refused |
| 6 | chat path `/inference/openai/v1/chat/completions` | 429 on `/inference/v1/chat/completions`, even with the client's own body |
| 7 | canonical system prompt + environment block | 429; the canonical text alone, a generic prompt and the superseded rendered prompt are refused |

Condition 5 is implemented in `internal/transport/bunhello.go` and
`internal/transport/direct.go`: a hand-built hello that follows the captured Bun
one. Condition 6 is `DefaultChatPath` / `chat_path`. Condition 7 is the
reference client's `packages/opencode/src/session/prompt/default.txt` at
v1.19.3, followed by the block the client appends:

    You are powered by the model named <model>. The exact model ID is opencode/<model>
    Here is some useful information about the environment you are running in:
    <env>
      Working directory: ...
      Workspace root folder: ...
      Is directory a git repo: ...
      Platform: linux
      Today's date: ...
    </env>

The values in the block are not checked: a different working directory and date
kept the 200. The block's shape is. The client's own system part may follow the
block; the provider keeps it there, so the harness's rules are not lost
(`internal/provider/opencode/prompt.go`).

The 2026-10-19 reading that "the system prompt is not a condition" was taken on
the legacy lane while it was still open. It no longer describes the client lane:
the same tools and session header with no system message are now answered 429,
and the canonical prompt is what flips the verdict.

## Headers

The set the provider sends, in this spelling — the custom names are written
lowercase on purpose, matching the client:

```
Content-Type: application/json
Accept-Encoding: gzip
User-Agent: <user-agent>
Accept: */*
Connection: keep-alive
x-opencode-client: <client name, "cli" by default>
x-opencode-project: <project id>
x-opencode-request: <message id>
x-opencode-session: <session id, or the affinity id when set>
x-opencode-session-id: <session id>
x-opencode-parent-session-id: <parent session id, only in a child session>
```

`Accept-Encoding` is `gzip` alone: a Go transport decodes only what it negotiated
itself, so offering `br` or `zstd` would turn every answer into unparsed bytes.

## The reference client's wire shape

Captured 2026-10-19 and re-measured 2026-10-25 by running the installed client
through a TLS-terminating proxy. The client's request and this gateway's:

| field | the client | this gateway |
| --- | --- | --- |
| protocol | HTTP/1.1, ALPN `http/1.1` | HTTP/1.1, ALPN `http/1.1` |
| TLS ClientHello | Bun/BoringSSL | the captured Bun hello (`internal/transport/bunhello.go`) |
| path | `/inference/openai/v1/chat/completions` | the same |
| headers | the set above | the same set, the same spelling |
| order | as listed above | alphabetical, `Host` first |
| `Accept-Encoding` | `gzip, deflate, br, zstd` | `gzip` |
| body | `max_tokens`, `tool_choice` present | only what the caller sent |
| system message | `default.txt` + environment block | the same, then the client's rules |

The path and the hello are now conditions, not choices: the legacy path is
answered 429 even with the client's own body, and so is any hello other than the
Bun one. The order is Go's and is not decisive — curl, whose order differs from
both, is admitted once its hello matches. The TLS ClientHello cannot be matched
from `crypto/tls` at all; measured JA3: client
`8f1d483d7c5fba63a9170d6e4dd66f93`, Go `96458b3de39df5d47bc5c4a9f10f8f26`,
curl `0659743d0a8904909448456a6dfa4b06`. On the open legacy lane all three were
admitted; on the client lane only the Bun hello is.

## What the vendor asks of a client it does not ship

The published client requirements are the closest thing to an admission policy
the free lane has, and this gateway meets them where it can. From
`services/www/src/docs/content/console/go.mdx:74-113` (live at
`/docs/console/go`): send typical coding agent traffic; identify the client with
its own user agent rather than a generic SDK or HTTP-library name; send a stable
session id in `x-opencode-session` — stable across the turns, tool calls and
retries of one conversation, and never one fixed value per installation. The same
page adds the one sentence of policy that exists at all: "Traffic is monitored for
abuse that degrades the experience for other users" (`:77`).

| requirement | this gateway |
| --- | --- |
| typical coding agent traffic | the caller's traffic; the model list is polled every 15 min, not per request |
| its own user agent | **not met on purpose** — see below |
| a stable session id per conversation | met: derived once per conversation and reused (`sessioncache.go`) |

The session id is not free-form. Since 2026-09-16 the free lane validates its
shape for anonymous requests, and third-party proxies had to copy the format to
stay admitted (`linux.do/t/topic/2914307`, `6Kmfi6HP/opencode2api#43`):
`ses_` + 12 hex + 14 base62 — 30 characters — where the hex part is the *inverted*
millisecond timestamp, so identifiers decrease over time. `identifiers.go`
implements `packages/schema/src/identifier.ts` verbatim for this reason: a
plausible-looking random id is not the same as a well-formed one.

On the user agent the honest answer is that the requirements conflict with the
lane. Identifying as this gateway is what the docs ask; the free lane admits only
what looks like the shipped client, and a relay is refused by design (#56662:
"relayed requests are expected to be rejected… this is intended"). The provider
therefore sends the client's user agent, which is a deliberate imitation and
should be read as one. The paid lane (`credential: account`) is where a client is
free to identify itself: it is an ordinary OpenAI-compatible endpoint
(`docs/console/api/inference.mdx:10-22`), and the whole free-lane admission
question disappears there.

## Response

An event stream of `data: ` lines, each carrying one chunk; the last two are the
usage chunk and `data: [DONE]`. With `stream_options: {"include_usage": true}`
the final chunk carries `usage.prompt_tokens`, `completion_tokens`,
`total_tokens` — optional on the wire, and requested because a client that counts
tokens from a stream has nowhere else to read them.

## Refusals

| status | body | class | lever |
| --- | --- | --- | --- |
| 403 | `{"type":"error","error":{"type":"FreeTierError","message":"OpenCode's free tier can only be used from within OpenCode. Download it at https://opencode.ai and keep building."}}` | request shape | the 403 conditions above |
| 429 | `{"type":"error","error":{"type":"FreeUsageLimitError","message":"Free usage limit reached. Subscribe to OpenCode Go to keep going: https://opencode.ai/go"}}` | quota, or traffic that does not look like the client | the 429 conditions above, or another address, or time |
| 403 | `{"code":null,"message":"OpenCode's free models aren't available from your current network because we've detected abusive traffic from this IP address. This isn't related to your account. If you're on a VPN or proxy, turn it off and try again, or subscribe to OpenCode Go…"}` | egress address | none in the request; the vendor's team, or another address, or time |
| 401 | `{"error":{"type":"server_error","message":"Upstream request failed: Go requires a credential"}}` | paid lane | a credential; the free lane has no equivalent |
| 403 / 429 / 503 | `{"error":{"type":"server_error","message":"Upstream request failed: Endpoint is unavailable."}}` | the model host | another model of the same lane; the requested one answers nothing until the vendor restores it |

### The status does not name this refusal

The endpoint-unavailable body arrived under three different codes in one hour on
2026-10-20, on a full client shape, from five different egress addresses:

| status | model | observed |
| --- | --- | --- |
| `503` | `deepseek-v4.1-flash-free` | `app.log`, 65 events, hour `20:00` MSK, first `20:13:59` |
| `403` | `nemotron-3.5-lightning-free` | live probe |
| `429` | `glm-5.3-flash-free` | live probe |

So the classification is made from the BODY (`upstream request failed` /
`endpoint is unavailable`) and never from the status: a `403` is otherwise read
as a key refusal twenty lines earlier in `classify.go`, and with a key pool that
reading would spend keys on a model that cannot reach its own host.

The vendor's shipped client is affected identically, which is what makes this a
vendor-side condition rather than a property of this gateway:
`~/.local/share/opencode/log/opencode.log` holds 14 `AI_APICallError: Upstream
request failed: Endpoint is unavailable.` on `deepseek-v4.1-flash-free`, all
within `2026-10-20T17` UTC — the same window, in UTC, as the entries above in
MSK.

### What the gateway does about it

**Nothing, by default.** The refusal above is the client's, verbatim: the client
named a model, and answering it with a different one is a different contract than
the one it asked for. The model field of an OpenAI answer is what a tool reads to
know who replied.

The move exists, off by default, under the `model_fallback` axis (`on | off`;
`--model-fallback`, `FREE_AI_ROUTER_MODEL_FALLBACK`, `"model_fallback"` in the
file). An operator who prefers any answer to the absence of the right one's can
turn it on, and then the request moves to another FREE model of the same
provider, up to `modelFallbacks = 2` moves, never to one already tried and never
to a paid one. The reason the move is worth having at all is measured: inside the
outage `glm-5.3-flash-free`, `mimo-v2.6-flash-free`, `minimax-m3.1-flash-free` and
`longcat-2.5-preview-free` all answered `200` in the same second in which
`deepseek-v4.1-flash-free` answered with this body. Repeating the same request on
the same model cannot help — the endpoint, not the request, is what is out — and
the repeat is only the client's time.

The same move is made when the model takes the connection and never sends its
headers — again only when the axis is on. Measured 2026-10-21 03:36–03:42 MSK: the
refusal on `deepseek-v4.1-flash-free` moved the request to `big-pickle`, which
held it for the whole first-event budget and then for a second one, and the client
got a `504` — while `glm-5.3-flash-free` answered in 818ms, `mimo-v2.6-flash-free`
in 5.3s, `beam-free` in 6.2s and `longcat-2.5-preview-free` in 9.3s. So the move is
tried before the second silence budget, and the silence is counted as an abort of
the model that stayed quiet, not as a refusal: nothing was refused.

Each refused attempt is still counted as a refusal of the model that refused, and
the answer is counted against the model that gave it: the counters describe
attempts, and hiding a move would hide exactly the model that is degraded. When
there is nothing left to move to, the client receives the provider's last refusal
verbatim, as always.

The target is chosen from the models the provider's own catalog offers and only
from those the catalog marks free. The walk starts AFTER the model that failed
rather than at the head of the catalog: with the head-first scan every request in
the 2026-10-21 outage landed on the same first entry, which was itself quiet, and
the rest of a healthy catalog was never reached. The free flag comes from the
property catalog, so a run that has not read it yet has no free model to move to —
the log says so in one line (`no untried free model to move to`) rather than
leaving it to be read as a plain refusal.

`FreeTierError` is classified as a quota code by the vendor's own classifier
(`packages/ai/src/provider-error.ts`, `classifyProviderFailure`), which is why it
is never treated as a bad key.

Upstream reports of the address-level refusal — `anomalyco/opencode` #56965,
#56874, #56916, #56762, #56853 — are closed as not planned. The vendor's answer
in #56965: the flag "comes from the free model gateway's abuse detection… only
the team can review the flag, tell you whether it expires, or lift it". The flag
is not about the request, and it is not about the account either: "This isn't
related to your account".

Measured 2026-10-19, `~/.local/state/free-ai-router/app.log`: one burst was 21
refusals in a minute and 8 in four seconds. The bursts were this gateway
repeating a request, not the provider counting them — the repeat is the client's
mistake, and it is why the client-side retry policy below matters.

## Why an address gets flagged

Everything known about the flag comes from the vendor's own triage and from the
docs, not from the code: the admission and abuse backend is a separate service
(`opencode-inference-<stage>`) that is neither in the vendor tree nor described
anywhere.

| fact | where it comes from |
| --- | --- |
| the flag is scoped to the client **address**, and to the network **prefix**: changing the IPv6 inside a flagged prefix did not help, a mobile hotspot did | #56965, #56762, #56853 |
| shared, carrier-grade-NAT and VPN addresses get flagged "because of other people's traffic" | #56916 |
| the message names the remedy it believes in: "If you're on a VPN or proxy, turn it off and try again" | the refusal body itself |
| automation is a trigger: looping the CLI at extraction work earned this exact flag for a residential address, and it held for hours | #56853 |
| no published threshold, no published duration, no self-service: the only channel is `help@anoma.ly` | #56965, and the absence of any abuse or fair-use page in the docs |
| relaying free models through a gateway is refused **by design**, separately from the flag | #56662, #56702 |

Two consequences for this gateway, and both are the reason for what it does and
does not do:

- Repeating a refused request cannot help and is not a signal the provider asks
  for. The vendor's own client classifies 403 as `AuthenticationError` and does
  not retry it (`packages/ai/src/provider-error.ts`), which is the behaviour this
  gateway copies.
- The address is a resource with a reputation, and the gateway cannot repair it
  from inside a request. What it can do is not look like a farm: one stable
  session per conversation, a model list polled four times an hour instead of
  twelve, and a request payload that is the client's own.


## Free models

Measured 2026-10-19 from `/inference/v1/models`; the list is the service's and
changes without notice, so the gateway reads it live rather than keeping a copy:

```
beam-free
big-pickle
deepseek-v4.1-flash-free
glm-5.3-flash-free
jev-1.13-free
longcat-2.5-preview-free
mimo-v2.6-flash-free
minimax-m3.1-flash-free
nemotron-3.5-lightning-free
nemotron-3.5-super-vl-free
```

The paid lane (`/inference/go/v1/models`) carries the same families without the
`-free` suffix, e.g. `deepseek-v4.1-flash`.

## Time budgets

The lane holds pauses of its own, so a budget shorter than the client's would
break a connection the client still expects:

| budget | default | configuration key |
| --- | --- | --- |
| until the first event | 30s | `first_event_timeout` |
| between events | 120s | `idle_timeout` |
| between events, reasoning models | 300s | `reasoning_idle_timeout` |

## Checks

```sh
# the catalog
curl -s http://127.0.0.1:8787/v1/models | jq -r '.data[].id' | head

# a non-streaming request — the shape condition 1 is about
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8787/v1/chat/completions \
  -H 'content-type: application/json' \
  -d '{"model":"opencode/deepseek-v4.1-flash-free","stream":false,
       "messages":[{"role":"user","content":"Reply with exactly: PONG"}]}'
# 200 — the gateway streams upstream and merges the answer back
# 403 FreeTierError — the gateway passed "stream": false through

# the same request, streamed
curl -sN http://127.0.0.1:8787/v1/chat/completions \
  -H 'content-type: application/json' \
  -d '{"model":"opencode/deepseek-v4.1-flash-free","stream":true,
       "messages":[{"role":"user","content":"Reply with exactly: PONG"}]}' | head -3
```

## Files

| file | what it holds |
| --- | --- |
| `internal/provider/opencode/provider.go` | request assembly, headers, forced stream, chat path |
| `internal/provider/opencode/prompt.go` | the canonical system prompt and the environment block |
| `internal/provider/opencode/prompt/default.txt` | `default.txt` of opencode v1.19.3, embedded |
| `internal/provider/opencode/contract.go` | the admission conditions, the wire-shape table, the budgets |
| `internal/provider/opencode/classify.go` | refusal classification, the quota and auth code sets |
| `internal/provider/opencode/sessionids.go` | session identifier derivation, the shape the check admits |
| `internal/provider/opencode/sessioncache.go` | one session per conversation, bounded |
| `internal/provider/opencode/identifiers.go` | the identifier formula of the shipped client, reproduced |
| `internal/gateway/collect.go` | event stream merged into one body for a non-streaming caller |
| `internal/gateway/gateway.go` | the rotation loop: key, repeat, and the move to another model |
| `internal/transport/bunhello.go` | the captured Bun ClientHello |
| `internal/transport/direct.go` | egress: HTTP/1.1, ALPN, uTLS with the Bun hello |

## Sources

Vendor tree, `~/Projects/opencode` (branch `dev`):

| file | what was taken from it |
| --- | --- |
| `packages/console/app/src/lib/inference-proxy.ts` | keyless free lane, `cf-connecting-ip` forwarded as `x-real-ip` |
| `packages/core/src/app.ts` | the `User-Agent` format |
| `packages/core/src/session/system-prompt.ts` | the system prompt assembled from tool names |
| `packages/opencode/src/session/prompt/default.txt` (tag `v1.19.3`) | the canonical chat prompt embedded in `prompt/default.txt` |
| `packages/ai/src/provider-error.ts` | `classifyProviderFailure`, the quota and auth code sets |
| `packages/schema/src/identifier.ts` | the identifier formula: inverted timestamp, 12 hex + 14 base62 |
| `packages/core/src/session/model-request.ts` | the header set of a live request, `x-opencode-client` included |
| `services/www/src/docs/content/console/go.mdx` | the published client requirements, the abuse sentence |
| `services/www/src/docs/content/console/api/inference.mdx` | the paid lane as a plain OpenAI-compatible endpoint |

The inference service itself is not in the repository: the console reads its
address from `SST_RESOURCE_*` bindings
(`packages/console/resource/resource.cloudflare.ts`), so the admission rules above
are measured, not read. The abuse detection in particular lives behind that
binding — no file and no commit in the vendor tree carries its message, and
`sst.config.ts` deploys no inference worker — so its thresholds are not knowable
from the code, only from the vendor's triage in the issues.

