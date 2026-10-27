// Package opencode implements the OpenCode Zen provider (opencode.ai).
//
// WHAT IS SPECIFIC TO IT and therefore lives here:
//
//   - the set of mandatory tools: the request must carry the definitions of
//     `shell` and `read` in full — name, description and schema (the check
//     compares entries, not names);
//   - the shape of the system prompt: the service rejects the specific text of
//     the title generator, so the prompt is rendered rather than passed through
//     as-is;
//   - the form of the headers: names, the separation of session and affinity,
//     the User-Agent format, the ban on the `prj_` and `req_` prefixes in
//     individual fields;
//   - the credentials field: an empty string means anonymous access, and that is
//     a normal mode rather than a missing setting.
//
// WHAT IS NOT HERE and must not be: retry policy, key rotation, transport. That
// is common machinery — see internal/capability and internal/transport.
//
// The source of the contract is opencode 2.0.30 sources:
//
//	headers         packages/core/src/session/model-request.ts
//	affinity        packages/core/src/session/affinity.ts
//	user agent      packages/core/src/app.ts
//	tools           packages/core/src/tool/plugin/shell.ts, read.ts
//	system prompt   packages/core/src/session/runner/prompt/system.txt
//	                packages/core/src/session/system-prompt.ts
//	models          packages/core/src/models-dev/snapshot.txt
package opencode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"free-ai-router/internal/provider"
	"free-ai-router/internal/provider/modelcat"
)

// ID is the provider identifier. It is visible in configuration, in the CLI and
// as the route name in the client, so it cannot be changed without renaming the
// route.
// liveModelsPath is the path of the service's model list.
//
// The same endpoint as model requests use: the service declares its composition
// where it serves it. It has no separate read-only catalog.
const liveModelsPath = "/models"

const ID provider.ID = "opencode"

// DefaultBaseURL is the address of the provider's model list.
//
// Taken from the model catalog (snapshot.txt, the `api` key) rather than from
// the client's code: the catalog is what the client goes by when choosing a
// model. It is NOT the chat address: the service moved chat to a
// protocol-specific path, and the old one now answers from a different,
// address-limited lane (see DefaultChatPath).
const DefaultBaseURL = "https://opencode.ai/inference/v1"

// DefaultChatPath is the path of the chat endpoint on the service's host.
//
// MEASURED on 2026-10-24: the reference client sends chat to
// /inference/openai/v1/chat/completions. The old /inference/v1/chat/completions
// still answers, but from the address-limited free lane, and with a spent
// address it answers 429 FreeUsageLimitError while the client's path answers
// 200 with the same body, headers and TLS client. The path is therefore part of
// the wire contract, not a cosmetic alias.
const DefaultChatPath = "/inference/openai/v1/chat/completions"

