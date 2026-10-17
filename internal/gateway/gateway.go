// Package gateway is the OpenAI surface the client sees.
//
// WHAT IS CENTRAL HERE. The gateway is a TRANSPARENT MIDDLEMAN. It does what
// the client would have done on a direct call, and adds no behaviour on top.
// Hence two rules to which the whole package is subject:
//
//  1. A REFUSAL IS PASSED THROUGH VERBATIM. The status, body and headers reach
//     the client byte for byte. The client parses the body itself: it looks
//     there for `error.type`, `error.code`, nested codes and markers, and
//     decides about a retry from them. Any repackaging would deprive it of
//     those signals.
//
//  2. THE GATEWAY DOES NOT REPEAT REQUESTS. The client has its own retry
//     policy, worked out in detail: an exponent with a ceiling, up to ten
//     times, with a limit on timeouts. A second policy on top of it would give
//     thirty requests where the client counted on ten. The only exception is a
//     key switch: the client has no keys, the gateway does, and from the
//     client's side it is one request.
package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"free-ai-router/internal/capability"
	"free-ai-router/internal/provider"
	"free-ai-router/internal/state"
)

// Limits. The request body is bounded, because the client can send anything,
// and memory is not infinite.
const (
	maxBodyBytes = 4 << 20 // 4 MiB
)

// Handler is the HTTP handler of the OpenAI surface.
type Handler struct {
	// Providers is the registry of the available providers.
	Providers *provider.Registry
	// Transport hands out the RoundTripper for requests.
	Transport RoundTripperFactory
	// State is the counters.
	State *state.Store
	// Log is the log.
	Log *state.Logger

	// DefaultProvider is the provider used when the client did not name one.
	// Empty means the first in the registry.
	DefaultProvider provider.ID

	// KeyRotation stores the key rotation mechanisms by provider. Empty means
	// anonymous operation: the provider will substitute its own anonymity
	// marker, and that is a normal mode, not a missing setting.
	KeyRotation map[string]*capability.KeyRotation

	// Client is the HTTP client. It is set by a field rather than created per
	// request: the client holds a connection pool, and losing it is not
	// acceptable.
	Client *http.Client
}

// RoundTripperFactory hands out the transport for requests.
//
// An interface rather than a concrete type: the gateway does not need to know
// how many transports exist or how they are arranged.
type RoundTripperFactory interface {
	RoundTripper() (http.RoundTripper, error)
}

// Routes registers the routes on the given mux.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("/v1/models", h.handleModels)
	mux.HandleFunc("/v1/chat/completions", h.handleChat)
}

// ProviderRoute is the prefix for choosing the provider right in the path.
//
// Form: /p/<provider>/v1/chat/completions. It is needed when the client can
// address several routes: then every provider is visible as a separate
// endpoint, and switching between them can happen on the client's side.
const ProviderRoute = "/p/"

// routes registers both kinds of routes.
func (h *Handler) AllRoutes(mux *http.ServeMux) {
	h.Routes(mux)
	mux.HandleFunc(ProviderRoute, h.handleProviderRoute)
}

// resolveClient returns the HTTP client.
func (h *Handler) resolveClient() *http.Client {
	if h.Client != nil {
		return h.Client
	}
	return http.DefaultClient
}

// resolveProvider chooses the provider for a request.
//
// The order: an explicit name in the request body, then the name in the path,
// then the default provider. An explicit name in the body is respected, because
// the client may send a `model` of the form "provider/model" — that is a common
// shape, and it is better to understand it than to reject it.
func (h *Handler) resolveProvider(body map[string]any, pathID provider.ID) (provider.Provider, string, error) {
	if pathID != "" {
		p, err := h.Providers.Get(pathID)
		if err != nil {
			return nil, "", err
		}
		model, _ := body["model"].(string)
		return p, stripProviderPrefix(string(model), pathID), nil
	}

	rawModel, _ := body["model"].(string)
	if name, rest := splitProviderPrefix(rawModel); name != "" {
		p, err := h.Providers.Get(provider.ID(name))
		if err != nil {
			return nil, "", fmt.Errorf("unknown provider %q in the model field", name)
		}
		return p, rest, nil
	}

	if h.DefaultProvider != "" {
		p, err := h.Providers.Get(h.DefaultProvider)
		if err != nil {
			return nil, "", err
		}
		return p, rawModel, nil
	}

	all := h.Providers.All()
	if len(all) == 0 {
		return nil, "", fmt.Errorf("no providers configured")
	}
	return all[0], rawModel, nil
}

