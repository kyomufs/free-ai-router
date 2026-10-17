// Package stub is a provider that answers from inside the gateway.
//
// WHY IT EXISTS. Two reasons, and neither is about a real service.
//
// It makes the gateway runnable without a network. A deployment can be brought up,
// a client pointed at it, a request sent and the counters, the log and the monitor
// watched — with no key, no quota and no dependency on someone else's uptime. That
// is what a test target for a gateway has to do, and the alternative is testing
// against a real service, which spends quota and fails for reasons that have nothing
// to do with the gateway.
//
// And it exercises the SECOND family of strategies. The opencode provider answers a
// refusal by rotating keys. A service that limits the address rather than the key
// cannot be helped that way: there the answer is to wait and send the same request
// again, which is what the retry strategy it declares does. Two providers, two
// mechanisms, one gateway — and any of it can be watched without a key.
//
// HOW IT ANSWERS WITHOUT A SERVER. The flow through the gateway is an HTTP round
// trip, unchanged, and the stub supplies its own transport: the request it builds is
// handed straight back to the gateway with an answer attached, and the address it
// names is never resolved. See provider.Local — the gateway honours the transport a
// provider offers, and a provider that offers none goes over the network as usual.
//
// WHAT IT IS NOT. It is not a mock of a particular service and does not pretend to
// be one: it speaks the OpenAI-compatible protocol, and that is as far as the
// resemblance goes. It is meant to be OBVIOUS. Nothing here should be copied into a
// provider for a real service.
package stub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"free-ai-router/internal/provider"
)

// Default ID. Overridden by configuration: the section name is the provider name.
const DefaultID provider.ID = "stub"

// DefaultModel is the model served when the configuration named none.
const DefaultModel = "stub-echo"

// stubHost is the host name in the addresses the stub builds. It is deliberately
// unresolvable: a request that reaches a socket means the local transport was not
// wired in, and it is better for that to fail loudly than to be answered by
// whatever happens to sit at a plausible address.
const stubHost = "stub.invalid"

// Config is the provider's settings.
//
// The names are the ones the settings are written under in the provider's section:
//
//	providers:
//	  stub:
//	    type: stub
//	    settings:
//	      models: ["stub-echo", "stub-slow"]
//	      fail: 2                        # refuse the first two requests
//	      fail_status: 429               # with this status
//	      fail_text: "rate limit exceeded"   # and this text in the body
//	      retry_after_s: 1               # announcing this wait
//	      delay_ms: 200                  # holding every request this long
//	      retry:                         # the declared strategy
//	        attempts: 3
//	        delay_ms: 500
//
// Every setting has a default that makes the provider work: a section consisting of
// nothing but "type: stub" answers requests.
type Config struct {
	// Models — the catalog. It is DECLARED here, unlike a real provider's, for the
	// plain reason that there is no service to ask. That is a property of a stub and
	// must not be read as an invitation to declare catalogs for real services.
	Models []provider.Model
	// Fail — how many requests to refuse before answering normally. The counter is
	// per provider, so a deployment can be given a refusal to look at and then a
	// working service, in that order.
	Fail int
	// FailStatus — the status of the refusal. Defaults to 429.
	FailStatus int
	// FailText — text placed in the refusal body. An empty value produces a
	// syntactically correct error object that names no reason.
	FailText string
	// RetryAfter — the wait announced in the refusal headers. Zero announces
	// nothing, which is the case a retry strategy has to handle with its own delay.
	RetryAfter time.Duration
	// Delay — how long to hold every request. It exists so that a client's timeout
	// and the monitor's stream handling can be exercised without a slow service.
	Delay time.Duration
	// NoStream — answer a streaming request with a whole response instead of a
	// stream. A deliberate misbehaviour, off by default: clients do meet services
	// that do this.
	NoStream bool
	// Reply — the assistant text of a successful answer.
	Reply string
}

// DefaultConfig returns the settings of a stub that always answers.
func DefaultConfig() Config {
	return Config{
		Models: []provider.Model{{
			ID:        DefaultModel,
			Name:      "Stub echo",
			Context:   32768,
			MaxOutput: 4096,
			Free:      true,
		}},
		FailStatus: http.StatusTooManyRequests,
		Reply:      "stub",
	}
}