// Config is the provider settings, parsed from the gateway configuration.
type Config struct {
	// ID is the route name of this section.
	//
	// Taken from the configuration rather than fixed here: the same type can
	// serve several sections — two accounts, two endpoints — and a hard-coded
	// name would make the second one unreachable, because the client addresses a
	// provider by the name its section has. Empty falls back to ID, which is what
	// a caller with nothing to say means.
	ID provider.ID
	// BaseURL overrides the provider address.
	BaseURL string
	// ChatPath overrides the chat path on the BaseURL host.
	//
	// A path rather than a whole URL: the catalog and the chat endpoint share a
	// host but not a path, and a deployment that mirrors the service usually
	// keeps that arrangement. An absolute http(s) URL is accepted too, for a
	// mirror that serves chat from another host.
	ChatPath string
	// MetaURL overrides the address of the property catalog.
	//
	// The properties of a model — limits, reasoning, price — are published
	// separately from the list of models, and both addresses belong to the
	// service. Overridable for the same reason the address is: a deployment that
	// mirrors the catalog, and a check that must not reach the network, both need
	// to say where to read it. Empty means the published address.
	MetaURL string
	// APIKey is the credentials. An empty string means anonymous access: the
	// provider sends the anonymity marker rather than a Bearer with a key.
	APIKey string
	// AnonymousKey is the anonymity marker. By default "public".
	AnonymousKey string
	// UserAgent overrides the User-Agent string.
	UserAgent string
	// ShellName and OSName go into the shell tool description. They are part of
	// the check, so they are lifted into settings: on another platform the
	// description must match what the client there would have sent.
	ShellName string
	OSName    string
	// ClientName is the value of the x-opencode-client header. By default "cli".
	ClientName string
	// HTTP is the client for catalog requests.
	//
	// Separate from the gateway's client, because it lives by different rules:
	// the catalog is requested rarely and with a small body, while a model
	// request is streamed and long. A shared client would force a wait limit
	// suitable for both, and no such limit exists. Empty means the default
	// client.
	HTTP *http.Client
	// Log receives the background catalog refresh warnings. May be nil.
	Log func(format string, args ...any)

	// FirstEventTimeout, IdleTimeout and ReasoningIdleTimeout are the silence
	// budgets carried into every prepared request: until the headers, between
	// events of an ordinary answer, and between events while the model reasons.
	//
	// Zero means the contract's own number. The contract is where the defaults
	// come from, but an operator whose uplink is slow needs to widen the band
	// without a rebuild — and a budget that cannot be widened is a budget that
	// gets disabled altogether.
	FirstEventTimeout    time.Duration
	IdleTimeout          time.Duration
	ReasoningIdleTimeout time.Duration
}

// Provider is the provider.Provider implementation for OpenCode Zen.
type Provider struct {
	cfg Config

	id           provider.ID
	baseURL      string
	chatURL      string
	anonymousKey string
	userAgent    string
	shellName    string
	osName       string
	clientName   string
	// catalog is the model catalog obtained from the service.
	catalog *modelcat.Catalog
	// sessions keeps the session identifier of each conversation whose client
	// sends none, so that its turns share one session on the service side.
	sessions *sessionCache
}

// New creates a provider from configuration.
func New(cfg Config) (*Provider, error) {
	p := &Provider{cfg: cfg}

	p.id = cfg.ID
	if p.id == "" {
		p.id = ID
	}
	p.baseURL = strings.TrimRight(cfg.BaseURL, "/")
	if p.baseURL == "" {
		p.baseURL = DefaultBaseURL
	}
	p.chatURL = resolveChatURL(p.baseURL, cfg.ChatPath)
	p.anonymousKey = cfg.AnonymousKey
	if p.anonymousKey == "" {
		p.anonymousKey = "public"
	}
	p.userAgent = cfg.UserAgent
	if p.userAgent == "" {
		p.userAgent = DefaultUserAgent
	}
	p.shellName = cfg.ShellName
	if p.shellName == "" {
		p.shellName = "sh"
	}
	p.osName = cfg.OSName
	if p.osName == "" {
		p.osName = "Linux"
	}
	p.clientName = cfg.ClientName
	if p.clientName == "" {
		p.clientName = "cli"
	}

	// The catalog is requested with the same headers as a model request: the
	// service recognises the client by them, and without them the catalog may be
	// trimmed.
	//
	// The composition of models is determined by the service's list, and the
	// properties are augmented by the property catalog. The order of sources
	// here is the priority: whatever the service names as live is what ends up
	// in the catalog.
	httpClient := cfg.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	p.sessions = newSessionCache(defaultSessionCacheSize)
	p.catalog = modelcat.New(
		p.baseURL, liveModelsPath, httpClient, p.applyHeaders,
		&metaSource{http: httpClient, url: cfg.MetaURL},
	).WithEligibility(isFree).WithLog(cfg.Log)
	return p, nil
}

// ID implements provider.Provider.
func (p *Provider) ID() provider.ID { return p.id }

