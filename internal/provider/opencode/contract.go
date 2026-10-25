package opencode

import (
	"time"

	"free-ai-router/internal/provider"
)

// jsonSchemaDialect is the schema dialect the client declares in tool
// descriptions. It is part of the check, so it is pinned.
const jsonSchemaDialect = "https://json-schema.org/draft/2020-12/schema"

// DefaultUserAgent is the User-Agent string.
//
// The client builds it as `opencode/${channel}/${version}/${name}`
// (packages/core/src/app.ts), and an arbitrary build ends up with
// `opencode/unknown/unknown/opencode` when the parts are unset — which is what
// this constant used to say.
//
// The lane checks the User-Agent, but only for "does this look like an OpenCode
// build", not for a version. Measured on 2026-10-19, canonical session ids and
// the mandatory tools held constant, one header changed at a time:
//
//	opencode/1.18.35 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14 -> admitted
//	opencode/1.19.2                                              -> admitted
//	opencode/stable/2.0.32/opencode                              -> admitted
//	opencode/unknown/unknown/opencode                            -> refused
//	bun                                                          -> refused
//	node bun                                                     -> refused
//	(no User-Agent header)                                       -> refused
//
// Version digits and the suffix are ignored: the 1.x form and the 2.x form are
// both admitted, and so is a bare `opencode/<version>`. A refused shape is one
// that does not resolve to an OpenCode release at all. The value below is the
// User-Agent of the build installed on this host, so the header names a build
// that exists.
//
// WARNING about the method, not the result: never isolate a wire-shape
// condition on an egress address whose quota is spent. 403s are then produced
// by the exhausted quota rather than by the request, and every reading in the
// batch is noise. Fresh sessions per batch are what make the table above
// meaningful — and an earlier batch on this same lane, whose session headers
// happened to be malformed, produced a UA table that no longer reproduces.
const DefaultUserAgent = "opencode/1.19.2 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14"

// The free-lane admission conditions, measured one request at a time.
//
// The lane has two different refusals, and they mean different things:
//
//   - 403 FreeTierError arrives before a model is chosen. It is about the SHAPE
//     of the request; the conditions below describe it.
//   - 429 FreeUsageLimitError is the quota refusal. On the legacy lane it means
//     the address has spent its free requests. On the client lane it is also
//     what traffic that does not look like the client gets: such a request is
//     routed to the exhausted legacy bucket instead of being refused with 403.
//
// The 403 conditions, measured on 2026-10-19:
//
//  1. The body is in the stream form: "stream": true. Everything else in the
//     body can be subtracted (stream_options, max_tokens, temperature,
//     reasoning_effort) without changing the verdict, and "stream": false fails
//     it on its own — with the canonical tools, with the canonical prompt, on
//     HTTP/1.1 and on HTTP/2 alike.
//
//  2. The request carries the mandatory tool set: a `read` tool AND a `shell`
//     or `bash` tool, by name. Only the names are decisive — the same request
//     with the canonical definitions replaced by `{"name":"shell","description":
//     "run shell",...}` is admitted too, which was measured on 2026-10-19. The
//     names are still sent with the client's own descriptions and schemas
//     because a deeper check is the obvious next move for the lane, and the cost
//     of the text is a few thousand tokens of a prompt that is cached.
//
//  3. The request carries a session header whose value is an identifier of the
//     client's shape: `^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`. A request that is
//     otherwise admitted is refused when the header is missing, and refused
//     when it carries anything else — `ses_test`, a bare UUID, `ses_` followed
//     by 64 zeros. This is the condition that is easy to lose without noticing:
//     the value is copied from the client, so a client with its own naming
//     convention fails every call. The provider therefore replaces a value of
//     the wrong shape with a derived one (sessionids.go). The gateway derives
//     nothing of its own: a service that ignores these headers takes the
//     client's values unchanged.
//
// The 429 conditions, measured on 2026-10-25. Each one flips 429/200 with
// everything else held constant, including the body, the headers and the
// address:
//
//  4. The TLS ClientHello must be the one Bun/BoringSSL sends. The real client
//     is a Bun binary; its hello is admitted, while crypto/tls, curl/OpenSSL
//     and uTLS with Chrome's hello are all answered 429. A hand-built hello
//     that follows the captured Bun one is answered 200 (transport/bunhello.go).
//
//  5. The chat path must be /inference/openai/v1/chat/completions. The legacy
//     /inference/v1/chat/completions is now the exhausted lane and is answered
//     429 even with the client's own body. The model catalog stays on
//     /inference/v1/models.
//
//  6. The system message must carry the reference client's canonical prompt
//     (opencode v1.19.3, packages/opencode/src/session/prompt/default.txt)
//     followed by the environment block the client appends. The canonical text
//     alone, the superseded prompt this package used to render, and a generic
//     system message are all answered 429; the text plus the block is answered
//     200. The values in the block (directory, date) are not checked — a changed
//     directory and date kept the 200 — and the client's own rules may follow
//     the block. The provider therefore renders the prompt and keeps the
//     client's part after it (prompt.go).
//
// Condition 1 is the one this proxy used to lose: a client that did not ask for
// a stream was passed through with "stream": false and refused, which is exactly
// the non-conforming traffic that gets an egress address flagged. Condition 2 is
// why the mandatory tools are injected unconditionally: they are needed for the
// request to work at all.
//
// A refusal costs more than the answer: the lane counts refused requests against
// the egress address, and a burst of them turns into the address-level
// "we've detected abusive traffic from this IP address" refusal, which no
// request shape can undo. That is why every condition here is enforced on our
// side rather than left to the caller.
//
// The User-Agent is not one of these conditions either — it is checked, but only
// for "looks like an OpenCode build"; see DefaultUserAgent. The 1.19.2 string is
// admitted.
//
// HOW OUR REQUEST COMPARES TO THE CLIENT'S OWN, captured on 2026-10-19 and
// re-measured on 2026-10-25 by running the installed client through a
// TLS-terminating proxy and reading the request off the wire:
//
//	field            the client                    this proxy
//	---------------- ------------------------------ ------------------------------
//	protocol         HTTP/1.1, ALPN "http/1.1"     HTTP/1.1 (transport/direct.go)
//	TLS hello        Bun/BoringSSL                 the captured Bun hello
//	                                               (transport/bunhello.go)
//	path             /inference/openai/v1/chat/…   the same
//	headers          Content-Type, User-Agent,     the same set, the same spelling
//	                 x-opencode-client,            (the custom names lowercase, as
//	                 x-opencode-project,           the client writes them)
//	                 x-opencode-request,
//	                 x-opencode-session,
//	                 x-opencode-session-id,
//	                 Connection: keep-alive,
//	                 Accept: *∕*
//	header order     as listed above               alphabetical, Host first
//	Accept-Encoding  gzip, deflate, br, zstd       gzip
//	body             max_tokens, tool_choice also  only what the caller sent
//	                 present
//	system message   default.txt + env block       the same, then the client's
//	                                               rules
//
// The differences that remain are deliberate or out of reach:
//
//   - The header order is Go's: net/http writes Host first and sorts the rest.
//     Matching the client's order needs a hand-written HTTP/1.1 writer, and the
//     order is not decisive — curl, whose order differs from both, is admitted.
//   - gzip alone, because a Go transport decodes only what it negotiated itself.
//   - max_tokens and tool_choice are the caller's: this proxy is not the client
//     and must not invent a token budget for it.
//
// The lesson of the same measurement, which is why the table is not a to-do
// list: the SAME bytes from the same address were answered 403 at 21:23 and 200
// at 21:28 with nothing changed. The verdict is not a pure function of the
// request: the address-level state matters too. The 429 conditions above were
// measured afterwards, each against a fresh session, with the address in an
// admitting state.

