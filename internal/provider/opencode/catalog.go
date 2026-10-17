package opencode

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"free-ai-router/internal/provider"
	"free-ai-router/internal/provider/modelcat"
)

// A source of model properties.
//
// WHY SEPARATE FROM THE LIST. The service's model list answers the question "what
// is available right now", but it reports only an identifier about a model. The
// properties — window size, image acceptance, the ability to think, the field
// for thinking in the response — live elsewhere, and that is where the service's
// clients take them from.
//
// WHY THESE TWO SOURCES ARE NOT MERGED. The list is more precise: it cannot
// outlive a model's retirement. The property catalog is fuller but lags behind:
// in its current state it declares noticeably more free models than the service
// actually serves. Merging them into one would either put dead models back into
// the selector or lose the exact limits of live ones. So the composition is
// determined by the list, and the properties merely augment what it names.

const (
	// metaURL is the property catalog.
	//
	// The address is derived from the client's sources: it is the same snapshot
	// of the models.dev catalog that the client relies on when displaying models
	// and their capabilities.
	metaURL = "https://models.opencode.ai/api.json"

	// metaTimeout is the property catalog wait limit.
	//
	// Noticeably larger than the list's: this catalog describes all services at
	// once and is therefore large. The list is awaited on the path of a live
	// request, the properties are not: without them the model is simply
	// described with defaults, and there is no reason to delay a request because
	// of them.
	metaTimeout = 30 * time.Second

	// maxMetaBytes is the read limit for the body.
	//
	// The property catalog is currently about six megabytes and grows with the
	// number of services. The limit is taken with a margin, but a finite one:
	// an unexpectedly large response must not eat memory.
	maxMetaBytes = 64 << 20
)

// metaSource reads model properties from the service's catalog.
type metaSource struct {
	// http is the client. Shared with the rest of the gateway.
	http *http.Client
	// url overrides the catalog address. Empty means the published one.
	//
	// A field rather than a constant read at the call site so that a check can
	// point the source at a local answer. Without it the source could only be
	// exercised against the real service, which makes the test depend on the
	// network and on a catalog the test cannot control.
	url string
}

// catalogURL reports the address to read the properties from.
func (s *metaSource) catalogURL() string {
	if s.url != "" {
		return s.url
	}
	return metaURL
}

// Name implements modelcat.MetaSource.
func (s *metaSource) Name() string { return "opencode property catalog" }

// metaModel is the properties of one model.
//
// Only what the gateway uses is parsed: limits, reasoning, image acceptance and
// the field for thinking in the response. The remaining catalog fields are not
// needed by the gateway, and parsing them would mean dragging someone else's
// whole schema into the code.
type metaModel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Attachment reports whether the model accepts attachments (images).
	Attachment bool `json:"attachment"`
	// Reasoning reports whether the model can think.
	Reasoning bool `json:"reasoning"`
	// ReasoningOptions is the effort control ladder.
	ReasoningOptions []struct {
		Type   string   `json:"type"`
		Values []string `json:"values"`
		Max    int      `json:"max"`
	} `json:"reasoning_options"`
	// Interleaved is where the response carries its thinking.
	//
	// The field is declared as raw: in the catalog it occurs in three forms —
	// as an object with the field name, as absent, and as FALSE. Strict parsing
	// fails on false, and a single such value would bring down the whole
	// property catalog, leaving every model without limits. The parsing is
	// therefore tolerant.
	Interleaved json.RawMessage `json:"interleaved"`
	// Modalities are the input and output modalities.
	Modalities struct {
		Input []string `json:"input"`
	} `json:"modalities"`
	// Limit is the limits in tokens.
	Limit struct {
		Context int `json:"context"`
		Output  int `json:"output"`
	} `json:"limit"`
	// Cost is the model's price per token.
	//
	// It is read not for calculations but for SELECTION: the gateway exists for
	// free models, and price is the only attribute that distinguishes them in
	// the catalog. Without it paid ones end up in the list, which is exactly
	// what happened while the field was not parsed.
	Cost struct {
		Input  float64 `json:"input"`
		Output float64 `json:"output"`
	} `json:"cost"`
}