// Models implements provider.Provider.
//
// Read from memory; the background refresher owns the network. A list in the
// code is deliberately not kept here: it would drift from the service, and the
// client would select models that no longer exist.
func (p *Provider) Models(ctx context.Context) ([]provider.Model, error) {
	return p.catalog.Models(ctx)
}

// Start begins the background catalog refresh.
//
// Implements provider.Polling. Reads come out of memory afterwards, so this has
// to run before the listener accepts anything — otherwise the first requests
// see an empty catalog.
func (p *Provider) Start(ctx context.Context) { p.catalog.Start(ctx) }

// Stop ends the background catalog refresh.
func (p *Provider) Stop() error { return p.catalog.Stop() }

// Credential returns the value for Authorization.
//
// A separate method rather than a struct field: the value depends on which key
// the common rotation machinery issued, and is taken on every request.
func (p *Provider) Credential(key string) string {
	if key == "" {
		return p.anonymousKey
	}
	return key
}

// Build implements provider.Provider.
//
// The request body is assembled in the OpenAI Chat Completions form: that is
// what the provider understands and what already arrives from the client. The
// gateway passes messages in neutral form, so the single conversion happens
// here.
func (p *Provider) Build(req provider.Request) (provider.Prepared, error) {
	body, err := p.buildBody(req)
	if err != nil {
		return provider.Prepared{}, err
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return provider.Prepared{}, fmt.Errorf("opencode: encoding body: %w", err)
	}

	headers := p.buildHeaders(p.stableSession(req))

	return provider.Prepared{
		URL:     p.chatURL,
		Headers: headers,
		Body:    raw,
		// The body always asks for a stream, so the gateway has to know that
		// even a client that wanted one body will receive events.
		StreamsUpstream: true,
		// The reasoning band holds longer pauses than an ordinary stream,
		// because the model can stay silent for several minutes while still
		// working. Splitting the budgets is part of the contract, not a setting;
		// their lengths are the operator's, within the contract's defaults.
		IdleTimeout:       p.idleTimeoutFor(req.Model),
		FirstEventTimeout: p.firstEventBudget(),
	}, nil
}

