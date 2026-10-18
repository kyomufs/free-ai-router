package catalog

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"free-ai-router/internal/config"
	"free-ai-router/internal/provider"
)

// cfgWith builds a configuration with a single provider section.
func cfgWith(t *testing.T, id string, pc config.ProviderConfig) *config.Config {
	t.Helper()
	c := config.Default()
	c.Providers = map[string]config.ProviderConfig{id: pc}
	return c
}

// TestBuildsEnabledProviders — enabled sections reach the registry.
func TestBuildsEnabledProviders(t *testing.T) {
	c := cfgWith(t, "opencode", config.ProviderConfig{Type: "opencode"})

	reg, err := Build(c)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if reg.Len() != 1 {
		t.Fatalf("Len = %d, want 1", reg.Len())
	}
	p, err := reg.Get("opencode")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p.ID() != "opencode" {
		t.Fatalf("ID = %q, want opencode", p.ID())
	}
}

// TestDisabledProvidersSkipped — a disabled section does not stop the rest from
// working.
func TestDisabledProvidersSkipped(t *testing.T) {
	off := false
	c := config.Default()
	c.Providers = map[string]config.ProviderConfig{
		"opencode": {Type: "opencode"},
		"disabled": {Type: "opencode", Enabled: &off},
	}

	reg, err := Build(c)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if reg.Len() != 1 {
		t.Fatalf("Len = %d, want 1 (the disabled entry must be skipped)", reg.Len())
	}
	if _, err := reg.Get("disabled"); err == nil {
		t.Fatal("the disabled provider reached the registry")
	}
}

// TestEmptyConfigurationIsAnError — with no providers the gateway has nothing
// to do, and that must be said plainly rather than discovered on the first
// request.
func TestEmptyConfigurationIsAnError(t *testing.T) {
	c := config.Default()
	if _, err := Build(c); err == nil {
		t.Fatal("an empty configuration must be an error")
	}

	off := false
	c.Providers = map[string]config.ProviderConfig{"disabled": {Type: "opencode", Enabled: &off}}
	if _, err := Build(c); err == nil {
		t.Fatal("a configuration with only disabled providers must be an error")
	}
}

// TestUnknownTypeNamesTheKnownOnes — a typo in the type must immediately show
// which types exist.
func TestUnknownTypeNamesTheKnownOnes(t *testing.T) {
	c := cfgWith(t, "mine", config.ProviderConfig{Type: "zenmux"})

	_, err := Build(c)
	if err == nil {
		t.Fatal("an unknown type must fail")
	}
	msg := err.Error()
	for _, known := range KnownTypes() {
		if !strings.Contains(msg, known) {
			t.Fatalf("the error %q does not mention the known type %q", msg, known)
		}
	}
}

// TestMissingTypeIsAnError — a section without a type does not build: it is
// unknown which code should serve it.
func TestMissingTypeIsAnError(t *testing.T) {
	c := cfgWith(t, "mine", config.ProviderConfig{})
	if _, err := Build(c); err == nil {
		t.Fatal("a section without a type must fail")
	}
}

// TestStubProviderBuilds — the second type, and the only one that needs no
// service.
//
// It exists so that the whole path can be exercised without a key and without a
// network: a section naming it builds, and its catalog is the one the section
// declares, because there is no service to ask.
func TestStubProviderBuilds(t *testing.T) {
	c := cfgWith(t, "zenmux", config.ProviderConfig{
		Type: "stub",
	})

	reg, err := Build(c)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	p, err := reg.Get("zenmux")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p.ID() != "zenmux" {
		t.Fatalf("ID = %q, want the section name", p.ID())
	}

	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "stub-echo" {
		t.Fatalf("Models = %+v, want the stub's own default", models)
	}
}

// TestStubCatalogIsDeclaredInSettings — the deliberate exception to "the catalog
// comes from the service".
//
// The exception is safe precisely because there is no service: a declared list
// can only go stale against something that exists, and this type has nothing to
// go stale against.
func TestStubCatalogIsDeclaredInSettings(t *testing.T) {
	c := cfgWith(t, "mine", config.ProviderConfig{
		Type:     "stub",
		Settings: map[string]any{"models": []any{"alpha", "beta"}},
	})

	reg, err := Build(c)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	p, _ := reg.Get("mine")
	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "alpha" || models[1].ID != "beta" {
		t.Fatalf("Models = %+v, want the two the section declared", models)
	}
}

