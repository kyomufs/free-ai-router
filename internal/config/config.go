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
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

	// Credential — what the daemon presents to the provider.
	//
	// The first of the two axes describing how a provider's limits are worked
	// around; the second is KeyRotation. A pair of them names one mechanism, and
	// because both can be set per provider as well, two providers in one process
	// can be worked around differently.
	Credential Value[string]

	// KeyRotation — whether to spend the pool on a refusal that names the key.
	//
	// Inert under CredentialAnonymous: with no credential sent there is nothing
	// to rotate, and a rejection is attributed to the address instead.
	KeyRotation Value[string]

	// --- transport ---------------------------------------------------------

	// Transport — the transport identifier.
	Transport Value[string]
	// TransportConfig — the transport settings.
	TransportConfig map[string]any

	// Family — the address family the daemon's traffic leaves from.
	//
	// It decides which address the provider sees, and that address is what the
	// anonymous limits are counted against. Switching families is how a second
	// budget is obtained without a proxy.
	Family Value[string]

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

	// Credential — this provider's credential axis, overriding the global one.
	//
	// Empty means "inherit". At least two providers in one process is the
	// normal case: one is reached anonymously because its free tier is worth it,
	// another only with a key.
	Credential string `json:"credential,omitempty"`
	// KeyRotation — this provider's key rotation axis, overriding the global
	// one. Empty means "inherit".
	KeyRotation string `json:"key_rotation,omitempty"`

	// Settings — everything else a specific provider type understands.
	Settings map[string]any `json:"settings,omitempty"`
}

// EffectiveCredential returns the credential axis in force for the provider.
func (p ProviderConfig) EffectiveCredential(global string) string {
	if p.Credential != "" {
		return p.Credential
	}
	return global
}

// EffectiveRotation returns the key rotation axis in force for the provider.
func (p ProviderConfig) EffectiveRotation(global string) string {
	if p.KeyRotation != "" {
		return p.KeyRotation
	}
	return global
}

