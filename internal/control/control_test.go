package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"free-ai-router/internal/config"
	"free-ai-router/internal/provider"
	"free-ai-router/internal/state"
)

// stubProvider — a stand-in provider for control API checks.
type stubProvider struct {
	id     provider.ID
	models []provider.Model
}

func (s *stubProvider) ID() provider.ID { return s.id }

func (s *stubProvider) Models(context.Context) ([]provider.Model, error) {
	return s.models, nil
}

func (s *stubProvider) Build(provider.Request) (provider.Prepared, error) {
	return provider.Prepared{}, nil
}

func (s *stubProvider) Classify(*http.Response, []byte) provider.Failure {
	return provider.Failure{}
}

// stubKeys — a key-rotation mechanism for checks.
type stubKeys struct {
	keys map[provider.ID][]string
}

func (s *stubKeys) Keys(id provider.ID) []string { return s.keys[id] }

func (s *stubKeys) KeyFingerprints(id provider.ID) []string {
	out := make([]string, 0, len(s.keys[id]))
	for _, k := range s.keys[id] {
		out = append(out, "fp-"+k)
	}
	return out
}

func (s *stubKeys) HasRotation(id provider.ID) bool { return len(s.keys[id]) > 1 }

func (s *stubKeys) AddKey(id provider.ID, key string) bool {
	for _, existing := range s.keys[id] {
		if existing == key {
			return false
		}
	}
	s.keys[id] = append(s.keys[id], key)
	return true
}

func (s *stubKeys) RemoveFingerprint(id provider.ID, fingerprint string) bool {
	want := strings.TrimPrefix(fingerprint, "fp-")
	for i, k := range s.keys[id] {
		if k == want {
			s.keys[id] = append(s.keys[id][:i], s.keys[id][i+1:]...)
			return true
		}
	}
	return false
}

// newTestControl assembles the handler for checks.
func newTestControl(t *testing.T) (*Handler, *http.ServeMux) {
	t.Helper()

	reg := provider.NewRegistry()
	reg.Register(&stubProvider{
		id: "first",
		models: []provider.Model{
			{ID: "m1", Name: "Model 1", Free: true, Context: 1000},
			{ID: "m2", Name: "Model 2", Free: true, Context: 2000, Reasoning: true},
		},
	})
	reg.Register(&stubProvider{id: "second", models: []provider.Model{{ID: "m3", Free: false}}})

	st, err := state.Open(t.TempDir() + "/state.json")
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Mode.Set("local", config.SourceDefault)
	cfg.Listen.Set("127.0.0.1:8787", config.SourceDefault)

	h := &Handler{
		Providers: reg,
		KeyRotation: &stubKeys{keys: map[provider.ID][]string{
			"first": {"key1", "key2"},
		}},
		State:      st,
		Config:     cfg,
		Transports: []string{"direct"},
	}

	mux := http.NewServeMux()
	h.Routes(mux)
	return h, mux
}

