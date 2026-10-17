// Package openai is a provider TEMPLATE for any service speaking the
// OpenAI-compatible protocol.
//
// WHY IT EXISTS. Most free-model services (ZenMux, Freebuff and the like) accept
// the same protocol as OpenAI: a `model` field, a `messages` list, a `stream`
// stream. They differ by address, credentials and small things like the name of
// the reasoning field. So instead of a new package per service it is enough to
// configure this one.
//
// WHAT TO DO TO ADD YOUR OWN SERVICE. The procedure is as follows:
//
//  1. If the service speaks the OpenAI-compatible protocol — there is nothing
//     to write. A configuration entry is enough:
//
//     providers:
//     zenmux:
//     type: openai
//     base_url: https://zenmux.ai/api/v1
//     api_key_env: ZENMUX_API_KEY
//
//  2. If the service requires sending mandatory tools, a particular kind of
//     system prompt or its own session headers — first check whether the fields
//     below cover it (gate_tools, system_prompt, headers). Often that is
//     enough.
//
//  3. If you still need your own code — copy the opencode package wholesale: it
//     has everything that could ever be needed, and it reads as a model example.
//     Your own package implements the three methods of the provider.Provider
//     interface and nothing more.
//
// WHAT IS NOT HERE AND MUST NOT BE: retry policy, key rotation, transport. That
// is common machinery: internal/capability and internal/transport.
package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"free-ai-router/internal/provider"
	"free-ai-router/internal/provider/modelcat"
)

// Default ID. Overridden by configuration, because the same code serves several
// services and must present itself under their names.
const DefaultID provider.ID = "openai"

// Config is the provider settings.
//
// All fields are optional except BaseURL: the rest have sensible defaults, so
// that a minimal configuration consists of a single line.
type Config struct {
	// ID is the provider identifier it is visible under in the gateway.
	ID provider.ID
	// BaseURL is the service address without the path tail.
	BaseURL string
	// APIKey is the credentials. An empty string means anonymous access.
	APIKey string
	// AnonymousKey is the anonymity marker, if the service understands it. By
	// default "public".
	AnonymousKey string
	// AuthHeader is the header for the key. By default "Authorization", and the
	// value is "Bearer <key>".
	AuthHeader string
	// AuthPrefix is the prefix before the key. By default "Bearer ".
	AuthPrefix string
	// UserAgent is the User-Agent string. Empty means do not send it.
	UserAgent string
	// ReasoningField is the name of the field the response carries its thinking
	// in. By default "reasoning_content".
	ReasoningField string
	// ExtraHeaders are additional headers the service requires.
	ExtraHeaders map[string]string
	// ChatPath is the path appended to BaseURL. By default
	// "/chat/completions".
	ChatPath string
	// ModelsPath is the model catalog path. By default "/models".
	//
	// A setting rather than a constant: on services speaking this protocol the
	// catalog lives at different paths, and there is nothing to guess it from.
	ModelsPath string
	// HTTP is the client for the catalog request. Empty means the default
	// client.
	HTTP *http.Client
}

// Provider is an implementation of provider.Provider on the OpenAI-compatible
// protocol.
type Provider struct {
	cfg Config

	id             provider.ID
	baseURL        string
	anonymousKey   string
	authHeader     string
	authPrefix     string
	reasoningField string
	chatPath       string
	modelsPath     string
	extraHeaders   map[string]string
	// catalog is the model catalog obtained from the service.
	catalog *modelcat.Catalog
}

// New creates a provider from configuration.
func New(cfg Config) (*Provider, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, fmt.Errorf("openai: base_url is required")
	}

	p := &Provider{
		cfg:            cfg,
		id:             cfg.ID,
		baseURL:        strings.TrimRight(cfg.BaseURL, "/"),
		anonymousKey:   cfg.AnonymousKey,
		authHeader:     cfg.AuthHeader,
		authPrefix:     cfg.AuthPrefix,
		reasoningField: cfg.ReasoningField,
		chatPath:       cfg.ChatPath,
		extraHeaders:   cfg.ExtraHeaders,
	}

	if p.id == "" {
		p.id = DefaultID
	}
	if p.anonymousKey == "" {
		p.anonymousKey = "public"
	}
	if p.authHeader == "" {
		p.authHeader = "authorization"
	}
	if p.authPrefix == "" {
		p.authPrefix = "Bearer "
	}
	if p.reasoningField == "" {
		p.reasoningField = "reasoning_content"
	}
	if p.chatPath == "" {
		p.chatPath = "/chat/completions"
	}
	if !strings.HasPrefix(p.chatPath, "/") {
		p.chatPath = "/" + p.chatPath
	}

	p.modelsPath = cfg.ModelsPath
	if p.modelsPath == "" {
		p.modelsPath = "/models"
	}
	if !strings.HasPrefix(p.modelsPath, "/") {
		p.modelsPath = "/" + p.modelsPath
	}

	httpClient := cfg.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	p.catalog = modelcat.New(p.baseURL, p.modelsPath, httpClient, p.applyHeaders)

	return p, nil
}

