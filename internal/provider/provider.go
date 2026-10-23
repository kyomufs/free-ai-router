// Package provider describes what a "free model provider" is and how the
// gateway talks to one.
//
// SEPARATION OF RESPONSIBILITIES is the core of this package. A provider does
// NOT describe its own retry policy, key rotation or transport. It answers
// exactly three questions:
//
//  1. What are its models called and what can they do?  → Models()
//  2. How is a request to it built from the common description? → Build()
//  3. How do we tell that it answered with a refusal?   → Classify()
//
// Everything else is the common gateway machinery, and it is the same for every
// provider:
//
//	key rotation        internal/capability/keyrotation
//	refusal and its forwarding internal/gateway
//	transport           internal/transport
//
// This split is needed because BYPASS MECHANISMS often coincide across
// dissimilar providers. Key rotation works the same for any provider that hands
// out keys. Rewriting the request shape is needed by everyone who filters
// clients. Proxying traffic through a third-party server does not depend on the
// provider at all. If every provider carried its own copy of that code,
// similar providers would have to duplicate it, and a fix would drift across
// the copies.
//
// The converse holds too: whatever is specific must stay with the provider. The
// set of mandatory tools, the shape of the system prompt, the shape of the
// identifiers and the form of the session headers are properties of a
// particular check, not of the general nature of things.
package provider

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"
)

// ID identifies one provider. It appears in configuration, in the control API,
// in the CLI, and as the DSH route name, so it must be stable and lowercase:
// changing it renames a user-visible route.
type ID string

// Model is one model a provider offers.
//
// The fields describe the model, not how to call it: what it is called, how much
// it holds and what it can do. How to call it is decided by Build().
type Model struct {
	// ID is the identifier in the provider's terms; it goes into the "model" field.
	ID string
	// Name is the display name.
	Name string
	// Description is a short description for interfaces.
	Description string
	// Context is the window size in tokens.
	Context int
	// MaxOutput is the response ceiling in tokens.
	MaxOutput int
	// Vision reports whether the model accepts images.
	Vision bool
	// Reasoning reports whether the model can reason.
	Reasoning bool
	// ReasoningOptions is the declared effort ladder. Empty means the model
	// either does not reason or accepts a free form.
	ReasoningOptions []ReasoningOption
	// ReasoningField is the field the response carries its thinking in
	// (for example "reasoning_content"). Empty means thinking is not separated.
	ReasoningField string
	// Free reports whether the model is declared free. Used only for filtering
	// in listings and never affects request behaviour.
	Free bool
}

// ReasoningOption is one form of controlling reasoning effort.
//
// The forms come from model catalogs and recur across providers, so they are
// described by types rather than by a string.
type ReasoningOption struct {
	// Kind is toggle, effort or budget_tokens.
	Kind ReasoningOptionKind
	// Values are the allowed values for Kind == ReasoningEffort.
	Values []string
	// Max is the ceiling for Kind == ReasoningBudget.
	Max int
}

// ReasoningOptionKind distinguishes the forms of effort control.
type ReasoningOptionKind string

const (
	// ReasoningToggle — reasoning is switched on and off.
	ReasoningToggle ReasoningOptionKind = "toggle"
	// ReasoningEffort — effort is set by a word from Values.
	ReasoningEffort ReasoningOptionKind = "effort"
	// ReasoningBudget — effort is set by a number of tokens.
	ReasoningBudget ReasoningOptionKind = "budget_tokens"
)

// Request is the common description of a request that the gateway hands to a
// provider.
//
// The gateway assembles this structure from the client request and the model
// metadata; the provider turns it into body bytes and a set of headers. The
// provider sees exactly what it needs and knows nothing about the shape of the
// client request.
type Request struct {
	// Model is the resolved model (not the client's original string).
	Model Model
	// Messages are the messages in neutral form.
	Messages []Message
	// System is the system part, already assembled by the gateway.
	System string
	// Tools are the tools the client declared.
	Tools []Tool
	// ToolChoice is the requirement for tool selection. Empty means "do not
	// send the field": that is exactly what the opencode client does when there
	// are no tools.
	ToolChoice string
	// MaxTokens is the allowance for the response.
	MaxTokens int
	// Temperature is the temperature, if the client set one.
	Temperature *float64
	// ReasoningEffort is the requested effort after clamping.
	ReasoningEffort string
	// Stream is always true: the gateway works in streaming mode.
	Stream bool
	// Session are the session identifiers the provider may put into headers.
	// The provider decides whether it needs them.
	Session SessionIDs
}