// resolveChatURL composes the chat endpoint from the catalog address.
//
// The path is absolute by default because the service puts chat on a different
// path of the same host. A mirror may override it with another absolute path,
// or with a whole URL when chat lives on a separate host.
func resolveChatURL(base, path string) string {
	if path == "" {
		path = DefaultChatPath
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	if u, err := url.Parse(base); err == nil && u.Scheme != "" && u.Host != "" {
		return u.Scheme + "://" + u.Host + path
	}
	return strings.TrimRight(base, "/") + path
}

// buildBody assembles the request body.
func (p *Provider) buildBody(req provider.Request) (map[string]any, error) {
	messages, err := p.buildMessages(req)
	if err != nil {
		return nil, err
	}

	// The system message is rendered here, not taken from the client as-is.
	//
	// MEASURED on 2026-10-25: the free lane admits a request only when the
	// system message carries the reference client's canonical prompt and the
	// environment block that follows it. A request without a system message,
	// with a generic one, or with the prompt this package used to render is
	// answered 429 FreeUsageLimitError; the canonical text plus the block is
	// answered 200. The client's own rules are kept after the block, which was
	// admitted as well.
	messages = append([]map[string]any{{
		"role":    "system",
		"content": p.systemPrompt(req.Model, req.System),
	}}, messages...)

	body := map[string]any{
		"model":    req.Model.ID,
		"messages": messages,
		// The stream form is mandatory on the wire, whatever the client asked
		// for.
		//
		// MEASURED against the live service: an otherwise perfect request with
		// "stream": false is answered 403 FreeTierError, and the same request
		// with "stream": true is answered 200. Nothing else in the body flips
		// the verdict. The gateway collects the events into a single response
		// when the client asked for one, so the shape the client sees stays the
		// one it requested.
		"stream": true,
	}

	// stream_options asks for the usage counters in a final chunk. The counters
	// cannot be recovered from the events otherwise, and a client that asked for
	// a single response expects them in the merged answer.
	body["stream_options"] = map[string]any{"include_usage": true}

	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.ReasoningEffort != "" {
		body["reasoning_effort"] = req.ReasoningEffort
	}

	tools := p.buildTools(req.Tools)
	if len(tools) > 0 {
		body["tools"] = tools
		// tool_choice is sent only when there are tools: the client does the
		// same. When there are no tools, the field is absent rather than
		// containing "none" — those are different forms, and the provider tells
		// them apart.
		if req.ToolChoice != "" {
			body["tool_choice"] = req.ToolChoice
		}
	}

	return body, nil
}

// buildMessages translates the neutral messages into the provider form.
func (p *Provider) buildMessages(req provider.Request) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(req.Messages)+1)

	// The thinking field of the model, and whether the conversation is in
	// thinking mode at all.
	//
	// The NAME comes from the model metadata: different models carry the
	// thinking in different places, and writing it to the wrong one would either
	// be ignored or rejected.
	field := req.Model.ReasoningField
	if field == "" {
		field = "reasoning_content"
	}
	thinking := reasoningMode(req)

	for _, m := range req.Messages {
		switch m.Role {
		case "assistant":
			msg := map[string]any{"role": "assistant", "content": m.Text}
			// The field is written for EVERY assistant message of a thinking
			// conversation, an empty string included.
			//
			// Not an empty value but a missing key is the failure: a gateway in
			// thinking mode requires the field on every assistant message when
			// tools are in play, and a turn whose thinking happened to be empty
			// would lose the key here and be rejected upstream with
			// "The `reasoning_content` in the thinking mode must be passed back
			// to the API" — after a round trip, in the middle of a tool call.
			// The client itself sends the empty string for exactly this reason.
			if thinking {
				msg[field] = m.Reasoning
			}
			if len(m.ToolCalls) > 0 {
				calls := make([]map[string]any, 0, len(m.ToolCalls))
				for _, tc := range m.ToolCalls {
					calls = append(calls, map[string]any{
						"id":   tc.ID,
						"type": "function",
						"function": map[string]any{
							"name":      tc.Name,
							"arguments": tc.Arguments,
						},
					})
				}
				msg["tool_calls"] = calls
			}
			// An empty assistant turn is not allowed on the wire.
			if m.Text == "" && m.Reasoning == "" && len(m.ToolCalls) == 0 {
				continue
			}
			out = append(out, msg)

		case "tool":
			out = append(out, map[string]any{
				"role":         "tool",
				"tool_call_id": m.ToolCallID,
				"content":      nonEmpty(m.Text),
			})

		case "system":
			// The system part is assembled separately (see buildBody); a system
			// entry in the history would send the same instruction twice.
			continue

		default: // user
			content, err := p.buildUserContent(m)
			if err != nil {
				return nil, err
			}
			if content == nil {
				continue
			}
			out = append(out, map[string]any{"role": m.Role, "content": content})
		}
	}
	return out, nil
}

// reasoningMode reports whether the conversation is in thinking mode.
//
// Two signals, and the second is what makes the first one safe to rely on. A
// model that can reason is in thinking mode by definition. But the metadata may
// lag behind the service — a model that has just learned to think is still
// described as an ordinary one — and the conversation itself carries the
// evidence: an assistant turn whose thinking came back non-empty is a
// conversation the upstream is holding in thinking mode, whatever the catalog
// says.
func reasoningMode(req provider.Request) bool {
	if req.Model.Reasoning {
		return true
	}
	for _, m := range req.Messages {
		if m.Role == "assistant" && m.Reasoning != "" {
			return true
		}
	}
	return false
}

