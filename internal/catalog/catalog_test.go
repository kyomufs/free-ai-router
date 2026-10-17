package catalog

import (
	"net/http"
	"net/http/httptest"
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

// TestOpenAICompatibleProviderBuilds — the extension point: a new service is
// hooked up by a section, without code.
func TestOpenAICompatibleProviderBuilds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"free-model"}]}`))
	}))
	defer srv.Close()

	c := cfgWith(t, "zenmux", config.ProviderConfig{
		Type:    "openai",
		BaseURL: srv.URL,
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
	if len(models) != 1 || models[0].ID != "free-model" {
		t.Fatalf("Models = %+v", models)
	}
	// The `Free` flag is NOT asserted here. It is not this package's decision:
	// a provider decides what belongs in its catalog, and the openai-compatible
	// provider applies no eligibility filter at all, so its models carry
	// whatever the source said — which for a live list with no properties is
	// nothing. Asserting it here would pin one provider's filtering policy
	// inside the shared assembly code, and the filter itself is covered where
	// it lives, in the provider's own checks.
}

// TestProviderIDMatchesSectionName — the route name comes from the section. If
// the constructor returns another name, the client will not find the provider.
func TestProviderIDMatchesSectionName(t *testing.T) {
	c := config.Default()
	c.Providers = map[string]config.ProviderConfig{
		"first":  {Type: "openai", BaseURL: "https://a.example/v1"},
		"second": {Type: "openai", BaseURL: "https://b.example/v1"},
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

// TestOpenAIRequiresBaseURL — a service on the shared protocol has no default
// address: each one has its own.
func TestOpenAIRequiresBaseURL(t *testing.T) {
	c := config.Default()
	c.Upstream.Val = ""
	c.Providers = map[string]config.ProviderConfig{"mine": {Type: "openai"}}

	if _, err := Build(c); err == nil {
		t.Fatal("an openai-compatible provider without a base_url must fail")
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
func TestSettingsForwardedToProvider(t *testing.T) {
	c := cfgWith(t, "mine", config.ProviderConfig{
		Type:    "openai",
		BaseURL: "https://example.test/v1",
		Settings: map[string]any{
			"chat_path":  "/custom/chat",
			"user_agent": "my-agent/1.0",
		},
	})

	reg, err := Build(c)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	p, _ := reg.Get("mine")
	prep, err := p.Build(provider.Request{Model: provider.Model{ID: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(prep.URL, "/custom/chat") {
		t.Fatalf("URL = %q, want the configured chat path", prep.URL)
	}
	if got := prep.Headers.Get("user-agent"); got != "my-agent/1.0" {
		t.Fatalf("user-agent = %q", got)
	}
}

// TestBuildDoesNotMutateConfiguration — assembly must not change the
// configuration: other consumers read it too.
func TestBuildDoesNotMutateConfiguration(t *testing.T) {
	settings := map[string]any{"chat_path": "/original"}
	c := cfgWith(t, "mine", config.ProviderConfig{
		Type:     "openai",
		BaseURL:  "https://example.test/v1",
		Settings: settings,
	})

	if _, err := Build(c); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if settings["chat_path"] != "/original" {
		t.Fatal("Build mutated the caller's settings map")
	}
}

// TestModelsComeFromTheService — the catalog is taken from the service, not
// from the configuration. That is the main property: a list in the code would
// drift away from the service, and the client would pick something that no
// longer exists.
func TestModelsComeFromTheService(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"live-model"},{"id":"second"}]}`))
	}))
	defer srv.Close()

	c := cfgWith(t, "own", config.ProviderConfig{
		Type:    "openai",
		BaseURL: srv.URL,
	})

	reg, err := Build(c)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	p, _ := reg.Get("own")
	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}

	if len(models) != 2 || models[0].ID != "live-model" {
		t.Fatalf("Models = %+v, want the two the service declared", models)
	}
	if len(asked) != 1 || asked[0] != "/models" {
		t.Fatalf("service was asked %v, want GET /models", asked)
	}
	// A model without properties is described by defaults rather than rejected:
	// the service declared it before the property catalog learned about it.
	if models[0].Context <= 0 {
		t.Fatalf("Context = %d, want a usable default", models[0].Context)
	}
}

// TestCatalogFailureIsReported — an unavailable catalog is returned as an
// error, not as an empty list. An empty list would be read by the operator as
// "the service has no models" and send them looking in the wrong place.
func TestCatalogFailureIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"nope"}`, http.StatusForbidden)
	}))
	defer srv.Close()

	c := cfgWith(t, "own", config.ProviderConfig{
		Type:    "openai",
		BaseURL: srv.URL,
	})

	reg, err := Build(c)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	p, _ := reg.Get("own")
	models, err := p.Models(t.Context())
	if err == nil {
		t.Fatalf("Models = %+v, want an error for an unavailable catalog", models)
	}
	// The service's refusal must be visible in the cause: without it there is
	// nothing to investigate.
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("error %q does not name the status", err)
	}
}

// TestRegistryOrderIsStable — the order in the registry must not depend on map
// traversal: lists in the CLI and the TUI would otherwise jump between runs.
func TestRegistryOrderIsStable(t *testing.T) {
	c := config.Default()
	c.Providers = map[string]config.ProviderConfig{
		"c": {Type: "openai", BaseURL: "https://v.test/v1"},
		"a": {Type: "openai", BaseURL: "https://a.test/v1"},
		"b": {Type: "openai", BaseURL: "https://b.test/v1"},
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