// Message is one message in neutral form.
//
// The neutral form exists so that a provider does not parse the client request:
// the gateway reduces it to this shape once, and each provider translates it
// into its own.
type Message struct {
	// Role is system, user, assistant or tool.
	Role string
	// Text is the text part.
	Text string
	// Reasoning is the assistant's thinking, if any.
	Reasoning string
	// ToolCalls are the tool calls the assistant made.
	ToolCalls []ToolCall
	// ToolCallID is the identifier of the call that the tool-role message answers.
	ToolCallID string
	// Images are the images in the message.
	Images []Image
}

// ToolCall is a tool call made by the assistant.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// Image is an image attachment.
type Image struct {
	MediaType string
	Data      []byte
}

// Tool is a tool declaration.
type Tool struct {
	Name        string
	Description string
	// Schema is the JSON schema of the parameters, already serialized.
	Schema []byte
}

// SessionIDs are the identifiers derived by the gateway from the client
// contract.
//
// A provider is free to ignore them; those that need them put them into their
// own headers.
type SessionIDs struct {
	// Session is the identifier of this session.
	Session string
	// Affinity is the identifier of the group root for cache routing.
	Affinity string
	// Project is the project identifier.
	Project string
	// Message is the identifier of the message being handled.
	Message string
	// Parent is the identifier of the parent session, empty for a flat one.
	Parent string
	// SessionMinted reports that Session was minted locally rather than taken
	// from the client: the client sent nothing, or sent a shape the service
	// refuses. A provider whose service tracks sessions may want to make such
	// an identifier stable, and this flag is how it tells the two apart.
	SessionMinted bool
}

// Prepared is the result of building a request.
//
// The provider returns what has to be sent and nothing more. Transport, retry
// and refusal classification are not its business.
type Prepared struct {
	// URL is the full address to send the request to.
	URL string
	// Headers are the request headers in full. The gateway does not add to
	// them: the set of headers is itself part of the provider contract.
	Headers http.Header
	// Body is the serialized body.
	Body []byte
	// StreamsUpstream marks that the body asks for an event stream even when
	// the client asked for a single response.
	//
	// It exists because a provider's check can require the stream form on the
	// wire while the client is entitled to the shape it requested (measured on
	// opencode: a body with stream: false is refused with FreeTierError before
	// the model is even chosen). The gateway honours the flag by collecting the
	// events into one response, so the client still gets what it asked for.
	StreamsUpstream bool
	// IdleTimeout is the stream silence budget for this request. Zero means the
	// gateway-wide budget.
	IdleTimeout time.Duration
	// FirstEventTimeout is the budget until the first headers. Zero means the
	// gateway-wide one.
	FirstEventTimeout time.Duration
}

// Failure is a classified provider refusal.
//
// A refusal is described MINIMALLY: what the provider answered and whether it
// is repeatable. What to do about it is decided by the common machinery (key
// rotation) and, ultimately, by the client.
type Failure struct {
	// Status is the code the provider returned.
	Status int
	// Body is the response body VERBATIM. The gateway passes it to the client
	// unchanged, because the client parses its fields itself.
	Body []byte
	// Headers are the response headers. They are needed in full: retry-after
	// and the like influence the client's decision.
	Headers http.Header
	// RetryAfter is the parsed retry-after, if there was one. A separate field
	// is needed by key rotation: it decides from it whether to wait or switch
	// keys.
	RetryAfter time.Duration
	// KeyRejected marks that the refusal was caused specifically by the
	// credentials.
	//
	// This judgement exists for the sake of key rotation: switching a key is
	// meaningful only when a key has been rejected.
	KeyRejected bool
	// Retryable marks that the same request, sent again after a wait, may
	// succeed.
	//
	// A SEPARATE JUDGEMENT from KeyRejected, and it must not be folded into it:
	// the two are answered by different machinery. A rejected key is answered by
	// taking another one; a "not now" is answered by waiting. A refusal may be
	// either, neither, or in principle both, and the provider is the only party
	// that can tell which — the same status carries different meanings at
	// different services.
	//
	// A provider that leaves this false is saying that a repeat is pointless,
	// which is the safe reading: repeating a refusal that will not change only
	// spends the client's time.
	Retryable bool
	// UpstreamUnavailable marks that the provider's own edge could not reach the
	// model host, so the model — not the key, not the request, not the address —
	// is what is out.
	//
	// It is a separate judgement from Retryable because it answers a different
	// question: a repeat of the SAME request is what Retryable allows, while this
	// one says the same request is addressed to a model that cannot answer at
	// all, and that moving the request to ANOTHER model is what may help. On
	// opencode both were measured live: with one free model refusing this way,
	// four other free models of the same provider answered 200 in the same
	// second.
	UpstreamUnavailable bool
	// AddressRejected marks that the refusal concerns the SOURCE ADDRESS of the
	// request — the provider does not want traffic from this network at all.
	//
	// A judgement of its own, and the only one that a different exit can answer:
	// another key does not help, waiting does not help, another model does not
	// help, and repeating the same request from the same address is exactly what
	// the refusal asks one not to do. Consumed by the pool transport, which
	// promotes servers by this verdict; inert for every other caller.
	AddressRejected bool
}