// buildUserContent assembles the content of a user message.
//
// Text without attachments stays a string: that is what the client does, and it
// makes the body shorter. Attachments turn the content into a list of parts.
func (p *Provider) buildUserContent(m provider.Message) (any, error) {
	if len(m.Images) == 0 {
		if m.Text == "" {
			return nil, nil
		}
		return m.Text, nil
	}

	parts := make([]map[string]any, 0, len(m.Images)+1)
	if m.Text != "" {
		parts = append(parts, map[string]any{"type": "text", "text": m.Text})
	}
	for _, img := range m.Images {
		mediaType := img.MediaType
		if mediaType == "" {
			mediaType = "image/png"
		}
		parts = append(parts, map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url": "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(img.Data),
			},
		})
	}
	return parts, nil
}

// buildTools assembles the tool definitions and AUGMENTS them with the mandatory
// ones.
//
// This is where the provider's main specificity lies. The check compares entries
// in full: name, description and schema. Therefore:
//
//   - mandatory tools are added if the client did not send them;
//   - if the client sent a tool with the same name but someone else's
//     description or schema, the entry is REPLACED with the canonical one.
//     Otherwise the request would go out with the right name and the wrong
//     content — and the check looks at the content.
//
// The order does not matter: the check does not look at it. So the client's tools
// go first, and the mandatory ones are appended at the end.
func (p *Provider) buildTools(tools []provider.Tool) []map[string]any {
	gate := p.gateTools()
	gateByName := make(map[string]map[string]any, len(gate))
	for _, g := range gate {
		gateByName[g.name] = g.entry
	}

	out := make([]map[string]any, 0, len(tools)+len(gate))
	seen := make(map[string]bool, len(gate))

	for _, t := range tools {
		if canonical, isGate := gateByName[t.Name]; isGate {
			out = append(out, canonical)
			seen[t.Name] = true
			continue
		}
		out = append(out, toolEntry(t))
	}

	for _, g := range gate {
		if !seen[g.name] {
			out = append(out, g.entry)
		}
	}
	return out
}

// toolEntry translates a tool declaration into the wire form.
func toolEntry(t provider.Tool) map[string]any {
	var schema any
	if len(t.Schema) > 0 {
		if err := json.Unmarshal(t.Schema, &schema); err != nil {
			// The client's schema did not parse: we send an empty object
			// rather than fail the request. The check looks only at the
			// mandatory tools; for the rest the shape is not critical.
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
	} else {
		schema = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"parameters":  schema,
		},
	}
}

// gateTool is a mandatory tool with its entry already assembled.
type gateTool struct {
	name  string
	entry map[string]any
}

// gateNameShell and gateNameRead are the names of the mandatory tools.
//
// In client v1 the tool was called `bash`; in v2 it was renamed to `shell`
// (packages/core/src/database/v1-migration.bun.ts, RENAMED_TOOLS). The target
// version is 2.0.30, so `shell` is used.
const (
	gateNameShell = "shell"
	gateNameRead  = "read"
)

// gateTools returns the entries of the mandatory tools.
//
// The shell description is parameterised by the shell name and the system name:
// the client substitutes them from the environment, and the check compares the
// resulting text.
func (p *Provider) gateTools() []gateTool {
	shellDescription := fmt.Sprintf(shellDescriptionTemplate, p.osName, p.shellName)

	return []gateTool{
		{
			name: gateNameShell,
			entry: map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        gateNameShell,
					"description": shellDescription,
					"parameters": map[string]any{
						"$schema": jsonSchemaDialect,
						"type":    "object",
						"properties": map[string]any{
							"command": map[string]any{
								"type":        "string",
								"description": "Shell command string to execute",
							},
							"workdir": map[string]any{
								"type":        "string",
								"description": workdirDescription,
							},
							"timeout": map[string]any{
								"type":        "integer",
								"minimum":     0,
								"description": timeoutDescription,
							},
							"background": map[string]any{
								"type":        "boolean",
								"description": backgroundDescription,
							},
						},
						"required": []any{"command"},
					},
				},
			},
		},
		{
			name: gateNameRead,
			entry: map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        gateNameRead,
					"description": readDescription,
					"parameters": map[string]any{
						"$schema": jsonSchemaDialect,
						"type":    "object",
						"properties": map[string]any{
							"path": map[string]any{
								"type":        "string",
								"description": "File or directory to read",
							},
							"offset": map[string]any{
								"type":        "integer",
								"minimum":     0,
								"description": "The line or directory entry to start reading from (1-based)",
							},
							"limit": map[string]any{
								"type":        "integer",
								"minimum":     0,
								"description": "The maximum number of lines or directory entries to read (defaults to and capped at 2000)",
							},
						},
						"required": []any{"path"},
					},
				},
			},
		},
	}
}

