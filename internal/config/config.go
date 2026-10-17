// Package config assembles the daemon's configuration from three levels.
//
// THE PRECEDENCE ORDER is part of the contract, not an implementation detail:
//
//	CLI flags               beat everything
//	environment variables   override the file
//	config.json             the base layer
//
// Every value remembers where it came from. That is not decoration: without a
// source, debugging the merge turns into guesswork, and `--print-config` prints
// exactly that source.
//
// LAYOUT. The paths are determined by XDG variables rather than by the
// configuration itself: otherwise there would be a circular dependency — a file
// telling where the file lives. The three roots are separated by the nature of
// the data:
//
//	configuration   you edit it; never created by the machine, never cleaned up
//	state           survives restarts; accumulates
//	runtime         lives as long as the process does
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AppName — the name of the application's directories under the XDG roots.
const AppName = "free-ai-router"

// Paths — all the places on disk that the daemon uses.
type Paths struct {
	// ConfigDir — the configuration directory.
	ConfigDir string
	// StateDir — the state directory.
	StateDir string
	// RuntimeDir — the temporary-files directory.
	RuntimeDir string
	// ConfigFile — the configuration file.
	ConfigFile string
	// StateFile — the state file.
	StateFile string
	// LogFile — the journal.
	LogFile string
	// PIDFile — the running daemon's process identifier.
	//
	// It lives in the runtime directory rather than beside the state: the
	// identifier describes THIS boot, and a file left behind by a previous one
	// would name a process that no longer exists or, worse, a different
	// process that has since reused the number. The runtime directory is
	// cleared by the system, which is the lifetime the file needs.
	PIDFile string
}

// DefaultPaths resolves the XDG directories.
//
// The variables are read through os.Getenv rather than through
// os.UserConfigDir: the latter substitutes a platform-specific directory (on
// macOS, Library), whereas the rest of the system expects an XDG path.
func DefaultPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("config: cannot resolve the home directory: %w", err)
	}

	configHome := envOr("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	stateHome := envOr("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	runtimeHome := envOr("XDG_RUNTIME_DIR", filepath.Join(stateHome, AppName, "run"))

	configDir := filepath.Join(configHome, AppName)
	stateDir := filepath.Join(stateHome, AppName)

	return Paths{
		ConfigDir:  configDir,
		StateDir:   stateDir,
		RuntimeDir: filepath.Join(runtimeHome, AppName),
		ConfigFile: filepath.Join(configDir, "config.json"),
		StateFile:  filepath.Join(stateDir, "state.json"),
		LogFile:    filepath.Join(stateDir, "app.log"),
		PIDFile:    filepath.Join(runtimeHome, AppName+".pid"),
	}, nil
}

// envOr returns the value of a variable or the fallback.
func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

// Source indicates where a setting's value came from.
//
// The source is needed for `--print-config`: without it the operator cannot
// tell a value from a flag apart from a value from the file.
type Source string

const (
	// SourceDefault — a built-in value.
	SourceDefault Source = "default"
	// SourceFile — from config.json.
	SourceFile Source = "config.json"
	// SourceEnv — from an environment variable.
	SourceEnv Source = "env"
	// SourceFlag — from a command-line flag.
	SourceFlag Source = "flag"
)

// Value — a single setting value together with its source.
type Value[T any] struct {
	Val  T
	From Source
}

// Set records the value and its source.
func (v *Value[T]) Set(val T, from Source) {
	v.Val = val
	v.From = from
}

// OrDefault stamps the "default" source if none has been set yet.
func (v *Value[T]) OrDefault() {
	if v.From == "" {
		v.From = SourceDefault
	}
}

// Config — the fully resolved configuration.
type Config struct {
	// --- operating mode ----------------------------------------------------

	// Mode — the deployment shape.
	Mode Value[string]
	// Listen — the listen address.
	Listen Value[string]
	// Token — the shared secret for server mode. Separate from the provider's
	// credentials: the client authenticates WITH IT, while the gateway
	// authenticates with the provider's key.
	Token Value[string]

	// --- providers and mechanisms ------------------------------------------

	// Providers — the provider descriptions keyed by their identifiers.
	Providers map[string]ProviderConfig

	// --- transport ---------------------------------------------------------

	// Transport — the transport identifier.
	Transport Value[string]
	// TransportConfig — the transport settings.
	TransportConfig map[string]any

	// --- timings -----------------------------------------------------------

	// FirstEventTimeout — the budget until the first response headers.
	FirstEventTimeout Value[time.Duration]
	// IdleTimeout — the silence budget of an ordinary stream.
	IdleTimeout Value[time.Duration]
	// ReasoningIdleTimeout — the silence budget for reasoning models.
	ReasoningIdleTimeout Value[time.Duration]

	// --- misc --------------------------------------------------------------

	// Upstream — the default base address for providers that did not name their
	// own. Needed rarely, but it keeps the address from being repeated in every
	// section.
	Upstream Value[string]

	// Paths — the places on disk. Not a setting: determined by XDG.
	Paths Paths
}