// Provider is what a particular free-model service implements.
type Provider interface {
	// ID returns the stable identifier.
	ID() ID
	// Models returns the catalog. Called at startup and on demand; the network
	// is not assumed but is not forbidden either — a provider may cache the
	// result.
	Models(ctx context.Context) ([]Model, error)
	// Build assembles a request to the provider.
	Build(req Request) (Prepared, error)
	// Classify parses a provider refusal.
	//
	// Called only for non-2xx statuses. A provider may fail to recognise a
	// refusal: then it returns a Failure with KeyRejected == false, and the
	// gateway simply forwards the response to the client.
	Classify(resp *http.Response, body []byte) Failure
}

// Local is what a provider implements when it answers requests ITSELF instead of
// sending them anywhere.
//
// An optional interface, deliberately separate from Provider: a service is the
// normal case and must not be made to say so. A provider that offers a transport is
// saying "do not dial the address I named — hand the request to me", and the gateway
// then makes no exception for it anywhere else. The classification, the counters,
// the pass-through and the stream loop all run unchanged, which is the point: what
// is being exercised has to be the real path.
//
// It exists for the stub, whose whole purpose is to make the gateway runnable and
// observable without a network and without a key. A provider for a real service has
// no reason to implement it.
type Local interface {
	// RoundTripper returns the transport that answers this provider's requests.
	// Nil means the provider is not, in the end, local.
	RoundTripper() http.RoundTripper
}

// Polling is what a provider implements when it keeps its catalog fresh in the
// background.
//
// Optional, on the same reasoning as Local: a provider whose catalog is a
// constant has nothing to poll, and forcing it to say so would add a method
// that only ever returns. A provider that implements it promises that Models
// reads from memory and never touches the network, which is what lets the
// gateway call Models on the request path without paying for it.
type Polling interface {
	// Start begins refreshing and returns once the first read is done.
	Start(ctx context.Context)
	// Stop ends the refresher.
	Stop() error
}

// IdentifierSource is what a provider implements when the service it talks to
// validates the shape of the identifiers a client sends in its headers.
//
// Optional, on the same reasoning as Local and Polling: a service that ignores
// those headers must not be made to answer for them. The gateway asks this
// interface and hands an empty set to a provider that does not implement it —
// it derives nothing on its own, because a value minted centrally would carry
// one service's naming convention into every other one, and the next service
// with a different convention could not be expressed at all.
type IdentifierSource interface {
	// SessionIDs derives the identifiers for one request from the client's own
	// headers, replacing the values the service would refuse.
	SessionIDs(r *http.Request) SessionIDs
}

// Registry is the set of available providers, assembled from configuration.
type Registry struct {
	providers map[ID]Provider
	order     []ID
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{providers: make(map[ID]Provider)}
}

// Register adds a provider. Registering the same ID again replaces the previous
// one and keeps its position in the order — that way a hot configuration reload
// does not change the order in listings.
func (r *Registry) Register(p Provider) {
	id := p.ID()
	if _, exists := r.providers[id]; !exists {
		r.order = append(r.order, id)
		sort.SliceStable(r.order, func(i, j int) bool { return r.order[i] < r.order[j] })
	}
	r.providers[id] = p
}

// Get returns a provider by identifier.
func (r *Registry) Get(id ID) (Provider, error) {
	p, ok := r.providers[id]
	if !ok {
		return nil, fmt.Errorf("provider: %q is not configured", id)
	}
	return p, nil
}

// IDs returns the identifiers in stable order.
func (r *Registry) IDs() []ID {
	out := make([]ID, len(r.order))
	copy(out, r.order)
	return out
}

// All returns the providers in stable order.
func (r *Registry) All() []Provider {
	out := make([]Provider, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.providers[id])
	}
	return out
}

// Len reports how many providers are registered.
func (r *Registry) Len() int { return len(r.providers) }