// Rotates reports whether the provider rotates the credential on a refusal.
//
// Both axes are consulted in one place, because the answer depends on both: a
// rotation requested under CredentialAnonymous is a no-op, and reporting it as
// enabled would make the monitor claim a mechanism that cannot run.
func (p ProviderConfig) Rotates(globalCredential, globalRotation string) bool {
	if p.EffectiveCredential(globalCredential) != CredentialAccount {
		return false
	}
	return p.EffectiveRotation(globalRotation) == RotationOn
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

// ModeValues returns the allowed values of the mode axis.
//
// The same list Validate ranges over, so "what the daemon accepts" and "what the
// dictionary offers" cannot part ways.
func ModeValues() []string {
	return []string{ModeLocal, ModeServer}
}

// Default modes.
const (
	DefaultMode      = ModeLocal
	DefaultListen    = "127.0.0.1:8787"
	DefaultTransport = "direct"
	// DefaultUpstream is empty: there is no single service the daemon forwards
	// to, so each provider names its own address and the axis only reports it.
	DefaultUpstream = ""

	DefaultFirstEventTimeout    = 30 * time.Second
	DefaultIdleTimeout          = 120 * time.Second
	DefaultReasoningIdleTimeout = 300 * time.Second
)

// The credential axis — what the daemon presents to the provider.
const (
	// CredentialAnonymous — no credential.
	//
	// The service identifies the caller by the egress address and applies its
	// free tier to that address. This is a normal mode, not a missing setting:
	// it is how the free tier is used at all.
	CredentialAnonymous = "anonymous"
	// CredentialAccount — a key from the pool.
	CredentialAccount = "account"
)

// CredentialValues returns the allowed values of the credential axis.
//
// A function returning a fresh slice rather than a package-level variable: the
// validation, the `axes` dictionary and the NixOS drift check all read it, and
// a caller able to append to the shared slice would silently widen the axis for
// everyone.
func CredentialValues() []string {
	return []string{CredentialAnonymous, CredentialAccount}
}

// The key rotation axis — whether the pool is spent on a refusal that names the
// key.
const (
	// RotationOn — ask for another key and retry once.
	RotationOn = "on"
	// RotationOff — hand the refusal to the client as it arrived.
	//
	// The right choice when the keys are equivalent and a rejection is more
	// likely to describe the address than the key.
	RotationOff = "off"
)

// RotationValues returns the allowed values of the key rotation axis.
func RotationValues() []string {
	return []string{RotationOn, RotationOff}
}

// The address family axis — which address the traffic leaves from.
const (
	// FamilyAuto — let the system pick, in the order the resolver returns.
	FamilyAuto = "auto"
	// FamilyV4 — IPv4 only.
	//
	// Pinned rather than merely preferred: the address is what the anonymous
	// limits are counted against, so a connection that quietly moved to another
	// family would spend a different budget than the one being watched.
	FamilyV4 = "v4"
	// FamilyV6 — IPv6 only.
	FamilyV6 = "v6"
)

// FamilyValues returns the allowed values of the address family axis.
func FamilyValues() []string {
	return []string{FamilyAuto, FamilyV4, FamilyV6}
}

// Default values of the new axes.
const (
	// DefaultCredential is anonymous.
	//
	// The free tier is reached without a key, and requiring a key to start would
	// make the daemon refuse to run in its most common deployment.
	DefaultCredential = CredentialAnonymous

	// DefaultKeyRotation is on.
	//
	// The point of keeping a pool is to survive a key being rejected, and an
	// operator who put keys in the pool meant them to be used. The option is
	// inert under CredentialAnonymous, where there is nothing to rotate.
	DefaultKeyRotation = RotationOn

	// DefaultFamily is auto.
	DefaultFamily = FamilyAuto
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
	c.Credential.Set(DefaultCredential, SourceDefault)
	c.KeyRotation.Set(DefaultKeyRotation, SourceDefault)
	c.Family.Set(DefaultFamily, SourceDefault)
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
//
// It writes nothing back into the paths: what comes in is what is used, so a
// caller can hold several configurations in one process — which is what the
// flag-driven test mode needs.
func LoadFrom(paths Paths) (*Config, error) {
	return load(paths, true)
}

// LoadUnchecked reads the configuration WITHOUT judging whether the daemon could
// start with it.
//
// For the commands that edit the file one key at a time. A configuration that is
// temporarily incomplete — server mode before its token has been written — must
// stay repairable, and a loader that refuses it would leave the operator unable
// to fix it with the very command that broke it.
//
// Everything else that starts or answers for the daemon goes through LoadFrom:
// there the question is whether the configuration would run, and the answer has
// to be no when it would not.
func LoadUnchecked(paths Paths) (*Config, error) {
	return load(paths, false)
}

// load reads the configuration from the given paths, checking it if asked.
func load(paths Paths, validate bool) (*Config, error) {
	c := Default()
	c.Paths = paths

	if err := c.applyFile(paths.ConfigFile); err != nil {
		return nil, err
	}
	if err := c.applyEnv(); err != nil {
		return nil, err
	}
	if validate {
		if err := c.Validate(); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// LoadWith reads the configuration from the given paths moved by the overrides.
//
// It cannot be expressed as an override applied after LoadFrom: the override
// decides which file is read, and a value layered on afterwards would have been
// read from the wrong file. Hence a loader that takes the overrides, rather than
// a setter.
func LoadWith(paths Paths, ov PathOverrides) (*Config, error) {
	return LoadFrom(ResolvePaths(paths, ov))
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
	Credential           *string                   `json:"credential"`
	KeyRotation          *string                   `json:"key_rotation"`
	Family               *string                   `json:"family"`
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
	// Unknown keys are refused, not ignored.
	//
	// A key nobody reads is indistinguishable from a key that works: the setting
	// is written, the daemon starts, and the value never takes effect. That is
	// exactly how the timeout options stayed inert — one side wrote
	// "firstEventTimeout" while the other read "first_event_timeout", and
	// nothing anywhere said so. A refusal names the offending key instead.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&shape); err != nil {
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
	if shape.Credential != nil {
		c.Credential.Set(*shape.Credential, SourceFile)
	}
	if shape.KeyRotation != nil {
		c.KeyRotation.Set(*shape.KeyRotation, SourceFile)
	}
	if shape.Family != nil {
		c.Family.Set(*shape.Family, SourceFile)
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
	// EnvCredential, EnvRotation and EnvFamily are the two mechanism axes and
	// the addressing axis.
	EnvCredential = "FREE_AI_ROUTER_CREDENTIAL"
	EnvRotation   = "FREE_AI_ROUTER_KEY_ROTATION"
	EnvFamily     = "FREE_AI_ROUTER_FAMILY"
	// The timeout variables. They exist so that every setting the file has can
	// also be set without a file — the daemon has to be testable on a machine
	// where the configuration belongs to something else.
	EnvFirstEventTimeout    = "FREE_AI_ROUTER_FIRST_EVENT_TIMEOUT"
	EnvIdleTimeout          = "FREE_AI_ROUTER_IDLE_TIMEOUT"
	EnvReasoningIdleTimeout = "FREE_AI_ROUTER_REASONING_IDLE_TIMEOUT"
	// EnvConfig moves the configuration file itself.
	//
	// It is not an ordinary setting: the file decides which file is read, so
	// this one is resolved before the file is opened.
	EnvConfig = "FREE_AI_ROUTER_CONFIG"
	// EnvState overrides the state file — so that a one-off run does not touch
	// the working state.
	EnvState = "FREE_AI_ROUTER_STATE"
)

// applyEnv layers the environment over the file.
//
// The path overrides are deliberately absent: they are applied by ResolvePaths
// before the file is read, because reading the file is what they decide.
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
		{EnvCredential, &c.Credential},
		{EnvRotation, &c.KeyRotation},
		{EnvFamily, &c.Family},
	} {
		if v := strings.TrimSpace(os.Getenv(e.name)); v != "" {
			e.dst.Set(v, SourceEnv)
		}
	}

	for _, e := range []struct {
		name string
		dst  *Value[time.Duration]
	}{
		{EnvFirstEventTimeout, &c.FirstEventTimeout},
		{EnvIdleTimeout, &c.IdleTimeout},
		{EnvReasoningIdleTimeout, &c.ReasoningIdleTimeout},
	} {
		v := strings.TrimSpace(os.Getenv(e.name))
		if v == "" {
			continue
		}
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("config: invalid %s %q: %w", e.name, v, err)
		}
		e.dst.Set(d, SourceEnv)
	}
	return nil
}

// PathOverrides — the paths that the environment and the command line may move.
//
// Only these two: the rest are derived from them, and a directory that could be
// moved on its own would let the log and the state end up in different places.
type PathOverrides struct {
	// ConfigFile — the configuration file to read.
	ConfigFile string
	// StateFile — the state file to write.
	StateFile string
}

// EnvPathOverrides reads the path overrides from the environment.
func EnvPathOverrides() PathOverrides {
	return PathOverrides{
		ConfigFile: strings.TrimSpace(os.Getenv(EnvConfig)),
		StateFile:  strings.TrimSpace(os.Getenv(EnvState)),
	}
}

// ResolvePaths applies the overrides to resolved paths.
//
// The directories follow their files rather than being set separately: a caller
// that moved the state file and left the directory behind would scatter the
// data. The log follows the state directory for the same reason — a test run
// with a redirected state must not append to the real journal, or the journal
// stops describing the daemon.
func ResolvePaths(base Paths, ov PathOverrides) Paths {
	if ov.ConfigFile != "" {
		base.ConfigFile = ov.ConfigFile
		base.ConfigDir = filepath.Dir(ov.ConfigFile)
	}
	if ov.StateFile != "" {
		base.StateFile = ov.StateFile
		base.StateDir = filepath.Dir(ov.StateFile)
		base.LogFile = filepath.Join(base.StateDir, "app.log")
	}
	return base
}

// FlagValues — the values that came from the command line.
//
// A struct rather than a row of positional strings: there are eight of them,
// several are adjacent strings, and a mis-ordered call would compile and quietly
// set the wrong axis.
type FlagValues struct {
	Mode       string
	Listen     string
	Token      string
	Transport  string
	Upstream   string
	Credential string
	Rotation   string
	Family     string

	// The timeouts arrive as strings, because that is what a command line
	// carries. Parsing them here keeps the caller from having to know the
	// duration format the configuration uses.
	FirstEventTimeout    string
	IdleTimeout          string
	ReasoningIdleTimeout string
}

// ApplyFlags layers the values from the command line.
//
// Only non-empty values are accepted: an empty string means "the flag was not
// given", not "set it empty".
func (c *Config) ApplyFlags(f FlagValues) error {
	for _, a := range []struct {
		val string
		dst *Value[string]
	}{
		{f.Mode, &c.Mode},
		{f.Listen, &c.Listen},
		{f.Token, &c.Token},
		{f.Transport, &c.Transport},
		{f.Upstream, &c.Upstream},
		{f.Credential, &c.Credential},
		{f.Rotation, &c.KeyRotation},
		{f.Family, &c.Family},
	} {
		if a.val != "" {
			a.dst.Set(a.val, SourceFlag)
		}
	}

	for _, d := range []struct {
		name string
		src  string
		dst  *Value[time.Duration]
	}{
		{"first-event-timeout", f.FirstEventTimeout, &c.FirstEventTimeout},
		{"idle-timeout", f.IdleTimeout, &c.IdleTimeout},
		{"reasoning-idle-timeout", f.ReasoningIdleTimeout, &c.ReasoningIdleTimeout},
	} {
		if d.src == "" {
			continue
		}
		v, err := time.ParseDuration(d.src)
		if err != nil {
			return fmt.Errorf("config: invalid --%s %q: %w", d.name, d.src, err)
		}
		d.dst.Set(v, SourceFlag)
	}
	return nil
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
		return fmt.Errorf("config: invalid mode %q (want %s)", c.Mode.Val, strings.Join(ModeValues(), "|"))
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
	if err := checkEnum("credential", c.Credential.Val, CredentialValues()); err != nil {
		return err
	}
	if err := checkEnum("key_rotation", c.KeyRotation.Val, RotationValues()); err != nil {
		return err
	}
	if err := checkEnum("family", c.Family.Val, FamilyValues()); err != nil {
		return err
	}
	for _, name := range sortedKeys(c.Providers) {
		p := c.Providers[name]
		if p.Credential != "" {
			if err := checkEnum("providers."+name+".credential", p.Credential, CredentialValues()); err != nil {
				return err
			}
		}
		if p.KeyRotation != "" {
			if err := checkEnum("providers."+name+".key_rotation", p.KeyRotation, RotationValues()); err != nil {
				return err
			}
		}
		// A credential asked for must exist.
		//
		// Under "account" the daemon presents a key on every request, so a
		// section without one describes a deployment that cannot work. It used to
		// start anyway and quietly reach the service anonymously, which is the
		// worst of both answers: the configuration says one thing, the wire says
		// another, and nothing tells the operator which one won.
		//
		// Refused HERE, at load, rather than at the first request: the daemon's
		// job is to serve, and a deployment that cannot serve should say so
		// before it accepts a single client.
		if p.IsEnabled() && c.CredentialFor(name) == CredentialAccount && !p.HasKeys() {
			return fmt.Errorf(
				"config: provider %q: credential %q requires a key, and none is configured: set api_key, api_key_env, or settings.keys — or switch the credential to %q",
				name, CredentialAccount, CredentialAnonymous,
			)
		}
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

// checkEnum checks a value against an axis's allowed values.
//
// The error names the axis the way the configuration file spells it, so the
// message can be taken straight to the line that has to be fixed.
func checkEnum(axis, val string, allowed []string) error {
	for _, a := range allowed {
		if val == a {
			return nil
		}
	}
	return fmt.Errorf("config: invalid %s %q (want %s)", axis, val, strings.Join(allowed, "|"))
}

// sortedKeys returns the provider identifiers in a stable order.
//
// Validation reports the first bad provider, so an unordered map would make the
// error depend on the run and let one mistake hide behind another.
func sortedKeys(m map[string]ProviderConfig) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// CredentialFor returns the credential axis in force for the provider.
//
// An unknown provider falls back to the global axis rather than reporting an
// error: deciding whether a provider exists is the registry's business, and
// duplicating that decision here would give it two answers.
func (c *Config) CredentialFor(id string) string {
	return c.Providers[id].EffectiveCredential(c.Credential.Val)
}

// RotationFor returns the key rotation axis in force for the provider.
func (c *Config) RotationFor(id string) string {
	return c.Providers[id].EffectiveRotation(c.KeyRotation.Val)
}

// RotatesFor reports whether the provider rotates its credential on a refusal.
//
// Both axes decide it, which is why the answer is computed in one place instead
// of being read off either one at the call site.
func (c *Config) RotatesFor(id string) bool {
	return c.Providers[id].Rotates(c.Credential.Val, c.KeyRotation.Val)
}

// UsesAccount reports whether the provider is reached with a credential.
func (c *Config) UsesAccount(id string) bool {
	return c.CredentialFor(id) == CredentialAccount
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

// HasKeys reports whether the section supplies at least one key.
//
// THREE PLACES, and all of them count: the key field, the variable named beside
// it, and the list under `settings.keys`. The last one is not a rarity — it is
// how a pool is declared when there are several keys — and a check that forgot
// it would refuse to start a perfectly valid deployment.
//
// The variable is read from the environment rather than trusted by name: a
// deployment that names a variable it never sets has no key, and that is exactly
// the case the caller is asking about.
func (p ProviderConfig) HasKeys() bool {
	if strings.TrimSpace(p.ProviderKey()) != "" {
		return true
	}
	return len(settingKeys(p.Settings)) > 0
}

// settingKeys extracts the key list from a section's settings.
//
// Both shapes are accepted because both arrive: a JSON file decodes the list as
// []any, while a programmatically built configuration (a test, the NixOS drift
// check) hands over []string. Handling one and ignoring the other would make the
// same configuration valid or invalid depending on where it came from.
func settingKeys(settings map[string]any) []string {
	if settings == nil {
		return nil
	}
	switch v := settings["keys"].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