// ID implements provider.Provider.
func (p *Provider) ID() provider.ID { return p.id }

// Models implements provider.Provider.
//
// The catalog is requested from the service rather than taken from
// configuration: a list in configuration would drift from the service, and the
// client would select things that do not exist.
func (p *Provider) Models(ctx context.Context) ([]provider.Model, error) {
	return p.catalog.Models(ctx)
}

// Credential returns the value for the credentials header.
func (p *Provider) Credential(key string) string {
	if key == "" {
		return p.anonymousKey
	}
	return key
}

// Build implements provider.Provider.
func (p *Provider) Build(req provider.Request) (provider.Prepared, error) {
	body, err := p.buildBody(req)
	if err != nil {
		return provider.Prepared{}, err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return provider.Prepared{}, fmt.Errorf("openai: encoding body: %w", err)
	}

	headers := make(http.Header)
	headers.Set("content-type", "application/json")
	p.applyHeaders(headers)

	return provider.Prepared{
		URL:               p.baseURL + p.chatPath,
		Headers:           headers,
		Body:              raw,
		IdleTimeout:       idleTimeoutFor(req.Model),
		FirstEventTimeout: firstEventTimeout,
	}, nil
}

// buildBody assembles the request body.
//
// No mandatory tools or system prompts are substituted here: ordinary services
// have no such check. If a particular service needs them, that is a sign it
// deserves its own package — the way opencode has one.
func (p *Provider) buildBody(req provider.Request) (map[string]any, error) {
	messages, err := p.buildMessages(req)
	if err != nil {
		return nil, err
	}

	body := map[string]any{
		"model":    req.Model.ID,
		"messages": messages,
		"stream":   true,
	}
	if req.Stream {
		body["stream_options"] = map[string]any{"include_usage": true}
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
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, toolEntry(t))
		}
		body["tools"] = tools
		if req.ToolChoice != "" {
			body["tool_choice"] = req.ToolChoice
		}
	}
	return body, nil
}

// buildMessages translates the neutral messages into the protocol form.
func (p *Provider) buildMessages(req provider.Request) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(req.Messages)+1)
	if req.System != "" {
		out = append(out, map[string]any{"role": "system", "content": req.System})
	}

	for _, m := range req.Messages {
		switch m.Role {
		case "assistant":
			if m.Text == "" && m.Reasoning == "" && len(m.ToolCalls) == 0 {
				continue // an empty assistant turn is not allowed on the wire
			}
			msg := map[string]any{"role": "assistant", "content": m.Text}
			if m.Reasoning != "" {
				msg[p.reasoningField] = m.Reasoning
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
			out = append(out, msg)

		case "tool":
			content := m.Text
			if content == "" {
				content = "(no output)"
			}
			out = append(out, map[string]any{
				"role":         "tool",
				"tool_call_id": m.ToolCallID,
				"content":      content,
			})

		default:
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

// toolEntry translates a tool declaration into the protocol form.
func toolEntry(t provider.Tool) map[string]any {
	var schema any
	if len(t.Schema) > 0 {
		if err := json.Unmarshal(t.Schema, &schema); err != nil {
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

// Stream budgets. They match the other providers: a mismatch would make the
// gateway break the connection before the client.
const (
	firstEventTimeout    = 30 * time.Second
	chatIdleTimeout      = 120 * time.Second
	reasoningIdleTimeout = 300 * time.Second
)

// idleTimeoutFor chooses the silence budget by the model's capabilities.
func idleTimeoutFor(m provider.Model) time.Duration {
	if m.Reasoning {
		return reasoningIdleTimeout
	}
	return chatIdleTimeout
}

// applyHeaders applies the provider's permanent headers.
//
// It takes an http.Header rather than a request: the headers are needed both on
// the model request and on the catalog request. A copy of the rules in the
// catalog would drift from this one, and the service would get two mismatching
// representations of one client.
func (p *Provider) applyHeaders(h http.Header) {
	if p.cfg.UserAgent != "" {
		h.Set("user-agent", p.cfg.UserAgent)
	}
	for k, v := range p.extraHeaders {
		h.Set(k, v)
	}
}