// ProviderConfig — the settings of a single provider.
//
// The settings are stored as a map rather than as a struct: different provider
// types have different fields, and a shared struct would describe only the
// intersection. Parsing the values is the provider's own business, and it is
// likewise the provider that reports the error.
type ProviderConfig struct {
	// Type — which code serves this entry ("opencode", "openai").
	Type string `json:"type"`
	// Enabled allows an entry to be turned off without deleting it.
	Enabled *bool `json:"enabled,omitempty"`

	// The fields below are common to all providers, because nearly all of them
	// need these. Anything not found here is set in Settings.

	// BaseURL — the service's address.
	BaseURL string `json:"base_url,omitempty"`
	// APIKey — the credentials straight in the file.
	APIKey string `json:"api_key,omitempty"`
	// APIKeyEnv — the name of the variable holding the key. Preferable to a key
	// in the file: the configuration often ends up in backups.
	APIKeyEnv string `json:"api_key_env,omitempty"`

	// Settings — everything else a specific provider type understands.
	Settings map[string]any `json:"settings,omitempty"`
}

// IsEnabled reports whether the entry is enabled.
//
// A missing field means "enabled": an operator who added a section expects it
// to work.
func (p ProviderConfig) IsEnabled() bool {
	if p.Enabled == nil {
		return true
	}
	return *p.Enabled
}

// Operating modes.
const (
	// ModeLocal — the gateway and the client on one machine. The token is not
	// checked at all: nobody but this machine can reach a loopback listener.
	ModeLocal = "local"
	// ModeServer — the gateway on a server, the client on another machine. The
	// token is mandatory, otherwise an open relay spends the operator's quota
	// for anyone who finds the port.
	ModeServer = "server"
)

// Default modes.
const (
	DefaultMode      = ModeLocal
	DefaultListen    = "127.0.0.1:8787"
	DefaultTransport = "direct"
	DefaultUpstream  = "https://opencode.ai"

	DefaultFirstEventTimeout    = 30 * time.Second
	DefaultIdleTimeout          = 120 * time.Second
	DefaultReasoningIdleTimeout = 300 * time.Second
)

// Default returns a configuration with the built-in values.
func Default() *Config {
	c := &Config{
		Providers:       make(map[string]ProviderConfig),
		TransportConfig: make(map[string]any),
	}
	c.Mode.Set(DefaultMode, SourceDefault)
	c.Listen.Set(DefaultListen, SourceDefault)
	c.Token.Set("", SourceDefault)
	c.Transport.Set(DefaultTransport, SourceDefault)
	c.Upstream.Set(DefaultUpstream, SourceDefault)
	c.FirstEventTimeout.Set(DefaultFirstEventTimeout, SourceDefault)
	c.IdleTimeout.Set(DefaultIdleTimeout, SourceDefault)
	c.ReasoningIdleTimeout.Set(DefaultReasoningIdleTimeout, SourceDefault)
	return c
}

// IsServer reports whether token checking is in force.
func (c *Config) IsServer() bool { return c.Mode.Val == ModeServer }

// Load reads the configuration: the file, then the environment.
//
// A missing file is not an error — the built-in values work on their own. A
// parse error is an error: a silent fallback to the defaults would hide a typo.
func Load() (*Config, error) {
	paths, err := DefaultPaths()
	if err != nil {
		return nil, err
	}
	return LoadFrom(paths)
}

// LoadFrom reads the configuration from the given paths.
func LoadFrom(paths Paths) (*Config, error) {
	c := Default()
	c.Paths = paths

	if err := c.applyFile(paths.ConfigFile); err != nil {
		return nil, err
	}
	if err := c.applyEnv(); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// fileShape — the shape of the configuration file.
//
// A separate type, not Config itself: in the file a value is just a value,
// while the source only needs to be known after the merge.
type fileShape struct {
	Mode                 *string                   `json:"mode"`
	Listen               *string                   `json:"listen"`
	Token                *string                   `json:"token"`
	Transport            *string                   `json:"transport"`
	TransportSettings    map[string]any            `json:"transport_settings"`
	Upstream             *string                   `json:"upstream"`
	FirstEventTimeout    *string                   `json:"first_event_timeout"`
	IdleTimeout          *string                   `json:"idle_timeout"`
	ReasoningIdleTimeout *string                   `json:"reasoning_idle_timeout"`
	Providers            map[string]ProviderConfig `json:"providers"`
}

// applyFile layers the file over the built-in values.
func (c *Config) applyFile(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no file — the defaults work
		}
		return fmt.Errorf("config: reading %s: %w", path, err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}

	var shape fileShape
	if err := json.Unmarshal(raw, &shape); err != nil {
		return fmt.Errorf("config: parsing %s: %w", path, err)
	}

	if shape.Mode != nil {
		c.Mode.Set(*shape.Mode, SourceFile)
	}
	if shape.Listen != nil {
		c.Listen.Set(*shape.Listen, SourceFile)
	}
	if shape.Token != nil {
		c.Token.Set(*shape.Token, SourceFile)
	}
	if shape.Transport != nil {
		c.Transport.Set(*shape.Transport, SourceFile)
	}
	if shape.Upstream != nil {
		c.Upstream.Set(*shape.Upstream, SourceFile)
	}
	if shape.TransportSettings != nil {
		c.TransportConfig = shape.TransportSettings
	}
	if shape.Providers != nil {
		c.Providers = shape.Providers
	}

	for _, d := range []struct {
		name string
		src  *string
		dst  *Value[time.Duration]
	}{
		{"first_event_timeout", shape.FirstEventTimeout, &c.FirstEventTimeout},
		{"idle_timeout", shape.IdleTimeout, &c.IdleTimeout},
		{"reasoning_idle_timeout", shape.ReasoningIdleTimeout, &c.ReasoningIdleTimeout},
	} {
		if d.src == nil {
			continue
		}
		v, err := time.ParseDuration(*d.src)
		if err != nil {
			return fmt.Errorf("config: invalid %s %q: %w", d.name, *d.src, err)
		}
		d.dst.Set(v, SourceFile)
	}

	return nil
}