// splitProviderPrefix parses the form "provider/model".
//
// The separator is looked for up to the first slash; a provider name cannot
// contain a slash, while model identifiers often do (for instance
// "poolside/laguna-s-2.1:free"), so the first part is checked against the
// registry by the caller.
func splitProviderPrefix(model string) (name, rest string) {
	i := strings.Index(model, "/")
	if i <= 0 || i == len(model)-1 {
		return "", ""
	}
	return model[:i], model[i+1:]
}

// stripProviderPrefix strips the provider prefix from the model name.
func stripProviderPrefix(model string, id provider.ID) string {
	prefix := string(id) + "/"
	if strings.HasPrefix(model, prefix) {
		return strings.TrimPrefix(model, prefix)
	}
	return model
}

// handleProviderRoute parses a path of the form /p/<provider>/v1/...
func (h *Handler) handleProviderRoute(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, ProviderRoute)
	slash := strings.Index(rest, "/")
	if slash <= 0 {
		http.NotFound(w, r)
		return
	}
	id := provider.ID(rest[:slash])
	inner := rest[slash:]

	switch inner {
	case "/v1/models":
		h.handleModelsFor(w, r, id)
	case "/v1/chat/completions":
		h.handleChatFor(w, r, id)
	default:
		http.NotFound(w, r)
	}
}

// handleModels answers with the list of models of all providers.
func (h *Handler) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "not supported",
			"GET /v1/models: the method must be GET")
		return
	}
	h.serveModels(w, r, "")
}

// handleModelsFor answers with the list of models of one provider.
func (h *Handler) handleModelsFor(w http.ResponseWriter, r *http.Request, id provider.ID) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "not supported",
			"GET /v1/models: the method must be GET")
		return
	}
	if _, err := h.Providers.Get(id); err != nil {
		writeError(w, http.StatusNotFound, "provider not found", err.Error())
		return
	}
	h.serveModels(w, r, id)
}

// serveModels assembles the list of models.
//
// The format is the OpenAI list. The model identifier is composite:
// "provider/model", otherwise two models with the same name at different
// providers would be indistinguishable, and the client could not pick the one
// it needs.
func (h *Handler) serveModels(w http.ResponseWriter, r *http.Request, only provider.ID) {
	type modelEntry struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}

	var entries []modelEntry
	for _, p := range h.Providers.All() {
		if only != "" && p.ID() != only {
			continue
		}
		models, err := p.Models(r.Context())
		if err != nil {
			h.warn("provider %q: the catalog is unavailable: %v", p.ID(), err)
			continue
		}
		for _, m := range models {
			entries = append(entries, modelEntry{
				ID:      string(p.ID()) + "/" + m.ID,
				Object:  "model",
				Created: modelsCreatedAt,
				OwnedBy: string(p.ID()),
			})
		}
	}

	if entries == nil {
		entries = []modelEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   entries,
	})
}

// modelsCreatedAt is a constant creation marker for the model list. The list is
// static, so inventing a new time every time would be dishonest.
const modelsCreatedAt int64 = 1759718400

// handleChat handles a request to a model.
func (h *Handler) handleChat(w http.ResponseWriter, r *http.Request) {
	h.handleChatFor(w, r, "")
}

// handleChatFor handles a request, addressing it to a particular provider where
// necessary.
func (h *Handler) handleChatFor(w http.ResponseWriter, r *http.Request, pathID provider.ID) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "not supported",
			"POST /v1/chat/completions: the method must be POST")
		return
	}

	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request", "reading the body: "+err.Error())
		return
	}

	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request", "parsing the body: "+err.Error())
		return
	}

	p, modelID, err := h.resolveProvider(body, pathID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "provider not found", err.Error())
		return
	}
	if modelID == "" {
		writeError(w, http.StatusBadRequest, "invalid request", "the model field is required")
		return
	}

	model, err := h.resolveModel(r, p, modelID)
	if err != nil {
		writeError(w, http.StatusNotFound, "model not found", err.Error())
		return
	}

	req, err := h.buildRequest(r, body, p, model)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request", err.Error())
		return
	}

	h.forward(w, r, p, req, model)
}

