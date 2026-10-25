// Package axes is the dictionary of every setting the daemon has.
//
// WHY A DICTIONARY AT ALL. The same settings are declared in three places that
// have to agree: the Go configuration, the command line, and the NixOS module.
// Three literal lists drift apart, and the drift is silent — a module option the
// binary does not know about simply does nothing, and nothing says so. The
// dictionary is written once, printed by `axes`, and the module's checks are run
// against that output.
//
// A VALUE LIST IS NEVER WRITTEN TWICE. The catalog reads config.CredentialValues
// and its siblings instead of repeating "anonymous|account", so the validation
// the daemon performs and the values the module offers cannot disagree.
//
// THE PACKAGE DEPENDS ON config AND NOTHING ELSE. The provider and transport
// types are handed in by the caller: discovering them here would mean importing
// every provider, which is the direction the dependency must not run.
package axes

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"free-ai-router/internal/config"
)

// Version is the schema version of the printed document.
//
// It exists so that a consumer — the NixOS module's drift check — can tell a
// changed shape apart from changed contents, instead of failing on a field that
// moved.
const Version = 1

// Kind is what a value looks like.
type Kind string

const (
	// KindEnum — one of a fixed set. Values is filled in.
	KindEnum Kind = "enum"
	// KindString — free-form text.
	KindString Kind = "string"
	// KindDuration — a Go duration string such as "30s".
	KindDuration Kind = "duration"
	// KindPath — a filesystem path.
	KindPath Kind = "path"
	// KindBool — a truth value.
	KindBool Kind = "bool"
)

// Scope is where a setting applies.
type Scope string

const (
	// ScopeGlobal — one value for the whole process.
	ScopeGlobal Scope = "global"
	// ScopeProvider — one value per provider, overriding the global one.
	//
	// Per-provider settings are file-only: their names depend on the provider
	// entries, and a flag would need a selector for each. The NixOS module
	// writes them into the file, which is the same mechanism.
	ScopeProvider Scope = "provider"
)

// Axis is one setting.
type Axis struct {
	// Name — the axis, and the `axes` output name.
	Name string `json:"name"`
	// Kind — what a value looks like.
	Kind Kind `json:"kind"`
	// Values — the allowed values of an enum. Empty for other kinds.
	Values []string `json:"values,omitempty"`
	// Default — the built-in value, as printed. Empty for a provider-scoped
	// axis, where empty means "inherit the global one".
	Default string `json:"default"`
	// Scope — global or per-provider.
	Scope Scope `json:"scope"`
	// Flag — the command-line flag that sets it, without the leading dashes.
	Flag string `json:"flag"`
	// Env — the environment variable that sets it.
	Env string `json:"env,omitempty"`
	// ConfigKey — the key in config.json. For a provider-scoped axis it
	// contains <id> where the provider's name goes.
	ConfigKey string `json:"config_key"`
	// InConfigFile — whether writing the file has any effect on this axis.
	//
	// False for the two path overrides, and only for them: they choose WHICH
	// file is read, so they cannot live inside it. A consumer that renders the
	// file — the NixOS module — has to know the difference, or it will offer an
	// option whose value goes nowhere.
	InConfigFile bool `json:"in_config_file"`
	// Secret — the value must never be printed or logged.
	Secret bool `json:"secret,omitempty"`
	// Description — one line, for the human output.
	Description string `json:"description"`
}

// Registries — the sets of names the catalog is completed from.
//
// Handed in rather than discovered: the provider and transport types live in
// their own packages, and a dictionary that imported them would have to be
// rebuilt whenever one was added.
type Registries struct {
	// Providers — the provider type identifiers, as registered.
	Providers []string `json:"providers"`
	// Transports — the transport identifiers, as registered.
	Transports []string `json:"transports"`
}

// Document is what `axes --json` prints.
type Document struct {
	Version    int        `json:"version"`
	Axes       []Axis     `json:"axes"`
	Registries Registries `json:"registries"`
}