// Environment variables. The flat axes each have a variable of their own;
// nested provider settings are set by file only, because their shape depends on
// the provider type.
const (
	EnvMode      = "FREE_AI_ROUTER_MODE"
	EnvListen    = "FREE_AI_ROUTER_LISTEN"
	EnvToken     = "FREE_AI_ROUTER_TOKEN"
	EnvTransport = "FREE_AI_ROUTER_TRANSPORT"
	EnvUpstream  = "FREE_AI_ROUTER_UPSTREAM"
	// EnvState overrides the state file — so that a one-off run does not touch
	// the working state.
	EnvState = "FREE_AI_ROUTER_STATE"
)

// applyEnv layers the environment over the file.
func (c *Config) applyEnv() error {
	for _, e := range []struct {
		name string
		dst  *Value[string]
	}{
		{EnvMode, &c.Mode},
		{EnvListen, &c.Listen},
		{EnvToken, &c.Token},
		{EnvTransport, &c.Transport},
		{EnvUpstream, &c.Upstream},
	} {
		if v := os.Getenv(e.name); v != "" {
			e.dst.Set(v, SourceEnv)
		}
	}

	// The state override affects only the path, not the axis value.
	if v := strings.TrimSpace(os.Getenv(EnvState)); v != "" {
		c.Paths.StateFile = v
		c.Paths.StateDir = filepath.Dir(v)
	}
	return nil
}

// ApplyFlags layers the values from the command line.
//
// Only non-empty values are accepted: an empty string means "the flag was not
// given", not "set it empty".
func (c *Config) ApplyFlags(mode, listen, token, transport, upstream string) {
	if mode != "" {
		c.Mode.Set(mode, SourceFlag)
	}
	if listen != "" {
		c.Listen.Set(listen, SourceFlag)
	}
	if token != "" {
		c.Token.Set(token, SourceFlag)
	}
	if transport != "" {
		c.Transport.Set(transport, SourceFlag)
	}
	if upstream != "" {
		c.Upstream.Set(upstream, SourceFlag)
	}
}

// Validate checks the invariants that hold BETWEEN axes.
//
// The only such invariant today: server mode requires a token. The gateway in
// this mode listens on a reachable address, so without a token it becomes an
// open relay, spending the operator's quota for anyone who finds the port.
// Refusing to start is the right reaction; a warning that slipped into the
// journal is not a protection.
func (c *Config) Validate() error {
	switch c.Mode.Val {
	case ModeLocal, ModeServer:
	default:
		return fmt.Errorf("config: invalid mode %q (want %s|%s)", c.Mode.Val, ModeLocal, ModeServer)
	}
	if c.Mode.Val == ModeServer && strings.TrimSpace(c.Token.Val) == "" {
		return fmt.Errorf("config: mode %q requires a token: an untokened listener on a reachable address is an open relay", ModeServer)
	}
	if strings.TrimSpace(c.Listen.Val) == "" {
		return fmt.Errorf("config: listen address must not be empty")
	}
	if strings.TrimSpace(c.Transport.Val) == "" {
		return fmt.Errorf("config: transport must not be empty")
	}
	if c.FirstEventTimeout.Val <= 0 {
		return fmt.Errorf("config: first_event_timeout must be positive")
	}
	if c.IdleTimeout.Val <= 0 {
		return fmt.Errorf("config: idle_timeout must be positive")
	}
	if c.ReasoningIdleTimeout.Val <= 0 {
		return fmt.Errorf("config: reasoning_idle_timeout must be positive")
	}
	return nil
}

// ProviderKey returns the provider's credentials.
//
// The order: the value in the file, then the variable named alongside it. The
// reverse order would be dangerous — a key in the file would outweigh an
// explicitly set variable.
func (p ProviderConfig) ProviderKey() string {
	if p.APIKey != "" {
		return p.APIKey
	}
	if p.APIKeyEnv != "" {
		return os.Getenv(p.APIKeyEnv)
	}
	return ""
}
