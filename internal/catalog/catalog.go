// Package catalog assembles the providers from the configuration into a working
// registry.
//
// WHY A SEPARATE PACKAGE. provider describes WHAT a provider is and knows
// nothing about the configuration. config describes the settings but knows
// nothing about providers. This package ties them together — and it alone.
// Thanks to that neither of the two depends on the other, and adding a new
// provider type requires editing neither: registering a constructor here is
// enough.
//
// TYPE REGISTRATION. A provider type is the name of the code that serves it
// ("opencode", "stub"). The mapping "type → constructor" is given by the table
// below. A new type is registered with a single line.
package catalog

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"free-ai-router/internal/config"
	"free-ai-router/internal/provider"
	"free-ai-router/internal/provider/opencode"
	"free-ai-router/internal/provider/stub"
)

// Builder creates a provider from the configuration settings.
//
// The input is the provider identifier (the name of the section in the
// configuration) together with its settings; the output is a ready provider. An
// error means the section is filled in incorrectly, and that must be visible at
// once rather than on the first request.
type Builder func(id string, cfg config.ProviderConfig, defaults Defaults) (provider.Provider, error)

// Defaults — the values common to all providers.
//
// They are passed separately so that a provider need not re-read the whole
// configuration for the sake of one line.
type Defaults struct {
	// Upstream — the default address for providers that did not name their own.
	Upstream string
	// HTTP — the client for catalog requests.
	//
	// One for all providers: catalogs are requested rarely, and giving each
	// provider its own connection pool would mean holding several sets of idle
	// sockets for one and the same host.
	HTTP *http.Client
	// Log receives the warnings of the background catalog refresh.
	//
	// A poll failure has no caller to return to: it happens between requests,
	// and the only place it can be reported is the log. Nil means silent.
	Log func(format string, args ...any)
	// FirstEventTimeout, IdleTimeout and ReasoningIdleTimeout are the silence
	// budgets the provider is to enforce on its own requests.
	//
	// They are passed here rather than read inside a provider because the
	// operator's axes are the operator's: a slow uplink is allowed a longer
	// silence, and a provider that ignored the setting would keep breaking a
	// stream the operator had knowingly widened. Zero means the provider's own
	// contract, which is what its defaults are.
	FirstEventTimeout    time.Duration
	IdleTimeout          time.Duration
	ReasoningIdleTimeout time.Duration
}

// builders — the mapping "provider type → constructor".
//
// A table, not a switch: adding a type does not touch the assembly logic, and
// that logic cannot be broken by accident while appending a branch.
var builders = map[string]Builder{
	// A full-fledged provider with a contract of its own.
	"opencode": buildOpenCode,

	// A provider that answers from inside the gateway, with no network at all.
	// It exists so that the whole path — request, refusal, rotation, retry,
	// counters, monitor — can be exercised without a key and without depending
	// on someone else's uptime.
	"stub": buildStub,
}

// Build assembles the provider registry from the configuration.
//
// Providers with enabled: false are skipped — that is the way to turn a section
// off without deleting it. A provider without models is not an error: some
// services have their catalog discovered later, and the gateway itself works
// without one.
func Build(cfg *config.Config) (*provider.Registry, error) {
	return BuildWithLog(cfg, nil)
}

// BuildWithLog assembles the registry and points the background catalog
// refreshers at a log.
//
// A separate entry point rather than a third argument on Build: a one-shot
// command has no log to give, and a nil parameter at every call site would
// obscure the calls that do have one.
func BuildWithLog(cfg *config.Config, log func(format string, args ...any)) (*provider.Registry, error) {
	defaults := Defaults{
		Upstream:             cfg.Upstream.Val,
		Log:                  log,
		FirstEventTimeout:    cfg.FirstEventTimeout.Val,
		IdleTimeout:          cfg.IdleTimeout.Val,
		ReasoningIdleTimeout: cfg.ReasoningIdleTimeout.Val,
	}

	ids := make([]string, 0, len(cfg.Providers))
	for id := range cfg.Providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	reg := provider.NewRegistry()
	for _, id := range ids {
		pc := cfg.Providers[id]
		if !pc.IsEnabled() {
			continue
		}

		if strings.TrimSpace(pc.Type) == "" {
			return nil, fmt.Errorf("provider %q: type is required", id)
		}
		build, ok := builders[pc.Type]
		if !ok {
			return nil, fmt.Errorf("provider %q: unknown type %q (known: %s)",
				id, pc.Type, strings.Join(KnownTypes(), ", "))
		}

		p, err := build(id, pc, defaults)
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", id, err)
		}
		if p.ID() != provider.ID(id) {
			// The constructor must return the provider under the name the
			// operator gave: that name goes into the client's routes, and a
			// mismatch would break provider selection.
			return nil, fmt.Errorf("provider %q: constructor returned %q", id, p.ID())
		}
		reg.Register(p)
	}

	if reg.Len() == 0 {
		return nil, fmt.Errorf("no providers configured: add a \"providers\" section to the configuration")
	}
	return reg, nil
}

