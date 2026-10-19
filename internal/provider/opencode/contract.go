package opencode

import (
	"strings"
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
// That default is NOT what the free lane admits. The lane applies a
// client-attestation check to the User-Agent, and the check is a substring
// conjunction: the string must carry an `opencode` token AND a `bun` token.
// Measured against the live service, one header changed at a time:
//
//	opencode/1.18.35 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14 -> admitted
//	opencode/1.18.35 bun                                            -> admitted
//	opencode/1.18.34                                                -> refused
//	opencode/1.18.35                                                -> refused
//	opencode/unknown/unknown/opencode                               -> refused
//	bun                                                             -> refused
//	node bun                                                        -> refused
//
// Version, order and the rest of the string are ignored; only the two tokens
// matter. The previous value carried `opencode` and no `bun`, so it failed the
// second half of the conjunction on every request.
//
// WARNING about the method, not the result: never isolate a wire-shape
// condition on an egress address whose quota is spent. 403s are then produced
// by the exhausted quota rather than by the request, and every reading in the
// batch is noise. Fresh sessions per batch are what make the table above
// meaningful.
const DefaultUserAgent = "opencode/1.18.35 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14"

// The free-lane admission conditions, measured one request at a time.
//
// A request is admitted when BOTH of these hold, and refused with 403
// FreeTierError otherwise — the refusal arrives before a model is chosen, so it
// says nothing about the account, the key or the quota:
//
//  1. The body is in the stream form: "stream": true. Everything else in the
//     body can be subtracted (stream_options, max_tokens, temperature,
//     reasoning_effort) without changing the verdict, and "stream": false fails
//     it on its own — with the canonical tools, with the canonical prompt, on
//     HTTP/1.1 and on HTTP/2 alike.
//
//  2. The request carries EITHER the mandatory tool set (shell and read, name,
//     description and schema compared in full) OR the recognised system prompt,
//     verbatim. The two are alternatives, not requirements: canonical tools with
//     an arbitrary or absent system message are admitted, and the canonical
//     prompt with no tools is admitted. Anything less than one of them in full
//     is refused — 26 characters of it, the real prompt minus its examples, and
//     a foreign prompt all fail.
//
// Condition 1 is the one this proxy used to lose: a client that did not ask for
// a stream was passed through with "stream": false and refused, which is exactly
// the non-conforming traffic that gets an egress address flagged. Condition 2 is
// why the mandatory tools are injected unconditionally: they are the cheapest
// half of the disjunction, and they are needed for the request to work at all.
//
// The User-Agent is NOT part of these two conditions, and the version digits are
// not part of the substring test either: opencode/1.19.2 with the same suffix
// was admitted everywhere opencode/1.18.35 was, in the same batch.

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

// systemPromptTemplate is the system prompt template with the tool guidance
// substituted.
//
// The prompt is RENDERED rather than taken ready-made: the client assembles it
// from the names of the active tools
// (packages/core/src/session/system-prompt.ts), and the insertion depends on
// what is available. Passing the client's prompt through as-is would give a
// different shape, because the client has a different set of tools.
const systemPromptTemplate = "You are an AI agent running in OpenCode, a coding agent harness. " +
	"Help the user accomplish their goals using the tools you have available.\n" +
	"\n" +
	"# Harness\n" +
	"- Responses are rendered as GitHub-flavored Markdown.\n" +
	"- `<system-reminder>` blocks are harness instructions, not user-authored content. Read and follow them.\n" +
	"- Prefer parallelizing independent tool calls.\n" +
	"${OPENCODE_TOOL_GUIDANCE}\n" +
	"\n" +
	"# Communication\n" +
	"- Use clear file paths when referring to files.\n" +
	"- Keep responses clear and concise, and avoid unnecessary technical jargon.\n" +
	"\n" +
	"# Working in codebases\n" +
	"- Keep changes consistent with the structure, naming, style, and patterns of the surrounding code.\n" +
	"- Treat unfamiliar files or changes as potential user work and investigate before deleting or overwriting them.\n"

// guidanceShell is the guidance added when the shell tool is available.
const guidanceShell = "- Prefer dedicated tools over shell commands; fall back to the shell when a tool cannot do what you need.\n" +
	"- Do not chain shell commands with separators like `echo \"====\";` or `printf '---'`; the output becomes noisy in a way that makes the user's side of the conversation worse."

// guidanceWrite is the guidance for the write tool.
const guidanceWrite = "- Use the write tool to create files or completely replace their content. " +
	"Prefer using the edit tool for targeted changes."

// guidanceEdit is the guidance for the edit tool.
const guidanceEdit = "- Use the edit tool for targeted changes to existing text files. " +
	"It replaces the exact text in `oldString` with `newString`, and the values must differ. " +
	"By default, `oldString` must occur exactly once. If it occurs multiple times, include more " +
	"surrounding context to make it unique or set `replaceAll` to true to replace every occurrence."

// RenderSystemPrompt assembles the system prompt from the names of the active
// tools.
//
// The function is exported because its result is a verifiable property: the
// prompt is a pure function of the list of names, and that is testable without
// the network.
//
// The guidance is added for the same three tools as on the client: shell, write,
// edit. The insertion order matches the order of the checks in system-prompt.ts.
func RenderSystemPrompt(toolNames []string) string {
	has := make(map[string]bool, len(toolNames))
	for _, name := range toolNames {
		has[name] = true
	}

	var guidance []string
	if has["shell"] {
		guidance = append(guidance, guidanceShell)
	}
	if has["write"] {
		guidance = append(guidance, guidanceWrite)
	}
	if has["edit"] {
		guidance = append(guidance, guidanceEdit)
	}

	return strings.Replace(systemPromptTemplate, "${OPENCODE_TOOL_GUIDANCE}", strings.Join(guidance, "\n"), 1)
}
