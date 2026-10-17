// Package modelcat asks the service for the list of models available right now.
//
// WHY THE CATALOG COMES FROM THE NETWORK AND NOT FROM THE CODE. A list written
// into the code inevitably drifts from the service: models get retired while the
// binary stays the same. Then the client sees something in the selector that no
// longer exists, picks it and gets a refusal. A refusal for a nonexistent model
// is indistinguishable from a refusal for a quota, and cannot be diagnosed. A
// catalog taken from the network has no such desynchronisation by construction:
// it physically cannot outlive a model's retirement.
//
// SEPARATION OF RESPONSIBILITIES. The package knows HOW to ask for the list and
// does not know what a particular model means. Model properties — limits,
// reasoning, image acceptance — are added by a metadata source (MetaSource).
// That way the common mechanism stays common: a service without a metadata
// catalog simply passes an empty list of sources.
package modelcat

import (
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

const (
	// catalogTimeout is the catalog wait limit.
	//
	// The catalog is requested on the path of a live request: both for the model
	// list and for validating a model before sending. A long wait here would
	// delay the request itself, so the limit is short: the catalog is either
	// there or it is not, and there is nothing worth waiting longer for.
	catalogTimeout = 8 * time.Second

	// listTTL is the list's shelf life.
	//
	// Models are added and disappear rarely, but are asked for often. Without a
	// shelf life every client request would turn into a network request to the
	// service, and the gateway would burn quota enumerating what it already
	// knows.
	listTTL = 5 * time.Minute

	// metaTTL is the shelf life of model properties. They change even more
	// rarely than the list.
	metaTTL = 6 * time.Hour

	// maxListBytes is the read limit for the list body. The list is small, and
	// the limit is only needed against an unexpectedly large response.
	maxListBytes = 4 << 20
)

// MetaSource augments a model description with properties.
//
// It is polled separately from the list and may be unavailable: it refines the
// description rather than determining the composition of the catalog. A source
// failure does not cancel the catalog — the model works without exact limits.
type MetaSource interface {
	// Meta returns model properties, keyed by model identifier.
	Meta(ctx context.Context) (map[string]provider.Model, error)
	// Name names the source for the log.
	Name() string
}

// Eligibility decides whether a model makes it into the catalog.
//
// The service's live list does not know this: it enumerates everything it
// serves and does not divide models by price. The gateway, however, exists for
// the free ones, so the selection is needed separately — and it is pushed out
// to the provider, because the "free" attribute belongs to a particular service,
// not to the common machinery.
//
// The selection is applied AFTER merging with the properties, otherwise there
// would be nothing to decide by: a live entry carries only an identifier.
//
// The predicate is handed `described`, which says whether the property source
// knew this model at all. That is not a hint but a distinction the predicate
// has to make: a model the source described and rejected IS paid, while a
// model the source has not reached is merely unproven. Those are different
// facts, and collapsing them would force every caller to guess which one it is
// looking at.
type Eligibility func(m provider.Model, described bool) bool

// Catalog is the model catalog of a service.
type Catalog struct {
	// baseURL is the service address.
	baseURL string
	// listPath is the model list path.
	listPath string
	// http is the client. Shared with the rest of the gateway so as not to spin
	// up a second connection pool to the same host.
	http *http.Client
	// headers applies the provider's permanent headers. The same ones as on the
	// model request: the service recognises the client by them, and without
	// them the catalog may be trimmed or served for a different client.
	headers func(http.Header)
	// metas are the property sources.
	metas []MetaSource
	// eligible selects models for the catalog. Empty means "take all".
	eligible Eligibility

	mu sync.Mutex
	// list is the last list and the time it was fetched.
	list   []string
	listAt time.Time
	// meta is the last properties merged from the sources.
	meta   map[string]provider.Model
	metaAt time.Time

	// fetching separates fetching from cache reads. Holding the main lock for
	// the duration of the network call would stall cache reads for every other
	// request.
	fetching sync.Mutex
}

// New creates a catalog.
func New(baseURL, listPath string, httpClient *http.Client, headers func(http.Header), metas ...MetaSource) *Catalog {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Catalog{
		baseURL:  strings.TrimRight(baseURL, "/"),
		listPath: listPath,
		http:     httpClient,
		headers:  headers,
		metas:    metas,
	}
}

// WithEligibility restricts the catalog to the models a predicate admits.
//
// A setter rather than a constructor argument: most services need no filter,
// and a parameter every caller had to pass nil for would obscure the calls
// that do. The predicate is applied to the merged model, so it may read the
// properties the sources supplied.
func (c *Catalog) WithEligibility(eligible Eligibility) *Catalog {
	c.eligible = eligible
	return c
}

// Error describes why the catalog was not obtained.
//
// A separate type rather than a string: the caller distinguishes the cases — the
// service answered with a refusal, the response was not parsed, the request was
// not performed. In the log these are different causes, and they are fixed
// differently.
type Error struct {
	// Op is what exactly was requested.
	Op string
	// Status is the response code, if there was one.
	Status int
	// Reason is a short cause.
	Reason string
}

func (e *Error) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("catalog %s: %s (HTTP %d)", e.Op, e.Reason, e.Status)
	}
	return fmt.Sprintf("catalog %s: %s", e.Op, e.Reason)
}