// KnownTypes returns the list of registered types.
//
// Needed for the error message: an operator who made a typo must immediately
// see which variants exist.
func KnownTypes() []string {
	out := make([]string, 0, len(builders))
	for name := range builders {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// buildOpenCode assembles the OpenCode Zen provider.
func buildOpenCode(id string, pc config.ProviderConfig, defaults Defaults) (provider.Provider, error) {
	baseURL := pc.BaseURL
	if baseURL == "" {
		baseURL = opencode.DefaultBaseURL
	}

	settings := copySettings(pc.Settings)
	return opencode.New(opencode.Config{
		ID:           provider.ID(id),
		BaseURL:      baseURL,
		MetaURL:      stringSetting(settings, "meta_url"),
		APIKey:       pc.ProviderKey(),
		AnonymousKey: stringSetting(settings, "anonymous_key"),
		UserAgent:    stringSetting(settings, "user_agent"),
		ShellName:    stringSetting(settings, "shell_name"),
		OSName:       stringSetting(settings, "os_name"),
		ClientName:   stringSetting(settings, "client_name"),
		HTTP:         defaults.HTTP,
		Log:          defaults.Log,

		FirstEventTimeout:    defaults.FirstEventTimeout,
		IdleTimeout:          defaults.IdleTimeout,
		ReasoningIdleTimeout: defaults.ReasoningIdleTimeout,
	})
}

// buildStub assembles the stub provider.
//
// It reads its catalog from the settings rather than asking anyone, because there
// is no one to ask: the stub IS the service. Nothing else about it is special —
// once built, it is an ordinary provider, and the gateway takes no special path
// for it beyond the transport it offers.
func buildStub(id string, pc config.ProviderConfig, defaults Defaults) (provider.Provider, error) {
	settings := copySettings(pc.Settings)

	cfg := stub.DefaultConfig()
	if models := modelListSetting(settings, "models"); len(models) > 0 {
		cfg.Models = models
	}
	cfg.Fail = intSetting(settings, "fail", 0)
	cfg.FailStatus = intSetting(settings, "fail_status", 0)
	cfg.FailText = stringSetting(settings, "fail_text")
	cfg.Reply = stringSetting(settings, "reply")
	cfg.NoStream = boolSetting(settings, "no_stream", false)
	cfg.Delay = durationSetting(settings, "delay_ms", time.Millisecond)
	cfg.RetryAfter = durationSetting(settings, "retry_after_s", time.Second)

	return stub.NewProvider(provider.ID(id), pc.BaseURL, cfg), nil
}

// modelListSetting reads a catalog written into the settings.
//
// The entries may be bare identifiers or objects, because a hand-written
// configuration should not have to spell out fields that have defaults.
func modelListSetting(settings map[string]any, key string) []provider.Model {
	raw, ok := settings[key]
	if !ok {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil
	}

	out := make([]provider.Model, 0, len(list))
	for _, entry := range list {
		switch v := entry.(type) {
		case string:
			out = append(out, provider.Model{ID: v, Name: v, Free: true})
		case map[string]any:
			m := provider.Model{
				ID:     stringSetting(v, "id"),
				Name:   stringSetting(v, "name"),
				Free:   boolSetting(v, "free", true),
				Vision: boolSetting(v, "vision", false),
			}
			m.Context = intSetting(v, "context", 0)
			m.MaxOutput = intSetting(v, "max_output", 0)
			if m.ID == "" {
				continue
			}
			if m.Name == "" {
				m.Name = m.ID
			}
			out = append(out, m)
		}
	}
	return out
}

// intSetting reads an integer setting.
func intSetting(settings map[string]any, key string, fallback int) int {
	switch v := settings[key].(type) {
	case int:
		return v
	case float64:
		return int(v)
	default:
		return fallback
	}
}

// boolSetting reads a boolean setting.
func boolSetting(settings map[string]any, key string, fallback bool) bool {
	if v, ok := settings[key].(bool); ok {
		return v
	}
	return fallback
}

// durationSetting reads a number of units as a duration.
//
// The unit is in the setting's name (delay_ms, retry_after_s), and that is
// deliberate: a bare number with no unit is a mistake waiting to be made.
func durationSetting(settings map[string]any, key string, unit time.Duration) time.Duration {
	n := intSetting(settings, key, 0)
	if n <= 0 {
		return 0
	}
	return time.Duration(n) * unit
}

// copySettings copies the settings so that the constructor cannot change the
// configuration through the map it received.
func copySettings(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// stringSetting reads a string setting.
//
// A wrong type is ignored rather than counted as an error: a setting with a
// foreign type almost always means a typo in the name, and failing over it is
// worse than applying the default.
func stringSetting(settings map[string]any, key string) string {
	v, ok := settings[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// stringMapSetting reads a string-map setting.
func stringMapSetting(settings map[string]any, key string) map[string]string {
	raw, ok := settings[key]
	if !ok {
		return nil
	}
	source, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(source))
	for k, v := range source {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
