package modelcat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"free-ai-router/internal/provider"
)

// listPath is where the catalog is asked for. The path itself is not what is
// under test: the package is handed one, and the stub has to answer the same
// one so that a check reaches the parsing rather than a 404.
const listPath = "/zen/v1/models"

// newTestCatalog serves one answer for the list and returns a started catalog.
//
// The server stands in for the service because a check must not reach the
// network: the real list changes with the service, and a check that depended on
// it would fail for reasons that have nothing to do with the parsing.
//
// Start is called because reads come out of memory: an unstarted catalog has
// never read anything and reports nothing, which would make every parse check
// pass vacuously.
func newTestCatalog(t *testing.T, handler http.HandlerFunc) *Catalog {
	t.Helper()
	return newTestCatalogWithLog(t, handler, nil)
}

// newTestCatalogWithLog is newTestCatalog with somewhere for the poller's
// warnings to go.
func newTestCatalogWithLog(t *testing.T, handler http.HandlerFunc, log func(string, ...any)) *Catalog {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := New(srv.URL, listPath, srv.Client(), nil).WithLog(log)
	c.Start(t.Context())
	t.Cleanup(func() { _ = c.Stop() })
	return c
}

// serveList answers every request with the given status and body.
func serveList(t *testing.T, status int, body string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != listPath {
			t.Errorf("the catalog was requested at %q, want %q", r.URL.Path, listPath)
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

// TestWellFormedListBecomesTheCatalog — the identifiers the service declares are
// the models the catalog reports, in the order the service declared them.
//
// Nothing else decides composition: a list in the code would drift from the
// service, and a client would end up selecting models that no longer exist.
func TestWellFormedListBecomesTheCatalog(t *testing.T) {
	c := newTestCatalog(t, serveList(t, http.StatusOK,
		`{"data":[{"id":"alpha-free"},{"id":"beta-free","owned_by":"zen"}]}`))

	models, err := c.Models(t.Context())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 2 || models[0].ID != "alpha-free" || models[1].ID != "beta-free" {
		t.Fatalf("Models = %+v, want exactly what the service declared, in order", models)
	}
}

// TestUndeclaredPropertiesBecomeDefaults — a model the service serves but no
// property source has described must still arrive described, with defaults.
//
// Omitting it would tell the operator the model is unavailable, which is the
// opposite of what the list says. Reporting zero would be worse still: the zeros
// mean "not declared" everywhere in the project, and a client reading them as
// real limits would clamp every request to nothing.
func TestUndeclaredPropertiesBecomeDefaults(t *testing.T) {
	c := newTestCatalog(t, serveList(t, http.StatusOK, `{"data":[{"id":"fresh-free"}]}`))

	models, err := c.Models(t.Context())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("Models = %+v, want the undeclared model kept", models)
	}
	m := models[0]

	if m.Context != DefaultContext || m.MaxOutput != DefaultMaxOutput {
		t.Fatalf("limits = %d/%d, want the defaults %d/%d",
			m.Context, m.MaxOutput, DefaultContext, DefaultMaxOutput)
	}
	if m.Name != m.ID {
		t.Fatalf("Name = %q, want the identifier when no name was declared", m.Name)
	}
	// Free is NOT defaulted to true. Every served model would then be marked
	// free, the paid ones included, and an eligibility predicate reading the
	// flag would admit all of them — the failure the flag exists to prevent.
	if m.Free {
		t.Fatalf("an undescribed model was reported free: %+v", m)
	}
}

// TestDeclaredPropertiesOutrankDefaults — a value the source declared must
// replace the default, and a value it left out must leave the default standing.
//
// The distinction is the whole point of the properties being optional: the
// absence of a field is "not declared", not "zero", so a source that knows the
// window but not the ceiling must not flatten the ceiling.
func TestDeclaredPropertiesOutrankDefaults(t *testing.T) {
	meta := provider.Model{
		ID:        "known-free",
		Name:      "Known",
		Context:   128000,
		MaxOutput: 0,
		Free:      true,
	}

	m := Describe("known-free", meta)

	if m.Name != "Known" {
		t.Fatalf("Name = %q, want the declared one", m.Name)
	}
	if m.Context != 128000 {
		t.Fatalf("Context = %d, want the declared window", m.Context)
	}
	if m.MaxOutput != DefaultMaxOutput {
		t.Fatalf("MaxOutput = %d, want the default: the source declared no ceiling", m.MaxOutput)
	}
	if !m.Free {
		t.Fatal("the declared free flag was dropped")
	}
}

// TestEligibilityJudgesUndescribedModelsToo — the predicate sees every model and
// is told which ones the property source described.
//
// The two cases are not equivalent: a model the source described and rejected is
// paid, while a model it has not reached is merely unproven. Collapsing them
// would take the decision away from the predicate, which is the only party that
// knows whether a name is enough to judge by.
func TestEligibilityJudgesUndescribedModelsToo(t *testing.T) {
	c := newTestCatalog(t, serveList(t, http.StatusOK,
		`{"data":[{"id":"proven-free"},{"id":"proven-paid"},{"id":"unproven-free"}]}`))

	var described []string
	c.WithEligibility(func(m provider.Model, known bool) bool {
		if known {
			described = append(described, m.ID)
			return m.Free
		}
		return strings.HasSuffix(m.ID, "-free")
	})

	models, err := c.Models(t.Context())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}

	// No property source is configured, so nothing is described and the name is
	// the only evidence: the model without the suffix is dropped.
	if len(models) != 2 {
		t.Fatalf("Models = %+v, want the two models whose names say free", models)
	}
	if models[0].ID != "proven-free" || models[1].ID != "unproven-free" {
		t.Fatalf("Models = %+v, want the free-suffixed models in order", models)
	}
	if len(described) != 0 {
		t.Fatalf("the predicate was told about described models %v with no source configured", described)
	}
}