// resolveModel finds a model in the provider's catalog.
//
// A model that is not in the catalog is rejected: the catalog is what the
// provider has declared, and sending a request to an unknown model would mean
// getting a refusal later and a less understandable one.
func (h *Handler) resolveModel(r *http.Request, p provider.Provider, id string) (provider.Model, error) {
	models, err := p.Models(r.Context())
	if err != nil {
		return provider.Model{}, fmt.Errorf("the provider catalog is unavailable: %w", err)
	}
	for _, m := range models {
		if m.ID == id {
			return m, nil
		}
	}
	return provider.Model{}, fmt.Errorf("provider %q does not declare model %q", p.ID(), id)
}

// buildRequest translates the client request into the provider's shape.
func (h *Handler) buildRequest(r *http.Request, body map[string]any, p provider.Provider, model provider.Model) (provider.Request, error) {
	messages, err := parseMessages(body["messages"])
	if err != nil {
		return provider.Request{}, err
	}
	tools, err := parseTools(body["tools"])
	if err != nil {
		return provider.Request{}, err
	}

	ids := h.sessionIDs(r, body)

	req := provider.Request{
		Model:    model,
		Messages: messages,
		System:   systemFrom(body),
		Tools:    tools,
		// Stream is always true: the upstream is obliged to stream, because
		// passing the stream to the client rests on it.
		Stream:  true,
		Session: ids,
	}

	if raw, ok := body["tool_choice"].(string); ok {
		req.ToolChoice = raw
	}
	if v, ok := body["max_tokens"].(float64); ok && v > 0 {
		req.MaxTokens = int(v)
	}
	if v, ok := body["temperature"].(float64); ok {
		req.Temperature = &v
	}
	if v, ok := body["reasoning_effort"].(string); ok {
		req.ReasoningEffort = v
	}

	return req, nil
}

// forward sends the request to the provider and passes the response to the
// client.
func (h *Handler) forward(w http.ResponseWriter, r *http.Request, p provider.Provider, req provider.Request, model provider.Model) {
	prepared, err := p.Build(req)
	if err != nil {
		h.warn("provider %q: assembling the request: %v", p.ID(), err)
		writeError(w, http.StatusInternalServerError, "gateway error", err.Error())
		h.recordRefusal(model.ID, "")
		return
	}

	// The gateway substitutes the credentials: the client has no keys.
	key := h.credentialFor(p)
	h.applyCredential(p, prepared, key)

	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost, prepared.URL, bytes.NewReader(prepared.Body))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "gateway error", err.Error())
		return
	}
	upstream.Header = prepared.Headers

	resp, err := h.do(upstream, p)
	if err != nil {
		// The upstream is unreachable. This is NOT a provider refusal, and
		// presenting it as one is not allowed: the client must see a transport
		// failure.
		h.warn("provider %q: connection: %v", p.ID(), err)
		writeError(w, http.StatusBadGateway, "transport", "the provider is unreachable: "+err.Error())
		h.recordRefusal(model.ID, h.fingerprint(key))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		h.passThroughError(w, r, p, resp, model, key)
		return
	}

	h.passThroughStream(w, resp)
	h.recordSuccess(model.ID, h.fingerprint(key))
}

// do performs the request through the gateway's transport.
func (h *Handler) do(req *http.Request, p provider.Provider) (*http.Response, error) {
	client := *h.resolveClient()
	if h.Transport != nil {
		if rt, err := h.Transport.RoundTripper(); err == nil && rt != nil {
			client.Transport = rt
		}
	}
	return client.Do(req)
}

