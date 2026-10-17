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
	"strings"

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

// DefaultBaseURL is the address of the provider's inference endpoint.
//
// Taken from the model catalog (snapshot.txt, the `api` key) rather than from
// the client's code: the catalog is what the client goes by when choosing a
// model.
const DefaultBaseURL = "https://opencode.ai/inference/v1"

// Config is the provider settings, parsed from the gateway configuration.
type Config struct {
	// BaseURL overrides the provider address.
	BaseURL string
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
}

// Provider is the provider.Provider implementation for OpenCode Zen.
type Provider struct {
	cfg Config

	baseURL      string
	anonymousKey string
	userAgent    string
	shellName    string
	osName       string
	clientName   string
	// catalog is the model catalog obtained from the service.
	catalog *modelcat.Catalog
}

// New creates a provider from configuration.
func New(cfg Config) (*Provider, error) {
	p := &Provider{cfg: cfg}

	p.baseURL = strings.TrimRight(cfg.BaseURL, "/")
	if p.baseURL == "" {
		p.baseURL = DefaultBaseURL
	}
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
	p.catalog = modelcat.New(
		p.baseURL, liveModelsPath, httpClient, p.applyHeaders,
		&metaSource{http: httpClient},
	).WithEligibility(isFree)
	return p, nil
}

// ID implements provider.Provider.
func (p *Provider) ID() provider.ID { return ID }

// Models implements provider.Provider.
//
// The catalog is taken from the service on every call — with caching inside the
// client. A list in the code is deliberately not kept here: it would drift from
// the service, and the client would select models that no longer exist.
func (p *Provider) Models(ctx context.Context) ([]provider.Model, error) {
	return p.catalog.Models(ctx)
}

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

	headers := p.buildHeaders(req.Session)

	return provider.Prepared{
		URL:     p.baseURL + "/chat/completions",
		Headers: headers,
		Body:    raw,
		// The reasoning band holds longer pauses than an ordinary stream,
		// because the model can stay silent for several minutes while still
		// working. Splitting the budgets is part of the contract, not a setting.
		IdleTimeout:       idleTimeoutFor(req.Model),
		FirstEventTimeout: firstEventTimeout,
	}, nil
}

// buildBody assembles the request body.
func (p *Provider) buildBody(req provider.Request) (map[string]any, error) {
	messages, err := p.buildMessages(req)
	if err != nil {
		return nil, err
	}

	body := map[string]any{
		"model":          req.Model.ID,
		"messages":       messages,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}

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

	// The system part is one message, rendered by the provider.
	if req.System != "" {
		out = append(out, map[string]any{"role": "system", "content": req.System})
	}

	for _, m := range req.Messages {
		switch m.Role {
		case "assistant":
			msg := map[string]any{"role": "assistant", "content": m.Text}
			if m.Reasoning != "" {
				// The field name comes from the model metadata: different
				// models expect the thinking in different places.
				field := req.Model.ReasoningField
				if field == "" {
					field = "reasoning_content"
				}
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

		default: // user, system from the client
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
// The separation of session and affinity is no detail: the client puts the
// session identifier into `x-opencode-session-id` and the affinity, i.e. the
// group root for cache routing, into `x-opencode-session`. For a flat session
// the values coincide, for a nested one they diverge.
//
// The parent headers are added ONLY when there is a parent: a flat session never
// has one, and they must not always be sent.
func (p *Provider) buildHeaders(ids provider.SessionIDs) http.Header {
	h := make(http.Header)

	h.Set("content-type", "application/json")
	// The permanent headers are applied by a shared function: the catalog uses
	// the same names, and a divergence would make the client unrecognisable to
	// the service on one of the two paths.
	p.applyHeaders(h)

	if ids.Session != "" {
		h.Set("x-opencode-session-id", ids.Session)
	}
	if ids.Session != "" || ids.Affinity != "" {
		h.Set("x-opencode-session", nonEmpty(ids.Affinity, ids.Session))
		h.Set("x-session-affinity", nonEmpty(ids.Affinity, ids.Session))
		h.Set("X-Session-Id", nonEmpty(ids.Affinity, ids.Session))
	}
	if ids.Message != "" {
		h.Set("x-opencode-request", ids.Message)
	}
	if ids.Project != "" {
		h.Set("x-opencode-project", ids.Project)
	}

	// Parent headers — only for nested sessions.
	if ids.Parent != "" {
		h.Set("x-opencode-parent-session-id", ids.Parent)
		h.Set("x-parent-session-id", ids.Parent)
	}

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
	h.Set("user-agent", p.userAgent)
	h.Set("x-opencode-client", p.clientName)
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