// TestARefusalCarriesTheStatusAndTheExplanation — the log line names the cause.
//
// A poll that failed with only "something went wrong" would leave the operator
// with an empty selector and nothing to act on. The status says WHICH refusal it
// was and the body says why, and both survive into the message.
func TestARefusalCarriesTheStatusAndTheExplanation(t *testing.T) {
	c := newTestCatalog(t, serveList(t, http.StatusUnauthorized, `{"error":"no key"}`))

	ids, err := c.fetchList(t.Context())
	if err == nil {
		t.Fatalf("fetchList = %v and no error, want the refusal reported", ids)
	}
	var catErr *Error
	if !asError(err, &catErr) {
		t.Fatalf("error is %T, want a catalog error", err)
	}
	if catErr.Status != http.StatusUnauthorized {
		t.Fatalf("Status = %d, want 401", catErr.Status)
	}
	// The body goes into the cause: without it the operator would have only a
	// status code to work from.
	if !strings.Contains(catErr.Reason, "no key") {
		t.Fatalf("Reason = %q, want the service's explanation", catErr.Reason)
	}
}

// TestEmptyListLeavesTheCatalogEmpty — the service answering with no models is
// not taken as a catalog.
//
// The read is silent now: it serves memory and has no error to return. What
// matters is that an unusable answer does not become a list — a catalog of
// nothing is the honest state, and the poller will report the reason into the
// log on its next pass.
func TestEmptyListLeavesTheCatalogEmpty(t *testing.T) {
	c := newTestCatalog(t, serveList(t, http.StatusOK, `{"data":[]}`))

	models, err := c.Models(t.Context())
	if err != nil {
		t.Fatalf("Models: %v, want a read from memory not to fail", err)
	}
	if len(models) != 0 {
		t.Fatalf("Models = %+v, want no models from an empty answer", models)
	}
}

// TestUnparsableAnswerLeavesTheCatalogEmpty — a body that is not JSON must not be
// read as a list of models.
func TestUnparsableAnswerLeavesTheCatalogEmpty(t *testing.T) {
	var logged atomic.Int32
	c := newTestCatalogWithLog(t, serveList(t, http.StatusOK, `<html>gateway</html>`),
		func(string, ...any) { logged.Add(1) })

	models, err := c.Models(t.Context())
	if err != nil {
		t.Fatalf("Models: %v, want a read from memory not to fail", err)
	}
	if len(models) != 0 {
		t.Fatalf("Models = %+v, want no models from an unparsable answer", models)
	}
	if logged.Load() == 0 {
		t.Fatal("the poll failure was not logged: nothing would explain the empty catalog")
	}
}