// Catalog returns every setting, in a stable order.
//
// A function rather than a package-level slice: the returned slice is the
// caller's, and nothing that appends to it can widen the set of axes for
// everyone else in the process.
func Catalog(r Registries) []Axis {
	transports := append([]string(nil), r.Transports...)
	if len(transports) == 0 {
		// The catalog describes an axis even when the caller registered nothing:
		// an empty enum would read as "this setting accepts no value".
		transports = []string{config.DefaultTransport}
	}
	sort.Strings(transports)

	providers := append([]string(nil), r.Providers...)
	sort.Strings(providers)

	return []Axis{
		{
			Name:         "mode",
			Kind:         KindEnum,
			Values:       config.ModeValues(),
			Default:      config.DefaultMode,
			Scope:        ScopeGlobal,
			Flag:         "mode",
			Env:          config.EnvMode,
			ConfigKey:    "mode",
			InConfigFile: true,
			Description:  "local listens on the loopback and checks no token; server requires a token and guards every route",
		},
		{
			Name:         "listen",
			Kind:         KindString,
			Default:      config.DefaultListen,
			Scope:        ScopeGlobal,
			Flag:         "listen",
			Env:          config.EnvListen,
			ConfigKey:    "listen",
			InConfigFile: true,
			Description:  "the address the daemon binds",
		},
		{
			Name:         "token",
			Kind:         KindString,
			Default:      "",
			Scope:        ScopeGlobal,
			Flag:         "token",
			Env:          config.EnvToken,
			ConfigKey:    "token",
			InConfigFile: true,
			Secret:       true,
			Description:  "the shared secret clients must present in server mode",
		},
		{
			Name:         "transport",
			Kind:         KindEnum,
			Values:       transports,
			Default:      config.DefaultTransport,
			Scope:        ScopeGlobal,
			Flag:         "transport",
			Env:          config.EnvTransport,
			ConfigKey:    "transport",
			InConfigFile: true,
			Description:  "how packets leave the host: direct, or through a rotating pool of proxy servers",
		},
		{
			Name:         "upstream",
			Kind:         KindString,
			Default:      config.DefaultUpstream,
			Scope:        ScopeGlobal,
			Flag:         "upstream",
			Env:          config.EnvUpstream,
			ConfigKey:    "upstream",
			InConfigFile: true,
			Description:  "the service the daemon forwards to; empty when each provider names its own",
		},
		{
			Name:         "family",
			Kind:         KindEnum,
			Values:       config.FamilyValues(),
			Default:      config.DefaultFamily,
			Scope:        ScopeGlobal,
			Flag:         "family",
			Env:          config.EnvFamily,
			ConfigKey:    "family",
			InConfigFile: true,
			Description:  "the address family the traffic leaves from; it decides which address the provider counts its limits against",
		},
		{
			Name:         "credential",
			Kind:         KindEnum,
			Values:       config.CredentialValues(),
			Default:      config.DefaultCredential,
			Scope:        ScopeGlobal,
			Flag:         "credential",
			Env:          config.EnvCredential,
			ConfigKey:    "credential",
			InConfigFile: true,
			Description:  "what the daemon presents to the provider: anonymous presents nothing even when keys are configured, account presents a key and refuses to start without one",
		},
		{
			Name:         "key_rotation",
			Kind:         KindEnum,
			Values:       config.RotationValues(),
			Default:      config.DefaultKeyRotation,
			Scope:        ScopeGlobal,
			Flag:         "rotate",
			Env:          config.EnvRotation,
			ConfigKey:    "key_rotation",
			InConfigFile: true,
			Description:  "whether to spend the pool on a refusal that names the key; ignored under the anonymous credential, where no key is presented and there is nothing to replace",
		},
		{
			Name:         "model_fallback",
			Kind:         KindEnum,
			Values:       config.FallbackValues(),
			Default:      config.DefaultModelFallback,
			Scope:        ScopeGlobal,
			Flag:         "model-fallback",
			Env:          config.EnvModelFallback,
			ConfigKey:    "model_fallback",
			InConfigFile: true,
			Description:  "whether a model that cannot answer at all is replaced by another free model of the same provider; the answer then names a model other than the one asked for, so it is off by default",
		},
		{
			Name:         "config",
			Kind:         KindPath,
			Default:      "",
			Scope:        ScopeGlobal,
			Flag:         "config",
			Env:          config.EnvConfig,
			ConfigKey:    "",
			InConfigFile: false,
			Description:  "the configuration file to read, instead of the one under the XDG root",
		},
		{
			Name:         "state",
			Kind:         KindPath,
			Default:      "",
			Scope:        ScopeGlobal,
			Flag:         "state",
			Env:          config.EnvState,
			ConfigKey:    "",
			InConfigFile: false,
			Description:  "the state file to write; the journal follows its directory",
		},
		{
			Name:         "first_event_timeout",
			Kind:         KindDuration,
			Default:      config.DefaultFirstEventTimeout.String(),
			Scope:        ScopeGlobal,
			Flag:         "first-event-timeout",
			Env:          config.EnvFirstEventTimeout,
			ConfigKey:    "first_event_timeout",
			InConfigFile: true,
			Description:  "how long to wait for the first streamed event before giving up",
		},
		{
			Name:         "idle_timeout",
			Kind:         KindDuration,
			Default:      config.DefaultIdleTimeout.String(),
			Scope:        ScopeGlobal,
			Flag:         "idle-timeout",
			Env:          config.EnvIdleTimeout,
			ConfigKey:    "idle_timeout",
			InConfigFile: true,
			Description:  "how long a stream may stay silent between events",
		},
		{
			Name:         "reasoning_idle_timeout",
			Kind:         KindDuration,
			Default:      config.DefaultReasoningIdleTimeout.String(),
			Scope:        ScopeGlobal,
			Flag:         "reasoning-idle-timeout",
			Env:          config.EnvReasoningIdleTimeout,
			ConfigKey:    "reasoning_idle_timeout",
			InConfigFile: true,
			Description:  "the longer silence allowed while the model is reasoning",
		},

		// The pool transport. These live in transport_settings.pool rather than at the
		// top level: they describe one transport, and a flat key would belong to a
		// transport that is not in use. The flags exist for trying a value out; the
		// file is where the value is adopted, and the module renders it there.
		{
			Name:         "pool.source",
			Kind:         KindString,
			Default:      "proxprobe",
			Scope:        ScopeGlobal,
			Flag:         "pool-source",
			Env:          config.EnvPoolSource,
			ConfigKey:    "transport_settings.pool.source",
			InConfigFile: true,
			Description:  "where the list comes from; proxprobe is the only source",
		},
		{
			Name:         "pool.proxprobe_workdir",
			Kind:         KindPath,
			Default:      "",
			Scope:        ScopeGlobal,
			Flag:         "pool-proxprobe-workdir",
			Env:          config.EnvPoolProxprobeDir,
			ConfigKey:    "transport_settings.pool.proxprobe_workdir",
			InConfigFile: true,
			Description:  "the pass's work directory: merged.jsonl and http.jsonl are read from here",
		},
		{
			Name:         "pool.proxprobe_command",
			Kind:         KindString,
			Default:      "",
			Scope:        ScopeGlobal,
			Flag:         "pool-proxprobe-command",
			Env:          config.EnvPoolProxprobeCmd,
			ConfigKey:    "transport_settings.pool.proxprobe_command",
			InConfigFile: true,
			Description:  "the pass argv, not a shell line; empty means an outside scheduler runs the pass",
		},
		{
			Name:         "pool.proxprobe_max_age",
			Kind:         KindDuration,
			Default:      "",
			Scope:        ScopeGlobal,
			Flag:         "pool-proxprobe-max-age",
			Env:          config.EnvPoolProxprobeAge,
			ConfigKey:    "transport_settings.pool.proxprobe_max_age",
			InConfigFile: true,
			Description:  "how old a pass may be before it is refused; empty means one interval",
		},
		{
			Name:         "pool.pass_timeout",
			Kind:         KindDuration,
			Default:      "10m",
			Scope:        ScopeGlobal,
			Flag:         "pool-pass-timeout",
			Env:          config.EnvPoolPassTimeout,
			ConfigKey:    "transport_settings.pool.pass_timeout",
			InConfigFile: true,
			Description:  "how long one pass may run",
		},
		{
			Name:         "pool.core_path",
			Kind:         KindPath,
			Default:      "",
			Scope:        ScopeGlobal,
			Flag:         "pool-core-path",
			Env:          config.EnvPoolCorePath,
			ConfigKey:    "transport_settings.pool.core_path",
			InConfigFile: true,
			Description:  "the core binary; empty means look up mihomo on PATH",
		},
		{
			Name:         "pool.interval",
			Kind:         KindDuration,
			Default:      "8h",
			Scope:        ScopeGlobal,
			Flag:         "pool-interval",
			Env:          config.EnvPoolInterval,
			ConfigKey:    "transport_settings.pool.interval",
			InConfigFile: true,
			Description:  "how often the list is rebuilt; the first check of the day is first_eval_utc",
		},
		{
			Name:         "pool.max_nodes",
			Kind:         KindString,
			Default:      "20",
			Scope:        ScopeGlobal,
			Flag:         "pool-max-nodes",
			Env:          config.EnvPoolMaxNodes,
			ConfigKey:    "transport_settings.pool.max_nodes",
			InConfigFile: true,
			Description:  "how many measured servers the relevant list keeps",
		},
		{
			Name:         "pool.probe_model",
			Kind:         KindString,
			Default:      "",
			Scope:        ScopeGlobal,
			Flag:         "pool-probe-model",
			Env:          config.EnvPoolProbeModel,
			ConfigKey:    "transport_settings.pool.probe_model",
			InConfigFile: true,
			Description:  "the model the test request asks for; empty means the first free model",
		},

		// The per-provider entries. Empty means "inherit the global axis", which
		// is what the module expresses as null.
		{
			Name:         "provider.type",
			Kind:         KindEnum,
			Values:       providers,
			Default:      "",
			Scope:        ScopeProvider,
			Flag:         "",
			ConfigKey:    "providers.<id>.type",
			InConfigFile: true,
			Description:  "which implementation serves the entry",
		},
		{
			Name:         "provider.enabled",
			Kind:         KindBool,
			Default:      "true",
			Scope:        ScopeProvider,
			Flag:         "",
			ConfigKey:    "providers.<id>.enabled",
			InConfigFile: true,
			Description:  "turn an entry off without deleting it",
		},
		{
			Name:         "provider.base_url",
			Kind:         KindString,
			Default:      "",
			Scope:        ScopeProvider,
			Flag:         "",
			ConfigKey:    "providers.<id>.base_url",
			InConfigFile: true,
			Description:  "the service's address, when it differs from the built-in one",
		},
		{
			Name:         "provider.api_key",
			Kind:         KindString,
			Default:      "",
			Scope:        ScopeProvider,
			Flag:         "",
			ConfigKey:    "providers.<id>.api_key",
			InConfigFile: true,
			Secret:       true,
			Description:  "a key straight in the file; a variable is preferable, the file ends up in backups",
		},
		{
			Name:         "provider.api_key_env",
			Kind:         KindString,
			Default:      "",
			Scope:        ScopeProvider,
			Flag:         "",
			ConfigKey:    "providers.<id>.api_key_env",
			InConfigFile: true,
			Description:  "the name of the variable holding a key",
		},
		{
			Name:         "provider.credential",
			Kind:         KindEnum,
			Values:       config.CredentialValues(),
			Default:      "",
			Scope:        ScopeProvider,
			Flag:         "",
			ConfigKey:    "providers.<id>.credential",
			InConfigFile: true,
			Description:  "this provider's credential axis; empty inherits the global one",
		},
		{
			Name:         "provider.key_rotation",
			Kind:         KindEnum,
			Values:       config.RotationValues(),
			Default:      "",
			Scope:        ScopeProvider,
			Flag:         "",
			ConfigKey:    "providers.<id>.key_rotation",
			InConfigFile: true,
			Description:  "this provider's key rotation axis; empty inherits the global one",
		},
		{
			Name:         "provider.settings",
			Kind:         KindString,
			Default:      "",
			Scope:        ScopeProvider,
			Flag:         "",
			ConfigKey:    "providers.<id>.settings",
			InConfigFile: true,
			Description:  "whatever the provider type itself understands; the daemon does not interpret it",
		},
	}
}