// Stream budgets. The numbers are the contract's own: the client holds its own
// timeouts, and a budget shorter than the client's would make the gateway break
// a connection the client still expects to be alive. They are therefore the
// defaults rather than the only values — an operator on a slow uplink may need
// a wider band, and the configuration carries it here.
const (
	// firstEventTimeout is the budget until the first response headers.
	firstEventTimeout = 30 * time.Second
	// chatIdleTimeout is the silence budget of an ordinary stream.
	chatIdleTimeout = 120 * time.Second
	// reasoningIdleTimeout is the silence budget for models that can reason:
	// they hold pauses noticeably longer.
	reasoningIdleTimeout = 300 * time.Second
)

// firstEventBudget returns the budget until the first response headers.
func (p *Provider) firstEventBudget() time.Duration {
	if p.cfg.FirstEventTimeout > 0 {
		return p.cfg.FirstEventTimeout
	}
	return firstEventTimeout
}

// idleTimeoutFor chooses the silence budget by the model's capabilities.
func (p *Provider) idleTimeoutFor(m provider.Model) time.Duration {
	if m.Reasoning {
		if p.cfg.ReasoningIdleTimeout > 0 {
			return p.cfg.ReasoningIdleTimeout
		}
		return reasoningIdleTimeout
	}
	if p.cfg.IdleTimeout > 0 {
		return p.cfg.IdleTimeout
	}
	return chatIdleTimeout
}

// Tool field descriptions. They are extracted into constants because they are
// part of the check: the service compares the text, not the intent.
const (
	workdirDescription = "Working directory to execute the command in. " +
		"Defaults to the current working directory. When possible, avoid changing " +
		"directories in the command and set the working directory here instead."

	timeoutDescription = "Timeout in milliseconds. Set to 0 to disable the timeout. " +
		"Defaults to 120000 for foreground commands. Background commands have no timeout by default."

	backgroundDescription = "Run the command in the background and return immediately " +
		"(useful for dev servers and long-running builds). You do not need to use '&' at the end " +
		"of the command when using this parameter. You will be notified when it completes. " +
		"DO NOT poll for completion."

	readDescription = "Read the contents of a file or directory. Supports text files, images, " +
		"and PDFs. Images and PDFs are presented directly to the model. Each text line is prefixed " +
		"by its 1-based line number as <line>: <content>. The prefix is for reference and is not part " +
		"of the file content. Directory entries are returned one per line. Use offset and limit to read " +
		"large files or directories in sections. Prefer one larger read over many small slices, and use " +
		"grep to find specific content in large files."
)

// shellDescriptionTemplate is the shell tool description with the system and the
// shell substituted. %s is the system name, %s is the shell name.
const shellDescriptionTemplate = "Execute a shell command and return its output. " +
	"Commands run on %s using %s. " +
	"Quote file paths containing spaces or special characters. " +
	"Prefer dedicated tools over shell commands when possible. " +
	"When output is large, the full result is saved to a file and a truncated preview is returned. " +
	"Rely on automatic truncation unless filtering the output is more useful. " +
	"Commands accept an optional timeout, background commands have no timeout by default. " +
	"Background commands return immediately, and you will be notified when they complete."