// Provider is the stub itself.
//
// It satisfies provider.Provider and provider.Local at once: the first makes it
// configurable like any other provider, the second is what lets it answer without a
// server.
type Provider struct {
	id      provider.ID
	baseURL string
	cfg     Config

	mu       sync.Mutex
	refused  int
	requests int
}

// NewProvider creates a provider from the settings.
//
// id is the name of the section in the configuration. baseURL is recorded and put
// into the requests so that what the provider reports of itself matches what the
// rest of the gateway believes of it; nothing is ever dialled at it.
func NewProvider(id provider.ID, baseURL string, cfg Config) *Provider {
	if id == "" {
		id = DefaultID
	}
	if baseURL == "" {
		baseURL = "http://" + stubHost
	}
	if len(cfg.Models) == 0 {
		cfg.Models = DefaultConfig().Models
	}
	if cfg.FailStatus == 0 {
		cfg.FailStatus = http.StatusTooManyRequests
	}
	if cfg.Reply == "" {
		cfg.Reply = "stub"
	}
	return &Provider{id: id, baseURL: strings.TrimRight(baseURL, "/"), cfg: cfg}
}

// ID implements provider.Provider.
func (p *Provider) ID() provider.ID { return p.id }

// Models implements provider.Provider.
//
// A copy, so that a caller cannot write into the catalog the provider serves from.
func (p *Provider) Models(context.Context) ([]provider.Model, error) {
	out := make([]provider.Model, len(p.cfg.Models))
	copy(out, p.cfg.Models)
	return out, nil
}

// Build implements provider.Provider.
//
// The request is assembled faithfully enough to be inspected: the model, the system
// prompt, the messages and the streaming flag all go into the body, because the point
// of a stub is to be looked at in a log or through a proxy without a real service in
// the way.
func (p *Provider) Build(req provider.Request) (provider.Prepared, error) {
	body := map[string]any{
		"model":      req.Model.ID,
		"stream":     req.Stream,
		"messages":   req.Messages,
		"max_tokens": req.MaxTokens,
	}
	if req.System != "" {
		body["system"] = req.System
	}
	if len(req.Tools) > 0 {
		body["tools"] = req.Tools
		if req.ToolChoice != "" {
			body["tool_choice"] = req.ToolChoice
		}
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return provider.Prepared{}, fmt.Errorf("stub: serializing the request: %w", err)
	}

	return provider.Prepared{
		URL:     p.baseURL + "/chat/completions",
		Headers: http.Header{"Content-Type": []string{"application/json"}},
		Body:    encoded,
	}, nil
}

// Classify implements provider.Provider.
//
// The verdicts are read off the status, and that is the whole point: a stub must be
// predictable, so that what the gateway does next is a property of the gateway and
// not of a clever fixture. 401 and 403 name the key; 429 and 5xx ask for a repeat;
// a quota named in the text outranks both, because an exhausted allowance is not
// restored by waiting.
func (p *Provider) Classify(resp *http.Response, body []byte) provider.Failure {
	f := provider.Failure{
		Status:  resp.StatusCode,
		Body:    body,
		Headers: resp.Header,
	}

	if v := resp.Header.Get("retry-after"); v != "" {
		if d, err := time.ParseDuration(v + "s"); err == nil {
			f.RetryAfter = d
		}
	}

	text := strings.ToLower(string(body))
	quota := strings.Contains(text, "quota") || strings.Contains(text, "insufficient")

	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		f.KeyRejected = true
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
		if quota {
			f.KeyRejected = true
		} else {
			f.Retryable = true
		}
	}

	return f
}

// RoundTripper implements provider.Local.
//
// The provider IS its own transport: it already has the method that interface
// needs, so nothing is wrapped. Returning itself is the honest answer — there is
// no other party in the exchange.
func (p *Provider) RoundTripper() http.RoundTripper { return p }