// Models returns the model catalog.
//
// Order: fresh cache, then the network. A network error is NOT substituted with
// the previous list. A substituted list would put models back into the selector
// that may no longer exist, i.e. it would reproduce exactly the
// desynchronisation that the catalog is taken from the network to eliminate. A
// missing catalog is an honest state, and the client must see it as missing
// rather than as a list.
func (c *Catalog) Models(ctx context.Context) ([]provider.Model, error) {
	ids, err := c.listIDs(ctx)
	if err != nil {
		return nil, err
	}

	meta := c.metaModels(ctx)

	out := make([]provider.Model, 0, len(ids))
	for _, id := range ids {
		props, known := meta[id]
		m := Describe(id, props)

		// The predicate sees every model, described or not, and decides on
		// whatever evidence exists. It is not spared the undescribed ones:
		// only the predicate knows whether a name is enough to judge by, and
		// skipping them here would take that decision away from it.
		//
		// `known` is passed in because the two cases are not equivalent. A
		// model the source described and rejected is paid; a model the source
		// has not reached yet is merely unproven, and a predicate may treat
		// those differently.
		if c.eligible != nil && !c.eligible(m, known) {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// listIDs returns the identifiers of the models available right now.
func (c *Catalog) listIDs(ctx context.Context) ([]string, error) {
	c.mu.Lock()
	if c.list != nil && time.Since(c.listAt) < listTTL {
		ids := c.list
		c.mu.Unlock()
		return ids, nil
	}
	c.mu.Unlock()

	c.fetching.Lock()
	defer c.fetching.Unlock()

	// While we were waiting in the queue, another request may have refreshed
	// the list.
	c.mu.Lock()
	if c.list != nil && time.Since(c.listAt) < listTTL {
		ids := c.list
		c.mu.Unlock()
		return ids, nil
	}
	c.mu.Unlock()

	ids, err := c.fetchList(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		// The previous list is not kept as an answer: it would look fresh.
		return nil, err
	}
	c.list, c.listAt = ids, time.Now()
	return ids, nil
}

// fetchList requests the model list.
func (c *Catalog) fetchList(ctx context.Context) ([]string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, catalogTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, c.baseURL+c.listPath, nil)
	if err != nil {
		return nil, &Error{Op: "model list", Reason: err.Error()}
	}
	if c.headers != nil {
		c.headers(req.Header)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &Error{Op: "model list", Reason: "request not performed: " + err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// The body is read and goes into the cause: the service explains the
		// refusal in it, and without the body the cause would be only in the
		// response code.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, &Error{
			Op:     "model list",
			Status: resp.StatusCode,
			Reason: strings.TrimSpace(string(body)),
		}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxListBytes))
	if err != nil {
		return nil, &Error{Op: "model list", Reason: "reading response: " + err.Error()}
	}

	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, &Error{Op: "model list", Reason: "response not parsed: " + err.Error()}
	}

	ids := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	if len(ids) == 0 {
		// An empty list means the parsing drifted from the response shape.
		// Silently returning an empty catalog would show the operator an empty
		// selector with no explanation.
		return nil, &Error{Op: "model list", Reason: "the service declared no models"}
	}
	return ids, nil
}

// metaModels collects model properties from the sources.
//
// The sources are polled in turn, each with its own shelf life. A failure of any
// of them does not interrupt the collection: properties are a refinement, and
// their absence merely means a more modest model description.
func (c *Catalog) metaModels(ctx context.Context) map[string]provider.Model {
	c.mu.Lock()
	if c.meta != nil && time.Since(c.metaAt) < metaTTL {
		m := c.meta
		c.mu.Unlock()
		return m
	}
	c.mu.Unlock()

	c.fetching.Lock()
	defer c.fetching.Unlock()

	c.mu.Lock()
	if c.meta != nil && time.Since(c.metaAt) < metaTTL {
		m := c.meta
		c.mu.Unlock()
		return m
	}
	c.mu.Unlock()

	merged := make(map[string]provider.Model)
	got := false
	for _, src := range c.metas {
		part, err := src.Meta(ctx)
		if err != nil {
			// The source did not answer: its properties simply do not take
			// part. The remaining sources are still polled.
			continue
		}
		got = true
		for id, m := range part {
			merged[id] = m
		}
	}
	if !got {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.meta, c.metaAt = merged, time.Now()
	return merged
}

// Defaults for a model without properties.
//
// The values are deliberately low. An understated limit leads to an early
// truncation of the conversation, an overstated one to a service refusal in the
// middle of a request. The first is noticeable and fixable, the second loses
// work already done.
const (
	DefaultContext        = 200000
	DefaultMaxOutput      = 32000
	DefaultReasoningField = "reasoning_content"
)

// Describe assembles a model description from an identifier and known
// properties.
//
// The properties may be missing: the service declares a model before the
// metadata catalog learns about it. Such a model IS STILL DESCRIBED — with
// defaults. Rejecting it would show the operator that the service has the model
// but it cannot be selected.
func Describe(id string, meta provider.Model) provider.Model {
	m := provider.Model{
		ID:             id,
		Name:           id,
		Context:        DefaultContext,
		MaxOutput:      DefaultMaxOutput,
		ReasoningField: DefaultReasoningField,
		// Free is copied from the properties rather than assumed. Defaulting
		// it to true would mark EVERY served model free, the paid ones
		// included, and an eligibility predicate reading this flag would then
		// admit all of them — which is exactly the failure this field exists
		// to prevent.
		Free: meta.Free,
	}
	if meta.Name != "" {
		m.Name = meta.Name
	}
	if meta.Description != "" {
		m.Description = meta.Description
	}
	if meta.Context > 0 {
		m.Context = meta.Context
	}
	if meta.MaxOutput > 0 {
		m.MaxOutput = meta.MaxOutput
	}
	m.Vision = meta.Vision
	m.Reasoning = meta.Reasoning
	m.ReasoningOptions = meta.ReasoningOptions
	if meta.ReasoningField != "" {
		m.ReasoningField = meta.ReasoningField
	}
	return m
}