// TestProviderIDMatchesSectionName — the route name comes from the section. If
// the constructor returns another name, the client will not find the provider.
func TestProviderIDMatchesSectionName(t *testing.T) {
	c := config.Default()
	c.Providers = map[string]config.ProviderConfig{
		"first":  {Type: "stub", BaseURL: "https://a.example/v1"},
		"second": {Type: "stub", BaseURL: "https://b.example/v1"},
	}

	reg, err := Build(c)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, id := range []string{"first", "second"} {
		p, err := reg.Get(provider.ID(id))
		if err != nil {
			t.Fatalf("Get(%q): %v", id, err)
		}
		if string(p.ID()) != id {
			t.Fatalf("provider %q reports ID %q", id, p.ID())
		}
	}
}

// TestEveryRegisteredTypeBuilds — every name in the type list is one the
// assembly can actually serve.
//
// The list is what the error message and the axes dictionary show the operator,
// so a name in it that no builder handles is worse than a missing type: it is
// advice that leads to a failure.
func TestEveryRegisteredTypeBuilds(t *testing.T) {
	for _, name := range KnownTypes() {
		t.Run(name, func(t *testing.T) {
			c := cfgWith(t, "mine", config.ProviderConfig{Type: name})
			reg, err := Build(c)
			if err != nil {
				t.Fatalf("the listed type %q does not build: %v", name, err)
			}
			if _, err := reg.Get("mine"); err != nil {
				t.Fatalf("the listed type %q built nothing: %v", name, err)
			}
		})
	}
}

// TestOpenCodeDefaultsToItsKnownURL — opencode's address is known, and it need
// not be repeated in the configuration.
func TestOpenCodeDefaultsToItsKnownURL(t *testing.T) {
	c := cfgWith(t, "opencode", config.ProviderConfig{Type: "opencode"})

	reg, err := Build(c)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	p, err := reg.Get("opencode")
	if err != nil {
		t.Fatal(err)
	}
	prep, err := p.Build(provider.Request{Model: provider.Model{ID: "x"}})
	if err != nil {
		t.Fatalf("Build request: %v", err)
	}
	if !strings.HasPrefix(prep.URL, "https://opencode.ai/") {
		t.Fatalf("URL = %q, want the known opencode address", prep.URL)
	}
}

// TestSettingsForwardedToProvider — the section's settings reach the provider.
//
// The settings map is the extension point for everything a type understands
// beyond the fields every type shares, and it is passed through untranslated. The
// check is made on the provider's BEHAVIOUR rather than on a field, because a
// field would only show that the value was stored, not that anything read it.
func TestSettingsForwardedToProvider(t *testing.T) {
	c := cfgWith(t, "mine", config.ProviderConfig{
		Type: "stub",
		Settings: map[string]any{
			"reply":  "from the settings",
			"models": []any{"only"},
		},
	})

	reg, err := Build(c)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	p, _ := reg.Get("mine")

	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "only" {
		t.Fatalf("Models = %+v, want the one the section named", models)
	}

	answer, err := p.(provider.Local).RoundTripper().RoundTrip(
		&http.Request{Method: http.MethodPost, Header: make(http.Header)})
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer func() { _ = answer.Body.Close() }()
	body, err := io.ReadAll(answer.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "from the settings") {
		t.Fatalf("the reply setting did not reach the provider: %s", body)
	}
}

// TestBuildDoesNotMutateConfiguration — assembly must not change the
// configuration: other consumers read it too.
func TestBuildDoesNotMutateConfiguration(t *testing.T) {
	models := []any{"only"}
	settings := map[string]any{"models": models}
	c := cfgWith(t, "mine", config.ProviderConfig{
		Type:     "stub",
		Settings: settings,
	})

	if _, err := Build(c); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(settings) != 1 {
		t.Fatalf("Build added keys to the caller's settings map: %v", settings)
	}
	if got, ok := settings["models"].([]any); !ok || len(got) != 1 || got[0] != "only" {
		t.Fatalf("Build rewrote the caller's settings: %v", settings["models"])
	}
	if !reflect.DeepEqual(c.Providers["mine"].Settings, settings) {
		t.Fatalf("Build replaced the settings map on the configuration: %v",
			c.Providers["mine"].Settings)
	}
}

