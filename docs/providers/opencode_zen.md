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
| `/inference/v1/chat/completions` | POST | 200 | the provider's base URL |
| `/inference/v1/models` | GET | 200 | model list, same base |
| `/inference/openai/v1/chat/completions` | POST | 200 | the path the reference client uses |
| `/inference/go/v1/models` | GET | 200 | paid lane, ids carry no `-free` suffix |
| `/inference/go/openai/v1/models` | GET | 404 | not a route |
| `/zen/v1/models` | GET | 200 | legacy alias |
| `/zen/go/v1/models` | GET | 200 | legacy alias |

measured 2026-10-19. The provider's `base_url` is `https://opencode.ai/inference/v1`
(`DefaultBaseURL` in `internal/provider/opencode/provider.go`), taken from the
`api` key of the published model catalog rather than from the client's code.

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

Four conditions, each measured one request at a time on 2026-10-19 with the rest
held constant. A request that fails any of them is answered `403` with a
`FreeTierError` *before a model is chosen* — so the refusal says nothing about
the model, the quota or the account.

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
(`internal/provider/opencode/sessionids.go`); the gateway derives nothing, so a
service that ignores these headers takes the client's values unchanged.

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

The system prompt is **not** a condition: with the tools present and the session
header canonical, a request whose only message is the user's is admitted
(measured 2026-10-19). The gateway renders the recognised prompt anyway
(`RenderSystemPrompt`), from the names of the active tools, the way the client
assembles it (`packages/core/src/session/system-prompt.ts`).

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

Captured 2026-10-19 by running the installed client through a TLS-terminating
proxy. The client's request and this gateway's:

| field | the client | this gateway |
| --- | --- | --- |
| protocol | HTTP/1.1, ALPN `http/1.1` | HTTP/1.1, ALPN `http/1.1` |
| path | `/inference/openai/v1/chat/completions` | `/inference/v1/chat/completions` |
| headers | the set above | the same set, the same spelling |
| order | as listed above | alphabetical, `Host` first |
| `Accept-Encoding` | `gzip, deflate, br, zstd` | `gzip` |
| body | `max_tokens`, `tool_choice` present | only what the caller sent |

Both paths are answered 200 with this shape, so the path is a choice, not a
condition. The order is Go's and is not decisive — curl, whose order differs from
both, is admitted. The TLS ClientHello cannot be matched from `crypto/tls` at
all; measured JA3: client `8f1d483d7c5fba63a9170d6e4dd66f93`, Go
`96458b3de39df5d47bc5c4a9f10f8f26`, curl `0659743d0a8904909448456a6dfa4b06` — all
three admitted while the lane was open, so the fingerprint is not a condition
today.

## Response

An event stream of `data: ` lines, each carrying one chunk; the last two are the
usage chunk and `data: [DONE]`. With `stream_options: {"include_usage": true}`
the final chunk carries `usage.prompt_tokens`, `completion_tokens`,
`total_tokens` — optional on the wire, and requested because a client that counts
tokens from a stream has nowhere else to read them.

## Refusals

| status | body | class | lever |
| --- | --- | --- | --- |
| 403 | `{"type":"error","error":{"type":"FreeTierError","message":"OpenCode's free tier can only be used from within OpenCode. Download it at https://opencode.ai and keep building."}}` | request shape | the four conditions above |
| 403 | `{"code":null,"message":"OpenCode's free models aren't available from your current network because we've detected abusive traffic from this IP address. This isn't related to your account. If you're on a VPN or proxy, turn it off and try again, or subscribe to OpenCode Go…"}` | egress address | none in the request; the vendor's team, or another address, or time |
| 401 | `{"error":{"type":"server_error","message":"Upstream request failed: Go requires a credential"}}` | paid lane | a credential; the free lane has no equivalent |

`FreeTierError` is classified as a quota code by the vendor's own classifier
(`packages/ai/src/provider-error.ts`, `classifyProviderFailure`), which is why it
is never treated as a bad key.

Upstream reports of the address-level refusal — `anomalyco/opencode` #56965,
#56874, #56916, and the in-client variant #56324, #55435, #52917 — are open. The
vendor's answer in #56965: the flag "comes from the free model gateway's abuse
detection… only the team can review the flag, tell you whether it expires, or
lift it". A refused request counts against the address, so a burst of refusals
extends the block; one observed burst was 21 refusals in a minute and 8 in four
seconds, produced by a client retrying a request that the shape conditions above
would have prevented (measured 2026-10-19, `~/.local/state/free-ai-router/app.log`).

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
| `internal/provider/opencode/provider.go` | request assembly, headers, forced stream, path |
| `internal/provider/opencode/contract.go` | the four conditions, the wire-shape table, the budgets |
| `internal/provider/opencode/classify.go` | refusal classification, the quota and auth code sets |
| `internal/provider/opencode/sessionids.go` | session identifier derivation, the shape the check admits |
| `internal/gateway/collect.go` | event stream merged into one body for a non-streaming caller |
| `internal/transport/direct.go` | egress: HTTP/1.1, ALPN, no proxy of its own |

## Sources

Vendor tree, `~/Projects/opencode` (branch `dev`):

| file | what was taken from it |
| --- | --- |
| `packages/console/app/src/lib/inference-proxy.ts` | keyless free lane, `cf-connecting-ip` forwarded as `x-real-ip` |
| `packages/core/src/app.ts` | the `User-Agent` format |
| `packages/core/src/session/system-prompt.ts` | the system prompt assembled from tool names |
| `packages/ai/src/provider-error.ts` | `classifyProviderFailure`, the quota and auth code sets |

The inference service itself is not in the repository: the console reads its
address from `SST_RESOURCE_*` bindings
(`packages/console/resource/resource.cloudflare.ts`), so the admission rules above
are measured, not read.