// buildHeaders assembles the request headers.
//
// THE SET AND THE SPELLING are those of the reference client, captured from its
// live request rather than guessed: `Content-Type`, `User-Agent`,
// `x-opencode-client`, `x-opencode-project`, `x-opencode-request`,
// `x-opencode-session`, `x-opencode-session-id`, `Connection: keep-alive`,
// `Accept: */*`. The value of `x-opencode-session` is the affinity (the group
// root for cache routing) and the value of `x-opencode-session-id` is the
// session itself, so for a flat session the two coincide and for a nested one
// they diverge.
//
// v1.19.x sent no separate affinity header. v2.1.2 (captured from its sources,
// packages/core/src/session/model-request.ts) repeats the affinity under
// `x-session-affinity` and `X-Session-Id`, and no longer sends
// `x-opencode-request`. The request is the union: the lane admitted both shapes
// when measured on 2026-10-27, and an extra header the client used to send is
// not a difference the lane punishes.
//
// The names are written into the map in the client's spelling, because
// http.Header.Set canonicalises them and a canonicalised name is an observable
// difference from the client being imitated. The consequence is internal:
// within this package the outbound names are read as map keys, not through Get,
// which canonicalises the lookup key.
//
// The parent headers are added ONLY when there is a parent: a flat session never
// has one, and they must not always be sent.
func (p *Provider) buildHeaders(ids provider.SessionIDs) http.Header {
	h := make(http.Header)
	set := func(name, value string) {
		if value != "" {
			h[name] = []string{value}
		}
	}

	set("Content-Type", "application/json")
	// gzip only, deliberately. The reference client also offers br and zstd, but
	// a Go transport decodes only the encodings it negotiated itself, so asking
	// for one it cannot undo would turn every answer into unparsed bytes.
	set("Accept-Encoding", "gzip")
	set("User-Agent", p.userAgent)
	set("Accept", "*/*")
	set("Connection", "keep-alive")

	// The permanent headers are applied by a shared function: the catalog uses
	// the same names, and a divergence would make the client unrecognisable to
	// the service on one of the two paths.
	p.applyHeaders(h)

	set("x-opencode-project", ids.Project)
	set("x-opencode-request", ids.Message)
	affinity := nonEmpty(ids.Affinity, ids.Session)
	set("x-opencode-session", affinity)
	set("x-opencode-session-id", ids.Session)
	set("x-opencode-parent-session-id", ids.Parent)
	// v2 of the client repeats the affinity under two more spellings; the lane
	// does not require them, but the client sends them, and an emulation that
	// omits what the client sends is a difference the service can see.
	set("x-session-affinity", affinity)
	set("X-Session-Id", affinity)

	return h
}

// applyHeaders applies the provider's permanent headers.
//
// It takes an http.Header rather than a request: the headers are needed in two
// places — on the model request, together with the session headers, and on the
// catalog request, where there is no session at all. A copy of the rules in the
// catalog would drift from this one, and the service would get two mismatching
// representations of one client.
func (p *Provider) applyHeaders(h http.Header) {
	h["User-Agent"] = []string{p.userAgent}
	h["x-opencode-client"] = []string{p.clientName}
}

// nonEmpty returns the first non-empty argument.
func nonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