// Meta implements modelcat.MetaSource.
//
// An error means the properties stay unknown: the catalog will describe the
// models with defaults. This is not a service refusal but a missing refinement,
// so the error does not interrupt the catalog assembly.
func (s *metaSource) Meta(ctx context.Context) (map[string]provider.Model, error) {
	reqCtx, cancel := context.WithTimeout(ctx, metaTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, s.catalogURL(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &modelcat.Error{Op: "property catalog", Status: resp.StatusCode, Reason: "the service did not serve the catalog"}
	}

	// The catalog describes all services at once. It is read in full: parsing it
	// streamingly for the sake of one section would mean reimplementing the
	// parsing by hand.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMetaBytes))
	if err != nil {
		return nil, err
	}

	var catalog map[string]struct {
		Models map[string]metaModel `json:"models"`
	}
	if err := json.Unmarshal(body, &catalog); err != nil {
		return nil, err
	}

	section, ok := catalog[string(ID)]
	if !ok {
		return nil, &modelcat.Error{Op: "property catalog", Reason: "the service section is missing"}
	}

	out := make(map[string]provider.Model, len(section.Models))
	for id, raw := range section.Models {
		out[id] = raw.toModel(id)
	}
	return out, nil
}

// toModel translates the properties into the gateway form.
func (m metaModel) toModel(id string) provider.Model {
	out := provider.Model{ID: id}

	if m.Name != "" {
		out.Name = m.Name
	}
	if m.Description != "" {
		out.Description = m.Description
	}
	if m.Limit.Context > 0 {
		out.Context = m.Limit.Context
	}
	if m.Limit.Output > 0 {
		out.MaxOutput = m.Limit.Output
	}

	// Image acceptance is declared in two ways: by a separate attribute and by
	// the list of input modalities. Either is enough — the catalog fills in
	// sometimes one, sometimes the other.
	out.Vision = m.Attachment || containsString(m.Modalities.Input, "image")

	// The reasoning field is taken from the properties: for different models the
	// response carries it in different places, and writing it to the wrong one
	// would break continuing from the model's own earlier thought.
	if field := interleavedField(m.Interleaved); field != "" {
		out.ReasoningField = field
	}

	if m.Reasoning {
		out.Reasoning = true
		out.ReasoningOptions = reasoningOptions(m.ReasoningOptions)
	}

	// Both prices are zero exactly when the service charges nothing. A model
	// is marked free only then, so that the flag means what it says and the
	// catalog filter can rely on it.
	out.Free = m.Cost.Input == 0 && m.Cost.Output == 0

	return out
}

// isFree admits only models the service charges nothing for.
//
// The gateway exists to route free models, so a paid one is not a less
// attractive choice — it is a request that bills the operator. Filtering here
// rather than in the client keeps a paid model out of every listing, and the
// listing is what the client picks from.
//
// The live list cannot answer this: it names what the service serves and says
// nothing about price. So the decision comes from the metadata source, which
// is why eligibility is applied after the merge.
//
// The name is the fallback for models the metadata source has not caught up
// with, and the two cases are genuinely different. When the source described
// the model, its price is the verdict and the name is not consulted: the
// service charges, so what it charges decides. When the source has not
// described it, the price is unknown rather than zero, and the only evidence
// left is the name the service gave it.
//
// That fallback rests on an observation rather than on a hope: across every
// model appearing in both sources, the `-free` suffix and a zero price agree
// exactly, with no model advertised as free by name and billed in fact. So a
// name is enough to ADMIT an unproven model, while a described paid model is
// still rejected. Being wrong in the admitting direction costs one refusal the
// operator can see; being wrong in the rejecting direction silently hides a
// model the service is serving.
func isFree(m provider.Model, described bool) bool {
	if described {
		return m.Free
	}
	return strings.HasSuffix(m.ID, "-free")
}

// reasoningOptions translates the effort ladder into the gateway form.
//
// There are three forms: a toggle, a ladder of values and a budget in tokens.
// The catalog may declare several at once, and all of them are preserved: the
// client picks the one it understands. An empty list is normal: some models
// declare the ability to think without describing how exactly to control it.
func reasoningOptions(in []struct {
	Type   string   `json:"type"`
	Values []string `json:"values"`
	Max    int      `json:"max"`
}) []provider.ReasoningOption {
	out := make([]provider.ReasoningOption, 0, len(in))
	for _, raw := range in {
		switch raw.Type {
		case "toggle":
			out = append(out, provider.ReasoningOption{Kind: provider.ReasoningToggle})
		case "effort":
			if len(raw.Values) > 0 {
				out = append(out, provider.ReasoningOption{
					Kind:   provider.ReasoningEffort,
					Values: append([]string(nil), raw.Values...),
				})
			}
		case "budget_tokens":
			out = append(out, provider.ReasoningOption{
				Kind: provider.ReasoningBudget,
				Max:  raw.Max,
			})
		}
	}
	return out
}

// containsString reports whether a value is in the list.
func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// interleavedField extracts the name of the reasoning field.
//
// The field occurs in the catalog in three forms: as an object with a name, as
// absent and as false. False means "thinking is not interleaved" — there is no
// property, and an empty string is returned. The parsing is deliberately silent:
// an incomprehensible value here is no reason to lose the model's other
// properties.
func interleavedField(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var parsed struct {
		Field string `json:"field"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return ""
	}
	return parsed.Field
}