// BuildDocument assembles the printable document.
func BuildDocument(r Registries) Document {
	return Document{
		Version:    Version,
		Axes:       Catalog(r),
		Registries: Registries{Providers: sortedCopy(r.Providers), Transports: sortedCopy(r.Transports)},
	}
}

// JSON renders the document.
func JSON(r Registries) ([]byte, error) {
	raw, err := json.MarshalIndent(BuildDocument(r), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("axes: rendering the catalog: %w", err)
	}
	return append(raw, '\n'), nil
}

// Find returns the axis with the given name.
func Find(r Registries, name string) (Axis, bool) {
	for _, a := range Catalog(r) {
		if a.Name == name {
			return a, true
		}
	}
	return Axis{}, false
}

// Text renders the catalog for a human.
//
// The columns are the ones an operator needs to move between the three places a
// setting can live: what it is called, what it accepts, what it starts as, and
// how to set it without editing the file.
func Text(r Registries) string {
	var b strings.Builder
	for _, a := range Catalog(r) {
		values := strings.Join(a.Values, "|")
		if values == "" {
			values = string(a.Kind)
		}
		fmt.Fprintf(&b, "%-24s %-24s default=%-12s", a.Name, values, or(a.Default, "-"))

		if a.Flag != "" {
			fmt.Fprintf(&b, " --%s", a.Flag)
		}
		if a.Env != "" {
			fmt.Fprintf(&b, " $%s", a.Env)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// or returns the value or a substitute for an empty one.
func or(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// sortedCopy returns a sorted copy of the slice.
func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
