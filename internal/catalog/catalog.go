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
// ("opencode", "openai"). The mapping "type → constructor" is given by the table
// below. A new type is registered with a single line.
package catalog

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"free-ai-router/internal/config"
	"free-ai-router/internal/provider"
	"free-ai-router/internal/provider/openai"
	"free-ai-router/internal/provider/opencode"
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
}

// builders — the mapping "provider type → constructor".
//
// A table, not a switch: adding a type does not touch the assembly logic, and
// that logic cannot be broken by accident while appending a branch.
var builders = map[string]Builder{
	// A full-fledged provider with a contract of its own.
	"opencode": buildOpenCode,

	// A template for any OpenAI-compatible service. Named neutrally: both
	// ZenMux and Freebuff work under it, and so does anything speaking the same
	// protocol.
	"openai": buildOpenAI,
}

// Build assembles the provider registry from the configuration.
//
// Providers with enabled: false are skipped — that is the way to turn a section
// off without deleting it. A provider without models is not an error: some
// services have their catalog discovered later, and the gateway itself works
// without one.
func Build(cfg *config.Config) (*provider.Registry, error) {
	defaults := Defaults{Upstream: cfg.Upstream.Val}

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
		BaseURL:      baseURL,
		APIKey:       pc.ProviderKey(),
		AnonymousKey: stringSetting(settings, "anonymous_key"),
		UserAgent:    stringSetting(settings, "user_agent"),
		ShellName:    stringSetting(settings, "shell_name"),
		OSName:       stringSetting(settings, "os_name"),
		ClientName:   stringSetting(settings, "client_name"),
		HTTP:         defaults.HTTP,
	})
}

// buildOpenAI assembles a provider on the OpenAI-compatible protocol.
//
// This is where the extension point lives: to hook up a new service (ZenMux,
// Freebuff and the like), a section in the configuration with the type "openai"
// is enough. No code needs to be written.
func buildOpenAI(id string, pc config.ProviderConfig, defaults Defaults) (provider.Provider, error) {
	baseURL := pc.BaseURL
	if baseURL == "" {
		// The shared default address is good only as a hint: every service has
		// its own. But if the operator set upstream globally, taking it is more
		// reasonable than refusing.
		baseURL = defaults.Upstream
	}
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("base_url is required for an openai-compatible provider")
	}

	settings := copySettings(pc.Settings)
	return openai.New(openai.Config{
		ID:             provider.ID(id),
		BaseURL:        baseURL,
		APIKey:         pc.ProviderKey(),
		AnonymousKey:   stringSetting(settings, "anonymous_key"),
		AuthHeader:     stringSetting(settings, "auth_header"),
		AuthPrefix:     stringSetting(settings, "auth_prefix"),
		UserAgent:      stringSetting(settings, "user_agent"),
		ReasoningField: stringSetting(settings, "reasoning_field"),
		ChatPath:       stringSetting(settings, "chat_path"),
		ExtraHeaders:   stringMapSetting(settings, "headers"),
		HTTP:           defaults.HTTP,
	})
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