// get performs a GET request against the control API.
func get(t *testing.T, mux *http.ServeMux, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestTokenIsEnforced — in server mode the check wraps the control API TOO.
//
// Control can burn through a provider's keys, so an open control would nullify
// the gateway's check.
func TestTokenIsEnforced(t *testing.T) {
	_, mux := newTestControl(t)
	h := &Handler{RequireToken: true, Token: "secret", Providers: provider.NewRegistry()}
	mux2 := http.NewServeMux()
	h.Routes(mux2)

	// Without a token — refusal.
	rec := get(t, mux2, Prefix+"status", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status without a token = %d, want 401", rec.Code)
	}

	// With a wrong token — refusal as well.
	rec = get(t, mux2, Prefix+"status", map[string]string{"authorization": "Bearer wrong"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status with a wrong token = %d, want 401", rec.Code)
	}

	// With the right one — access is granted.
	rec = get(t, mux2, Prefix+"status", map[string]string{"authorization": "Bearer secret"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status with the right token = %d, want 200", rec.Code)
	}

	_ = mux
}

// TestLocalModeNeedsNoToken — in local the check is not in force.
func TestLocalModeNeedsNoToken(t *testing.T) {
	_, mux := newTestControl(t)
	if rec := get(t, mux, Prefix+"status", nil); rec.Code != http.StatusOK {
		t.Fatalf("status in local mode = %d, want 200", rec.Code)
	}
}

// TestStatusReportsProvidersAndCounters — the snapshot contains the providers
// and today's counters.
func TestStatusReportsProvidersAndCounters(t *testing.T) {
	h, mux := newTestControl(t)
	h.State.RecordSuccess(state.Outcome{Model: "m1", Address: "213.14.43.234"})
	h.State.RecordRefusal(state.Outcome{Model: "m2", Address: "213.14.43.234"})

	rec := get(t, mux, Prefix+"status", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var st Status
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("the status body is not JSON: %v", err)
	}
	if st.Mode != "local" {
		t.Fatalf("Mode = %q, want local", st.Mode)
	}
	if len(st.Providers) != 2 {
		t.Fatalf("Providers = %d, want 2", len(st.Providers))
	}

	byID := map[string]ProviderStatus{}
	for _, p := range st.Providers {
		byID[p.ID] = p
	}
	if got := byID["first"]; got.Models != 2 || got.FreeModels != 2 {
		t.Fatalf("first = %+v, want 2 models both free", got)
	}
	// The keys are on file and NOT used: the fixture runs the default
	// configuration, whose credential is anonymous, and that mode presents no
	// key whatever the pool holds. Rotation therefore cannot act either — there
	// is nothing to replace — and a monitor reporting otherwise would describe
	// the mechanism's switch rather than the wire.
	if got := byID["first"]; got.Keys != 2 {
		t.Fatalf("first keys = %+v, want the two keys on file", got)
	}
	if got := byID["first"]; got.KeyRotation {
		t.Fatalf("first = %+v, want no rotation under the anonymous credential", got)
	}
	if got := byID["first"]; got.KeyUsed {
		t.Fatalf("first = %+v, want the keys reported as unused", got)
	}
	if got := byID["first"]; got.Credential != config.CredentialAnonymous {
		t.Fatalf("first credential = %q, want %q", got.Credential, config.CredentialAnonymous)
	}
	if got := byID["second"]; got.Keys != 0 || got.KeyRotation || got.KeyUsed {
		t.Fatalf("second = %+v, want no keys at all", got)
	}

	if st.Totals.OK != 1 || st.Totals.Refused != 1 {
		t.Fatalf("totals = ok %d refused %d, want 1 and 1", st.Totals.OK, st.Totals.Refused)
	}
	if st.Totals.Aborted != 0 {
		t.Fatalf("totals aborted = %d, want 0", st.Totals.Aborted)
	}

	// The per-address breakdown travels with the status: it is what the monitor
	// draws the model matrix from, and a monitor is useless if its data source
	// drops the dimension.
	if len(st.Totals.ByAddress) != 1 {
		t.Fatalf("totals by_address = %+v, want one row", st.Totals.ByAddress)
	}
	row := st.Totals.ByAddress[0]
	if row.Address != "213.14.43.234" || row.Family != "v4" {
		t.Fatalf("the address row lost its identity: %+v", row)
	}
	if row.OK != 1 || row.Refused != 1 {
		t.Fatalf("the address row lost its counters: %+v", row)
	}
}

// TestStatsReportsDays — the counters are handed out by day.
func TestStatsReportsDays(t *testing.T) {
	h, mux := newTestControl(t)
	h.State.RecordSuccess(state.Outcome{Model: "m1"})

	rec := get(t, mux, Prefix+"stats", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("stats = %d", rec.Code)
	}
	var snap state.Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("the stats body is not JSON: %v", err)
	}
	if len(snap.Days) != 1 || snap.Days[0].OK != 1 {
		t.Fatalf("days = %+v", snap.Days)
	}
}

// TestModelsListsEveryProvider — the catalog contains the models of every
// provider.
func TestModelsListsEveryProvider(t *testing.T) {
	_, mux := newTestControl(t)

	rec := get(t, mux, Prefix+"models", nil)
	var body struct {
		Models []struct {
			Provider string `json:"provider"`
			ID       string `json:"id"`
			Free     bool   `json:"free"`
		} `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if len(body.Models) != 3 {
		t.Fatalf("models = %d, want 3", len(body.Models))
	}
	providers := map[string]int{}
	for _, m := range body.Models {
		providers[m.Provider]++
	}
	if providers["first"] != 2 || providers["second"] != 1 {
		t.Fatalf("provider split = %v", providers)
	}
}

// TestKeysAreFingerprinted — the help text hands out fingerprints, not secrets.
//
// The operator needs to understand how many keys are in use, not to see a key.
func TestKeysAreFingerprinted(t *testing.T) {
	_, mux := newTestControl(t)

	rec := get(t, mux, Prefix+"keys", nil)
	body := rec.Body.String()
	if strings.Contains(body, `"key1"`) {
		t.Fatalf("the raw key leaked into the response: %s", body)
	}
	if !strings.Contains(body, "fp-key1") {
		t.Fatalf("the fingerprint is missing: %s", body)
	}
}

// TestKeyAddAndRemove — a key is added and removed, while adding the same one
// again creates no duplicate.
func TestKeyAddAndRemove(t *testing.T) {
	_, mux := newTestControl(t)

	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// Adding a new key.
	rec := post(Prefix+"keys/add", `{"provider":"first","key":"key3"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("add = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"added": true`) {
		t.Fatalf("the first add must report a change: %s", rec.Body.String())
	}

	// Adding the same key again makes no change.
	rec = post(Prefix+"keys/add", `{"provider":"first","key":"key3"}`)
	if !strings.Contains(rec.Body.String(), `"added": false`) {
		t.Fatalf("a duplicate add must report no change: %s", rec.Body.String())
	}

	// Removal by fingerprint.
	rec = post(Prefix+"keys/remove", `{"provider":"first","key":"fp-key3"}`)
	if !strings.Contains(rec.Body.String(), `"removed": true`) {
		t.Fatalf("remove must report a change: %s", rec.Body.String())
	}
	rec = post(Prefix+"keys/remove", `{"provider":"first","key":"fp-no-such-key"}`)
	if !strings.Contains(rec.Body.String(), `"removed": false`) {
		t.Fatalf("removing an absent key must report no change: %s", rec.Body.String())
	}
}