// passThroughError passes the refusal to the client VERBATIM.
//
// This is where the package's central rule lives. Neither the status, nor the
// body, nor the headers change. The client looks in the body for the signals it
// decides by: `error.type`, `error.code`, nested codes, the upstream code
// marker in a rewritten message. Repackaging would deprive it of those signals,
// and the decision would diverge from the one the client would have made on a
// direct call.
func (h *Handler) passThroughError(w http.ResponseWriter, r *http.Request, p provider.Provider, resp *http.Response, model provider.Model, key string) {
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if readErr != nil {
		writeError(w, http.StatusBadGateway, "transport", "reading the response: "+readErr.Error())
		return
	}

	failure := p.Classify(resp, body)

	// The headers that influence the client's decision are forwarded as they
	// are. retry-after and retry-after-ms the client reads itself.
	for _, name := range []string{"retry-after", "retry-after-ms", "content-type"} {
		if v := resp.Header.Get(name); v != "" {
			w.Header().Set(name, v)
		}
	}
	if w.Header().Get("content-type") == "" {
		w.Header().Set("content-type", "application/json")
	}

	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)

	h.recordRefusal(model.ID, h.fingerprint(key))
	h.warn("provider %q: refusal %d, key rejected: %t", p.ID(), failure.Status, failure.KeyRejected)
}

// passThroughStream passes the stream to the client.
func (h *Handler) passThroughStream(w http.ResponseWriter, resp *http.Response) {
	if ct := resp.Header.Get("content-type"); ct != "" {
		w.Header().Set("content-type", ct)
	} else {
		w.Header().Set("content-type", "text/event-stream")
	}
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(resp.StatusCode)

	flusher, canFlush := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			// The stream is handed out as it arrives: a delay until the full
			// response would make streaming pointless.
			if canFlush {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

// applyCredential substitutes the credentials into the assembled request.
//
// The provider assembles the headers but does NOT know which key was issued for
// this request: the keys are issued by the shared mechanism. That is why the
// header is set here.
func (h *Handler) applyCredential(p provider.Provider, prepared provider.Prepared, key string) {
	if prepared.Headers == nil {
		return
	}
	value := credentialValue(p, key)
	if value == "" {
		return
	}
	// The header name and the prefix are part of the provider's contract, so
	// they are taken from it rather than set here.
	name, prefix := credentialShape(p)
	prepared.Headers.Set(name, prefix+value)
}

// credentialScheme describes where the provider expects the credentials.
type credentialScheme interface {
	Credential(key string) string
}

// credentialValue asks the provider what to put in the header.
func credentialValue(p provider.Provider, key string) string {
	if cs, ok := p.(credentialScheme); ok {
		return cs.Credential(key)
	}
	if key == "" {
		return "public"
	}
	return key
}

// credentialShape returns the header name and the value prefix.
//
// A provider that cares about this reports the shape itself; the rest get the
// standard one.
func credentialShape(p provider.Provider) (name, prefix string) {
	if shaped, ok := p.(interface{ CredentialHeader() (string, string) }); ok {
		return shaped.CredentialHeader()
	}
	return "authorization", "Bearer "
}

// warn writes a warning to the log, if there is one.
func (h *Handler) warn(format string, args ...any) {
	if h.Log != nil {
		h.Log.Warn(format, args...)
	}
}

// recordSuccess records a successful request.
func (h *Handler) recordSuccess(model, key string) {
	if h.State != nil {
		h.State.RecordSuccess(model, key)
	}
}

// recordRefusal records a refusal.
func (h *Handler) recordRefusal(model, key string) {
	if h.State != nil {
		h.State.RecordRefusal(model, key)
	}
}

// writeJSON answers with a JSON body.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError answers with the OpenAI error envelope.
//
// The envelope is used ONLY for the gateway's own errors: an invalid request,
// an unreachable upstream, an unknown model. Provider refusals do not GO
// through it — they are passed on verbatim (see passThroughError).
//
// type and code carry the same string: the client has one vocabulary to branch
// on, and a second, different one would force it to keep a mapping between
// them.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    code,
			"code":    code,
		},
	})
}

// Timeout suggests to the caller a silence budget for the request.
//
// The provider declares it itself: reasoning models hold pauses noticeably
// longer than ordinary ones, and a shared budget would tear their stream.
func Timeout(prepared provider.Prepared, fallback time.Duration) time.Duration {
	if prepared.IdleTimeout > 0 {
		return prepared.IdleTimeout
	}
	return fallback
}

// fingerprint returns a key fingerprint for the counters.
//
// The key itself never reaches the state: it is a secret, and a short
// fingerprint is enough to answer the question "what has been spent".
func (h *Handler) fingerprint(key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:4])
}