// RoundTrip implements provider.Local.
//
// THE ANSWER IS PRODUCED HERE, in the process, and returned as an HTTP response so
// that everything downstream of it — classification, counters, pass-through, the
// stream loop — is the same code that runs against a real service. A stub that
// bypassed the response would test nothing.
func (p *Provider) RoundTrip(req *http.Request) (*http.Response, error) {
	// A request may legitimately carry no body — the interface is http.RoundTripper,
	// and not every method has one. Reading unconditionally would panic on a nil
	// reader, which is a crash rather than a refusal.
	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("stub: reading the request: %w", err)
		}
		_ = req.Body.Close()
	}

	if p.cfg.Delay > 0 {
		select {
		case <-time.After(p.cfg.Delay):
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}

	status, refused := p.Refuse()
	if refused {
		return p.refusalResponse(req, status), nil
	}

	var sent struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	_ = json.Unmarshal(body, &sent)

	if sent.Stream && !p.cfg.NoStream {
		return p.streamResponse(req, sent.Model), nil
	}
	return p.jsonResponse(req, sent.Model), nil
}

// refusalResponse builds the refusal.
func (p *Provider) refusalResponse(req *http.Request, status int) *http.Response {
	payload := map[string]any{
		"error": map[string]any{
			"message": p.cfg.FailText,
			"type":    http.StatusText(status),
			"code":    status,
		},
	}
	encoded, _ := json.Marshal(payload)

	header := http.Header{"Content-Type": []string{"application/json"}}
	if p.cfg.RetryAfter > 0 {
		header.Set("Retry-After", fmt.Sprintf("%g", p.cfg.RetryAfter.Seconds()))
	}

	return p.respond(req, status, header, encoded)
}

// jsonResponse builds an ordinary answer.
func (p *Provider) jsonResponse(req *http.Request, model string) *http.Response {
	payload := map[string]any{
		"id":      "chatcmpl-stub",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   fallbackModel(model),
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": p.cfg.Reply},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":     1,
			"completion_tokens": 1,
			"total_tokens":      2,
		},
	}
	encoded, _ := json.Marshal(payload)
	return p.respond(req, http.StatusOK, http.Header{"Content-Type": []string{"application/json"}}, encoded)
}

// streamResponse builds an SSE stream.
//
// The shape is the one a client expects from an OpenAI-compatible service: a chunk
// per delta, then a chunk carrying the finish reason, then the terminator. A
// deliberate misbehaviour is not built in here — NoStream produces the fatter
// alternative instead.
func (p *Provider) streamResponse(req *http.Request, model string) *http.Response {
	var buf bytes.Buffer
	created := time.Now().Unix()

	chunk := func(delta map[string]any, finish any) {
		event := map[string]any{
			"id":      "chatcmpl-stub",
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   fallbackModel(model),
			"choices": []any{map[string]any{
				"index":         0,
				"delta":         delta,
				"finish_reason": finish,
			}},
		}
		encoded, _ := json.Marshal(event)
		buf.WriteString("data: ")
		buf.Write(encoded)
		buf.WriteString("\n\n")
	}

	chunk(map[string]any{"role": "assistant", "content": p.cfg.Reply}, nil)
	chunk(map[string]any{}, "stop")
	buf.WriteString("data: [DONE]\n\n")

	return p.respond(req, http.StatusOK, http.Header{
		"Content-Type":  []string{"text/event-stream"},
		"Cache-Control": []string{"no-store"},
	}, buf.Bytes())
}

// respond assembles the response value.
//
// The body is already in memory, so no pipe and no goroutine is needed: an
// http.Response with a reader over the bytes is exactly what the gateway's client
// would have produced.
func (p *Provider) respond(req *http.Request, status int, header http.Header, body []byte) *http.Response {
	if req.Context().Err() != nil {
		return &http.Response{
			StatusCode: status,
			Header:     header,
			Body:       io.NopCloser(bytes.NewReader(body)),
			Request:    req,
		}
	}
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		StatusCode:    status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        header,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}
}

// fallbackModel names the model in answers to a request that named none.
func fallbackModel(model string) string {
	if model == "" {
		return DefaultModel
	}
	return model
}

// Refuse records a request and reports the status to refuse it with.
//
// Exported for tests: asserting that a refusal happened should not require going
// through an HTTP client.
func (p *Provider) Refuse() (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests++
	if p.refused < p.cfg.Fail {
		p.refused++
		return p.cfg.FailStatus, true
	}
	return 0, false
}

// Requests reports how many requests have arrived.
func (p *Provider) Requests() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests
}

// Build-time checks: the stub is obliged to satisfy both interfaces.
var (
	_ provider.Provider = (*Provider)(nil)
	_ provider.Local    = (*Provider)(nil)
)