// TestARefusedListIsReportedToTheLog — a refusal has no caller to return to.
//
// The poller runs between requests, so the error cannot be handed back to
// anyone: the log is the only place it survives, and a silent refusal would
// leave an empty catalog with no explanation.
func TestARefusedListIsReportedToTheLog(t *testing.T) {
	var logged atomic.Int32
	c := newTestCatalogWithLog(t, serveList(t, http.StatusForbidden, `{"error":"denied"}`),
		func(string, ...any) { logged.Add(1) })

	models, err := c.Models(t.Context())
	if err != nil {
		t.Fatalf("Models: %v, want a read from memory not to fail", err)
	}
	if len(models) != 0 {
		t.Fatalf("Models = %+v, want no models after a refusal", models)
	}
	if logged.Load() == 0 {
		t.Fatal("the refusal was not logged: the empty catalog would be unexplained")
	}
}

// TestReadsDoNotReachTheService — the request path never pays for the catalog.
//
// This is the whole point of the background poller: a hundred client requests
// and a quiet gateway ask the service the same number of times, and none of
// those requests can be delayed by a slow or unreachable service.
func TestReadsDoNotReachTheService(t *testing.T) {
	var hits atomic.Int32
	c := newTestCatalog(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"alpha-free"}]}`))
	})

	before := hits.Load()
	for i := 0; i < 3; i++ {
		if _, err := c.Models(t.Context()); err != nil {
			t.Fatalf("Models call %d: %v", i+1, err)
		}
	}
	if got := hits.Load(); got != before {
		t.Fatalf("the list was requested %d more times on the read path, want 0", got-before)
	}

	// The warm-up read is the one request that IS expected: without it a freshly
	// started catalog would have nothing to report.
	if before != 1 {
		t.Fatalf("warm-up made %d requests, want exactly 1", before)
	}
}

// metaSourceStub supplies properties without a network, so that the merge can be
// checked on its own.
type metaSourceStub struct {
	models []provider.Model
	err    error
	hits   atomic.Int32
}

func (s *metaSourceStub) Name() string { return "stub" }

func (s *metaSourceStub) Meta(context.Context) (map[string]provider.Model, error) {
	s.hits.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	out := make(map[string]provider.Model, len(s.models))
	for _, m := range s.models {
		out[m.ID] = m
	}
	return out, nil
}

// TestPropertiesAugmentTheListWithoutDecidingIt — the list determines which
// models exist; the source only refines them, and its failure is not the
// catalog's failure.
//
// This is the separation the package is built on: a property source that lags
// behind must not remove live models, and one that is down must leave the
// catalog working with defaults rather than break it.
func TestPropertiesAugmentTheListWithoutDecidingIt(t *testing.T) {
	const list = `{"data":[{"id":"described-free"},{"id":"undescribed-free"}]}`
	srv := httptest.NewServer(serveList(t, http.StatusOK, list))
	t.Cleanup(srv.Close)

	described := &metaSourceStub{models: []provider.Model{{
		ID:        "described-free",
		Name:      "Described",
		Context:   64000,
		MaxOutput: 8000,
		Free:      true,
	}}}
	c := New(srv.URL, listPath, srv.Client(), nil, described)
	c.Start(t.Context())
	t.Cleanup(func() { _ = c.Stop() })

	models, err := c.Models(t.Context())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("Models = %+v, want both models: the source does not decide composition", models)
	}
	if models[0].Name != "Described" || models[0].Context != 64000 {
		t.Fatalf("the declared properties did not reach the model: %+v", models[0])
	}
	if models[1].Context != DefaultContext {
		t.Fatalf("the undescribed model got %d for a window, want the default", models[1].Context)
	}

	// A source that failed leaves the catalog standing, described with defaults.
	failing := &metaSourceStub{err: &Error{Op: "property catalog", Status: 500, Reason: "down"}}
	broken := New(srv.URL, listPath, srv.Client(), nil, failing)
	broken.Start(t.Context())
	t.Cleanup(func() { _ = broken.Stop() })
	models, err = broken.Models(t.Context())
	if err != nil {
		t.Fatalf("Models with a failed property source: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("Models = %+v, want the live list kept when the source is down", models)
	}
	if models[0].Context != DefaultContext {
		t.Fatalf("Context = %d, want the default: the source answered nothing", models[0].Context)
	}
	if hits := failing.hits.Load(); hits != 1 {
		t.Fatalf("the property source was polled %d times, want once per refresh", hits)
	}
}

// asError is errors.As without the import noise in every check.
func asError(err error, target **Error) bool {
	e, ok := err.(*Error)
	if !ok {
		return false
	}
	*target = e
	return true
}

// TestDescribeKeepsTheReasoningLadderIntact — the effort options are the model's
// declaration of how its reasoning can be steered, and they must survive the
// merge as the source wrote them.
func TestDescribeKeepsTheReasoningLadderIntact(t *testing.T) {
	options := []provider.ReasoningOption{
		{Kind: provider.ReasoningEffort, Values: []string{"low", "high"}},
		{Kind: provider.ReasoningBudget, Max: 4096},
	}
	m := Describe("thinks-free", provider.Model{Reasoning: true, ReasoningOptions: options})

	if !m.Reasoning {
		t.Fatal("the declared ability to reason was dropped")
	}
	if len(m.ReasoningOptions) != 2 {
		t.Fatalf("ReasoningOptions = %+v, want both declared forms", m.ReasoningOptions)
	}
	if m.ReasoningOptions[0].Kind != provider.ReasoningEffort || len(m.ReasoningOptions[0].Values) != 2 {
		t.Fatalf("the effort ladder was altered: %+v", m.ReasoningOptions[0])
	}
	// The encoding of the options is what a client reads, so it is checked as
	// the client would see it rather than field by field.
	encoded, err := json.Marshal(m.ReasoningOptions)
	if err != nil {
		t.Fatalf("encoding the options: %v", err)
	}
	if !strings.Contains(string(encoded), `"budget_tokens"`) {
		t.Fatalf("the budget form lost its kind: %s", encoded)
	}
}

// TestTheWaitAfterAFailureShrinksTowardsTheTtl — the poller's interval is a
// ladder, not a constant. What it must never be is the TTL straight after a
// failure: a daemon that came up while the uplink was down would then serve an
// empty catalog for five minutes, and every request inside that window would be
// answered "the model does not exist".
func TestTheWaitAfterAFailureShrinksTowardsTheTtl(t *testing.T) {
	// The first wait is the one the warm-up's result sizes. A warm catalog is
	// polled at the TTL, a cold one is asked again as soon as firstRetryDelay.
	if got := firstWait(true); got != listTTL {
		t.Fatalf("the wait after a successful warm-up = %v, want %v", got, listTTL)
	}
	if got := firstWait(false); got != firstRetryDelay {
		t.Fatalf("the wait after a failed warm-up = %v, want %v", got, firstRetryDelay)
	}

	// A failure doubles the wait, and a success returns it to the TTL.
	if got := nextWait(false, firstRetryDelay); got != 2*firstRetryDelay {
		t.Fatalf("the wait after a second failure = %v, want %v", got, 2*firstRetryDelay)
	}
	if got := nextWait(true, firstRetryDelay); got != listTTL {
		t.Fatalf("the wait after a good read = %v, want %v", got, listTTL)
	}

	// The ladder is capped by the TTL, and a failure AT the TTL comes back to the
	// short retry: a service that recovers after a long outage must be noticed in
	// seconds, not at the end of another interval.
	if got := nextWait(false, 4*listTTL/5); got != listTTL {
		t.Fatalf("the ladder sailed past the TTL: %v, want %v", got, listTTL)
	}
	if got := nextWait(false, listTTL); got != firstRetryDelay {
		t.Fatalf("the wait after a failure at the TTL = %v, want %v", got, firstRetryDelay)
	}
}