// TestSectionAddressesReachTheProvider — the section says where to ask, and the
// provider asks there.
//
// Both addresses are asserted at once, because both are the same decision: the
// list of models and the properties of those models come from the service, and
// neither belongs in the configuration. The check is made through the catalog
// request rather than by reading a field, since a field only shows that the value
// was stored.
func TestSectionAddressesReachTheProvider(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		w.Header().Set("content-type", "application/json")
		if r.URL.Path == "/props.json" {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"live-model-free"},{"id":"second-free"}]}`))
	}))
	defer srv.Close()

	c := cfgWith(t, "own", config.ProviderConfig{
		Type:    "opencode",
		BaseURL: srv.URL,
		Settings: map[string]any{
			"meta_url": srv.URL + "/props.json",
		},
	})

	reg, err := Build(c)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	p, _ := reg.Get("own")
	// The catalog is read from memory: a built provider has to be warmed before
	// it can report anything, exactly as the daemon does before listening.
	polling, ok := p.(provider.Polling)
	if !ok {
		t.Fatal("the provider does not poll its catalog")
	}
	polling.Start(t.Context())
	t.Cleanup(func() { _ = polling.Stop() })
	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}

	if len(models) != 2 || models[0].ID != "live-model-free" {
		t.Fatalf("Models = %+v, want the two the service declared", models)
	}
	if len(asked) != 2 || asked[0] != "/models" || asked[1] != "/props.json" {
		t.Fatalf("the service was asked %v, want the list and then the properties", asked)
	}
	// A model without properties is described by defaults rather than rejected:
	// the service declared it before the property catalog learned about it.
	if models[0].Context <= 0 {
		t.Fatalf("Context = %d, want a usable default", models[0].Context)
	}
}

// TestCatalogFailureIsReported — an unavailable catalog is reported into the
// log, not returned as an error. The read serves memory, so an empty catalog is
// what the caller sees; the reason has to survive somewhere, and the log is the
// only place left.
func TestCatalogFailureIsReported(t *testing.T) {
	var captured strings.Builder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"nope"}`, http.StatusForbidden)
	}))
	defer srv.Close()

	c := cfgWith(t, "own", config.ProviderConfig{
		Type:    "opencode",
		BaseURL: srv.URL,
		Settings: map[string]any{
			"meta_url": srv.URL + "/props.json",
		},
	})

	reg, err := BuildWithLog(c, func(format string, args ...any) {
		fmt.Fprintf(&captured, format, args...)
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	p, _ := reg.Get("own")
	polling, ok := p.(provider.Polling)
	if !ok {
		t.Fatal("the provider does not poll its catalog")
	}
	polling.Start(t.Context())
	t.Cleanup(func() { _ = polling.Stop() })

	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatalf("Models: %v, want a read from memory not to fail", err)
	}
	if len(models) != 0 {
		t.Fatalf("Models = %+v, want nothing from an unavailable catalog", models)
	}
	// The refusal has no caller to return to, so the log is the only place it
	// survives — and without it there is nothing to investigate.
	if !strings.Contains(captured.String(), "403") {
		t.Fatalf("the log %q does not name the status", captured.String())
	}
}

// TestRegistryOrderIsStable — the order in the registry must not depend on map
// traversal: lists in the CLI and the TUI would otherwise jump between runs.
func TestRegistryOrderIsStable(t *testing.T) {
	c := config.Default()
	c.Providers = map[string]config.ProviderConfig{
		"c": {Type: "stub", BaseURL: "https://v.test/v1"},
		"a": {Type: "stub", BaseURL: "https://a.test/v1"},
		"b": {Type: "stub", BaseURL: "https://b.test/v1"},
	}

	reg, err := Build(c)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	ids := reg.IDs()
	want := []provider.ID{"a", "b", "c"}
	if len(ids) != len(want) {
		t.Fatalf("IDs = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("IDs = %v, want %v", ids, want)
		}
	}
}
