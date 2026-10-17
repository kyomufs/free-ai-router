package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeConfig drops the file into a temporary directory and returns the paths.
func writeConfig(t *testing.T, body string) Paths {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Paths{
		ConfigDir:  dir,
		StateDir:   dir,
		RuntimeDir: dir,
		ConfigFile: path,
		StateFile:  filepath.Join(dir, "state.json"),
		LogFile:    filepath.Join(dir, "app.log"),
	}
}

// TestDefaultsWhenFileAbsent — a missing file is not an error: the defaults
// work on their own.
func TestDefaultsWhenFileAbsent(t *testing.T) {
	dir := t.TempDir()
	paths := Paths{ConfigFile: filepath.Join(dir, "no-file.json"), StateFile: filepath.Join(dir, "s.json")}

	c, err := LoadFrom(paths)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if c.Mode.Val != DefaultMode {
		t.Fatalf("Mode = %q, want %q", c.Mode.Val, DefaultMode)
	}
	if c.Mode.From != SourceDefault {
		t.Fatalf("Mode source = %q, want %q", c.Mode.From, SourceDefault)
	}
	if c.Listen.Val != DefaultListen {
		t.Fatalf("Listen = %q, want %q", c.Listen.Val, DefaultListen)
	}
}

// TestFileOverridesDefaults — the file is layered over the defaults, and the
// value's source reflects that.
func TestFileOverridesDefaults(t *testing.T) {
	paths := writeConfig(t, `{
		"mode": "server",
		"listen": "0.0.0.0:9000",
		"token": "secret",
		"transport": "direct",
		"first_event_timeout": "45s"
	}`)

	c, err := LoadFrom(paths)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if c.Mode.Val != ModeServer || c.Mode.From != SourceFile {
		t.Fatalf("Mode = %q from %q, want %q from %q", c.Mode.Val, c.Mode.From, ModeServer, SourceFile)
	}
	if c.Listen.Val != "0.0.0.0:9000" {
		t.Fatalf("Listen = %q", c.Listen.Val)
	}
	if c.FirstEventTimeout.Val != 45*time.Second {
		t.Fatalf("FirstEventTimeout = %v, want 45s", c.FirstEventTimeout.Val)
	}
	// Fields absent from the file stay at their defaults.
	if c.IdleTimeout.Val != DefaultIdleTimeout || c.IdleTimeout.From != SourceDefault {
		t.Fatalf("IdleTimeout = %v from %q, want the default", c.IdleTimeout.Val, c.IdleTimeout.From)
	}
}

// TestEnvOverridesFile — the environment overrides the file.
func TestEnvOverridesFile(t *testing.T) {
	paths := writeConfig(t, `{"mode": "local", "listen": "127.0.0.1:8000"}`)

	t.Setenv(EnvListen, "127.0.0.1:9999")

	c, err := LoadFrom(paths)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if c.Listen.Val != "127.0.0.1:9999" {
		t.Fatalf("Listen = %q, want the env value", c.Listen.Val)
	}
	if c.Listen.From != SourceEnv {
		t.Fatalf("Listen source = %q, want %q", c.Listen.From, SourceEnv)
	}
	// A value from the file, untouched by the environment, keeps its source.
	if c.Mode.From != SourceFile {
		t.Fatalf("Mode source = %q, want %q", c.Mode.From, SourceFile)
	}
}

// TestFlagsOverrideEverything — flags are older than everything else.
func TestFlagsOverrideEverything(t *testing.T) {
	paths := writeConfig(t, `{"listen": "127.0.0.1:8000"}`)
	t.Setenv(EnvListen, "127.0.0.1:9999")

	c, err := LoadFrom(paths)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if err := c.ApplyFlags(FlagValues{Listen: "127.0.0.1:7777"}); err != nil {
		t.Fatal(err)
	}

	if c.Listen.Val != "127.0.0.1:7777" {
		t.Fatalf("Listen = %q, want the flag value", c.Listen.Val)
	}
	if c.Listen.From != SourceFlag {
		t.Fatalf("Listen source = %q, want %q", c.Listen.From, SourceFlag)
	}
}

// TestEmptyFlagsDoNotOverride — an empty flag means "not given", not "set it
// empty". Otherwise a run without flags would zero out the configuration.
func TestEmptyFlagsDoNotOverride(t *testing.T) {
	paths := writeConfig(t, `{"listen": "127.0.0.1:8000"}`)
	c, err := LoadFrom(paths)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyFlags(FlagValues{}); err != nil {
		t.Fatal(err)
	}

	if c.Listen.Val != "127.0.0.1:8000" || c.Listen.From != SourceFile {
		t.Fatalf("an empty flag changed the value: %q from %q", c.Listen.Val, c.Listen.From)
	}
}

// TestServerModeRequiresToken — an invariant between axes. Without it the
// gateway would become an open relay.
func TestServerModeRequiresToken(t *testing.T) {
	paths := writeConfig(t, `{"mode": "server", "listen": "0.0.0.0:8787"}`)
	if _, err := LoadFrom(paths); err == nil {
		t.Fatal("server mode without a token must fail")
	}

	paths = writeConfig(t, `{"mode": "server", "listen": "0.0.0.0:8787", "token": "x"}`)
	if _, err := LoadFrom(paths); err != nil {
		t.Fatalf("server mode with a token must load: %v", err)
	}
}

// TestLocalModeNeedsNoToken — in local the check is not in force at all.
func TestLocalModeNeedsNoToken(t *testing.T) {
	paths := writeConfig(t, `{"mode": "local"}`)
	c, err := LoadFrom(paths)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if c.IsServer() {
		t.Fatal("local mode must not be treated as server")
	}
}

// TestInvalidValuesAreRejected — a typo must surface rather than lead to a
// silent fallback to the defaults.
func TestInvalidValuesAreRejected(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"broken JSON", `{"mode":`},
		{"unknown mode", `{"mode": "cloud"}`},
		{"empty listen", `{"listen": "   "}`},
		{"empty transport", `{"transport": ""}`},
		{"invalid duration", `{"first_event_timeout": "fast"}`},
		{"zero duration", `{"idle_timeout": "0s"}`},
		{"unknown credential", `{"credential": "key"}`},
		{"unknown rotation", `{"key_rotation": "yes"}`},
		{"unknown family", `{"family": "ipv7"}`},
		{"unknown provider credential", `{"providers": {"p": {"type": "stub", "credential": "pool"}}}`},
		{"unknown provider rotation", `{"providers": {"p": {"type": "stub", "key_rotation": "true"}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			paths := writeConfig(t, tc.body)
			if _, err := LoadFrom(paths); err == nil {
				t.Fatalf("config %s was accepted, want an error", tc.body)
			}
		})
	}
}

// TestProviderKeyEnvPreferredOverNothing — when there is no key in the file,
// the variable named alongside is taken.
func TestProviderKeyEnvPreferredOverNothing(t *testing.T) {
	t.Setenv("TEST_PROVIDER_KEY", "from-env")

	p := ProviderConfig{APIKeyEnv: "TEST_PROVIDER_KEY"}
	if got := p.ProviderKey(); got != "from-env" {
		t.Fatalf("ProviderKey() = %q, want the environment value", got)
	}

	// A key in the file outweighs the variable: an operator who wrote it
	// explicitly expects exactly that one.
	p.APIKey = "from-file"
	if got := p.ProviderKey(); got != "from-file" {
		t.Fatalf("ProviderKey() = %q, want the file value", got)
	}
}

// TestProviderEnabledByDefault — an added section works until it is explicitly
// switched off.
func TestProviderEnabledByDefault(t *testing.T) {
	var p ProviderConfig
	if !p.IsEnabled() {
		t.Fatal("a provider without the enabled field must be enabled")
	}
	off := false
	p.Enabled = &off
	if p.IsEnabled() {
		t.Fatal("an explicitly disabled provider must be disabled")
	}
}

// TestProvidersParsedFromFile — the providers section is read together with the
// models and settings.
func TestProvidersParsedFromFile(t *testing.T) {
	paths := writeConfig(t, `{
		"providers": {
			"zenmux": {
				"type": "openai",
				"base_url": "https://zenmux.ai/api/v1",
				"api_key_env": "ZENMUX_API_KEY",
				"settings": {"chat_path": "/chat/completions"}
			},
			"disabled-one": {
				"type": "openai",
				"enabled": false
			}
		}
	}`)

	c, err := LoadFrom(paths)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if len(c.Providers) != 2 {
		t.Fatalf("Providers = %d entries, want 2", len(c.Providers))
	}

	zm, ok := c.Providers["zenmux"]
	if !ok {
		t.Fatal("the zenmux provider is missing")
	}
	if zm.Type != "openai" {
		t.Fatalf("Type = %q", zm.Type)
	}
	if zm.BaseURL != "https://zenmux.ai/api/v1" {
		t.Fatalf("BaseURL = %q", zm.BaseURL)
	}
	if !zm.IsEnabled() {
		t.Fatal("zenmux must be enabled by default")
	}
	// The model catalog is not declared in the configuration: it is taken from
	// the service. So what is checked is the setting, not the list of models.
	if zm.Settings["chat_path"] != "/chat/completions" {
		t.Fatalf("Settings = %+v", zm.Settings)
	}

	if c.Providers["disabled-one"].IsEnabled() {
		t.Fatal("the disabled provider must report disabled")
	}
}

// TestStateOverrideFromEnv — a one-off run must not touch the working state.
func TestStateOverrideFromEnv(t *testing.T) {
	paths := writeConfig(t, `{}`)
	override := filepath.Join(t.TempDir(), "one-off.json")
	t.Setenv(EnvState, override)

	c, err := LoadWith(paths, EnvPathOverrides())
	if err != nil {
		t.Fatal(err)
	}
	if c.Paths.StateFile != override {
		t.Fatalf("StateFile = %q, want %q", c.Paths.StateFile, override)
	}
}

// TestStateOverrideMovesTheLogToo — a redirected state must not append to the
// real journal.
//
// The log lives in the state directory, so leaving it behind makes a test run
// write into the journal of the daemon that is actually running — and the
// journal stops describing that daemon.
func TestStateOverrideMovesTheLogToo(t *testing.T) {
	base := Paths{
		ConfigDir:  "/base/cfg",
		StateDir:   "/base/state",
		ConfigFile: "/base/cfg/config.json",
		StateFile:  "/base/state/state.json",
		LogFile:    "/base/state/app.log",
	}
	got := ResolvePaths(base, PathOverrides{StateFile: "/tmp/probe/state.json"})

	if got.StateFile != "/tmp/probe/state.json" {
		t.Fatalf("StateFile = %q", got.StateFile)
	}
	if got.StateDir != "/tmp/probe" {
		t.Fatalf("StateDir = %q, want the file's directory", got.StateDir)
	}
	if got.LogFile != "/tmp/probe/app.log" {
		t.Fatalf("LogFile = %q, want it to follow the state", got.LogFile)
	}
	// An override that was not given leaves the base alone.
	if got.ConfigFile != base.ConfigFile || got.ConfigDir != base.ConfigDir {
		t.Fatalf("the absent override moved the config: %+v", got)
	}
}

// TestConfigOverrideReadsAnotherFile — the override is resolved before the file
// is opened, which is why it cannot be layered on after loading.
func TestConfigOverrideReadsAnotherFile(t *testing.T) {
	real := writeConfig(t, `{"listen": "127.0.0.1:8000"}`)

	probe := filepath.Join(t.TempDir(), "probe.json")
	if err := os.WriteFile(probe, []byte(`{"listen": "127.0.0.1:9123"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := LoadWith(real, PathOverrides{ConfigFile: probe})
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen.Val != "127.0.0.1:9123" {
		t.Fatalf("Listen = %q, want the probe file's value", c.Listen.Val)
	}
	if c.Paths.ConfigFile != probe {
		t.Fatalf("ConfigFile = %q, want the probe path", c.Paths.ConfigFile)
	}
}

// TestLoadFromIgnoresPathEnvironment — LoadFrom takes paths that are already
// resolved and must not read the path variables itself.
//
// A library caller that resolved its own paths would otherwise be silently
// redirected by the ambient environment, and the same call would read different
// files in two contexts.
func TestLoadFromIgnoresPathEnvironment(t *testing.T) {
	paths := writeConfig(t, `{}`)
	t.Setenv(EnvState, "/tmp/elsewhere/state.json")
	t.Setenv(EnvConfig, "/tmp/elsewhere/config.json")

	c, err := LoadFrom(paths)
	if err != nil {
		t.Fatal(err)
	}
	if c.Paths.StateFile != paths.StateFile || c.Paths.ConfigFile != paths.ConfigFile {
		t.Fatalf("LoadFrom followed the environment: %+v", c.Paths)
	}
}

// TestNewAxesDefaults — the mechanism and addressing axes start where the free
// tier is reachable without any configuration at all.
func TestNewAxesDefaults(t *testing.T) {
	paths := writeConfig(t, `{}`)
	c, err := LoadFrom(paths)
	if err != nil {
		t.Fatal(err)
	}
	if c.Credential.Val != CredentialAnonymous || c.Credential.From != SourceDefault {
		t.Fatalf("Credential = %q from %q, want %q from the default", c.Credential.Val, c.Credential.From, CredentialAnonymous)
	}
	if c.KeyRotation.Val != RotationOn {
		t.Fatalf("KeyRotation = %q, want %q", c.KeyRotation.Val, RotationOn)
	}
	if c.Family.Val != FamilyAuto {
		t.Fatalf("Family = %q, want %q", c.Family.Val, FamilyAuto)
	}
}

// TestNewAxesPrecedence — flags beat the environment, the environment beats the
// file, and the file beats the defaults.
func TestNewAxesPrecedence(t *testing.T) {
	paths := writeConfig(t, `{
		"credential": "account",
		"key_rotation": "off",
		"family": "v4"
	}`)

	c, err := LoadFrom(paths)
	if err != nil {
		t.Fatal(err)
	}
	if c.Credential.Val != CredentialAccount || c.Credential.From != SourceFile {
		t.Fatalf("Credential = %q from %q", c.Credential.Val, c.Credential.From)
	}
	if c.KeyRotation.Val != RotationOff {
		t.Fatalf("KeyRotation = %q", c.KeyRotation.Val)
	}
	if c.Family.Val != FamilyV4 {
		t.Fatalf("Family = %q", c.Family.Val)
	}

	t.Setenv(EnvCredential, CredentialAnonymous)
	t.Setenv(EnvFamily, FamilyV6)
	c, err = LoadFrom(paths)
	if err != nil {
		t.Fatal(err)
	}
	if c.Credential.Val != CredentialAnonymous || c.Credential.From != SourceEnv {
		t.Fatalf("Credential = %q from %q, want the environment to win over the file", c.Credential.Val, c.Credential.From)
	}
	if c.Family.Val != FamilyV6 || c.Family.From != SourceEnv {
		t.Fatalf("Family = %q from %q", c.Family.Val, c.Family.From)
	}
	// Untouched by the environment, the file value keeps its source.
	if c.KeyRotation.Val != RotationOff || c.KeyRotation.From != SourceFile {
		t.Fatalf("KeyRotation = %q from %q", c.KeyRotation.Val, c.KeyRotation.From)
	}

	if err := c.ApplyFlags(FlagValues{Credential: CredentialAccount, Family: FamilyV4}); err != nil {
		t.Fatal(err)
	}
	if c.Credential.Val != CredentialAccount || c.Credential.From != SourceFlag {
		t.Fatalf("Credential = %q from %q, want the flag to win", c.Credential.Val, c.Credential.From)
	}
	if c.Family.Val != FamilyV4 || c.Family.From != SourceFlag {
		t.Fatalf("Family = %q from %q", c.Family.Val, c.Family.From)
	}
}

// TestProviderOverridesTheGlobalAxes — a provider can be worked around
// differently from the rest of the process.
func TestProviderOverridesTheGlobalAxes(t *testing.T) {
	// "picky" carries a key because it asks for the account credential, and the
	// two cannot be separated: validation refuses the pair "account without a
	// key" outright. This test is about the inheritance of the axes, so the key
	// is there to let the configuration load at all.
	paths := writeConfig(t, `{
		"credential": "anonymous",
		"key_rotation": "on",
		"providers": {
			"picky": {"type": "stub", "credential": "account", "key_rotation": "off", "api_key": "sk-picky"},
			"plain": {"type": "stub"}
		}
	}`)

	c, err := LoadFrom(paths)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.CredentialFor("picky"); got != CredentialAccount {
		t.Fatalf("picky credential = %q, want its own value", got)
	}
	if got := c.RotationFor("picky"); got != RotationOff {
		t.Fatalf("picky rotation = %q, want its own value", got)
	}
	if got := c.CredentialFor("plain"); got != CredentialAnonymous {
		t.Fatalf("plain credential = %q, want the global value", got)
	}
	if got := c.RotationFor("plain"); got != RotationOn {
		t.Fatalf("plain rotation = %q, want the global value", got)
	}
	// An unknown provider falls back to the global axes rather than failing.
	if got := c.CredentialFor("absent"); got != CredentialAnonymous {
		t.Fatalf("absent credential = %q, want the global value", got)
	}
}

// TestRotationIsInertWithoutACredential — with nothing sent there is nothing to
// rotate, and the monitor must not claim a mechanism that cannot run.
func TestRotationIsInertWithoutACredential(t *testing.T) {
	cases := []struct {
		credential string
		rotation   string
		want       bool
	}{
		{CredentialAnonymous, RotationOn, false},
		{CredentialAnonymous, RotationOff, false},
		{CredentialAccount, RotationOn, true},
		{CredentialAccount, RotationOff, false},
	}
	for _, tc := range cases {
		t.Run(tc.credential+"+"+tc.rotation, func(t *testing.T) {
			paths := writeConfig(t, `{"credential": "`+tc.credential+`", "key_rotation": "`+tc.rotation+`"}`)
			c, err := LoadFrom(paths)
			if err != nil {
				t.Fatal(err)
			}
			if got := c.RotatesFor("opencode"); got != tc.want {
				t.Fatalf("RotatesFor = %t, want %t", got, tc.want)
			}
			if got := c.UsesAccount("opencode"); got != (tc.credential == CredentialAccount) {
				t.Fatalf("UsesAccount = %t for credential %q", got, tc.credential)
			}
		})
	}
}

// TestDefaultPathsAreXDG — the paths follow XDG, not platform directories.
// Otherwise the state would end up where the rest of the system does not look
// for it.
func TestDefaultPathsAreXDG(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "st"))

	paths, err := DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "cfg", AppName, "config.json"); paths.ConfigFile != want {
		t.Fatalf("ConfigFile = %q, want %q", paths.ConfigFile, want)
	}
	if want := filepath.Join(dir, "st", AppName, "state.json"); paths.StateFile != want {
		t.Fatalf("StateFile = %q, want %q", paths.StateFile, want)
	}
}

// TestEmptyFileIsValid — an empty file is equivalent to a missing one.
func TestEmptyFileIsValid(t *testing.T) {
	paths := writeConfig(t, "   \n")
	c, err := LoadFrom(paths)
	if err != nil {
		t.Fatalf("an empty file must be valid: %v", err)
	}
	if c.Mode.Val != DefaultMode {
		t.Fatalf("Mode = %q, want the default", c.Mode.Val)
	}
}

// TestAnUnknownKeyIsRefused.
//
// A key the daemon does not read and a key that works are indistinguishable from
// the outside: the setting is written, the daemon starts, and the value never
// takes effect. That is not hypothetical — the timeout options in the NixOS
// module were written as "firstEventTimeout" while the daemon read
// "first_event_timeout", and nothing anywhere reported it.
func TestAnUnknownKeyIsRefused(t *testing.T) {
	// The exact shape the module used to render.
	paths := writeConfig(t, `{"mode":"local","firstEventTimeout":"45s"}`)

	_, err := LoadFrom(paths)
	if err == nil {
		t.Fatal("a file with an unknown key was accepted")
	}
	if !strings.Contains(err.Error(), "firstEventTimeout") {
		t.Errorf("the error does not name the key: %v", err)
	}
}

// TestEveryKnownKeyIsAccepted is the other half: the refusal must not reach the
// keys the daemon does read.
//
// Without this, tightening the check into a wholesale rejection of the file
// shape would pass the test above and break every working configuration.
func TestEveryKnownKeyIsAccepted(t *testing.T) {
	paths := writeConfig(t, `{
		"mode": "local",
		"listen": "127.0.0.1:9000",
		"token": "secret",
		"transport": "direct",
		"transport_settings": {"family": "v4"},
		"upstream": "https://example.invalid",
		"credential": "account",
		"key_rotation": "off",
		"family": "v6",
		"first_event_timeout": "45s",
		"idle_timeout": "90s",
		"reasoning_idle_timeout": "600s",
		"providers": {
			"opencode": {
				"type": "opencode",
				"enabled": true,
				"base_url": "https://example.invalid",
				"api_key": "k",
				"api_key_env": "K",
				"credential": "anonymous",
				"key_rotation": "on",
				"settings": {"anything": "goes"}
			}
		}
	}`)

	c, err := LoadFrom(paths)
	if err != nil {
		t.Fatalf("a valid file was refused: %v", err)
	}
	if c.FirstEventTimeout.Val != 45*time.Second {
		t.Errorf("FirstEventTimeout = %v, want 45s", c.FirstEventTimeout.Val)
	}
	if c.Family.Val != FamilyV6 {
		t.Errorf("Family = %q, want %q", c.Family.Val, FamilyV6)
	}
	if c.Credential.Val != CredentialAccount {
		t.Errorf("Credential = %q, want %q", c.Credential.Val, CredentialAccount)
	}
	if c.KeyRotation.Val != RotationOff {
		t.Errorf("KeyRotation = %q, want %q", c.KeyRotation.Val, RotationOff)
	}
}

// TestProviderSettingsStayOpen.
//
// The provider's settings bag is not interpreted by the daemon: a provider type
// owns its keys, and refusing them here would mean the daemon had to be rebuilt
// for every new setting a provider grows.
func TestProviderSettingsStayOpen(t *testing.T) {
	paths := writeConfig(t, `{"providers":{"opencode":{"type":"opencode","settings":{"unknown_to_the_daemon":1}}}}`)

	if _, err := LoadFrom(paths); err != nil {
		t.Fatalf("an unknown key inside settings was refused: %v", err)
	}
}

// TestUncheckedLoadReadsWhatCheckedLoadRefuses.
//
// The dividing line between the two loaders is the whole point: one answers
// "what would run", the other answers "what does the file say". A configuration
// that cannot start is still a configuration, and the commands that edit it have
// to be able to read it — including the one that can fix it.
func TestUncheckedLoadReadsWhatCheckedLoadRefuses(t *testing.T) {
	// Server mode with no token: refused at startup, by design.
	paths := writeConfig(t, `{"mode":"server","listen":"0.0.0.0:8787","credential":"account"}`)

	if _, err := LoadFrom(paths); err == nil {
		t.Fatal("the checked loader accepted a configuration that cannot start")
	}

	c, err := LoadUnchecked(paths)
	if err != nil {
		t.Fatalf("the unchecked loader refused it: %v", err)
	}
	if c.Mode.Val != ModeServer {
		t.Errorf("Mode = %q, want %q", c.Mode.Val, ModeServer)
	}
	if c.Credential.Val != CredentialAccount {
		t.Errorf("Credential = %q, want %q", c.Credential.Val, CredentialAccount)
	}
	if c.Paths.ConfigFile != paths.ConfigFile {
		t.Errorf("ConfigFile = %q, want %q", c.Paths.ConfigFile, paths.ConfigFile)
	}
}

// TestUncheckedLoadStillRefusesABrokenFile.
//
// Relaxing the check must not relax the parsing: a file that cannot be read at
// all is not an incomplete configuration, it is an unreadable one, and saying so
// is the difference between a typo being reported and a typo being ignored.
func TestUncheckedLoadStillRefusesABrokenFile(t *testing.T) {
	paths := writeConfig(t, `{"mode":`)

	if _, err := LoadUnchecked(paths); err == nil {
		t.Fatal("an unparseable file was accepted")
	}
}

// TestUncheckedLoadStillAppliesTheEnvironment.
//
// The two loaders differ in one thing only. If skipping the check also skipped
// the environment, the file commands would report a value the daemon is not
// going to use — which is exactly the kind of quiet disagreement they exist to
// prevent.
func TestUncheckedLoadStillAppliesTheEnvironment(t *testing.T) {
	paths := writeConfig(t, `{"credential":"anonymous"}`)
	t.Setenv(EnvCredential, CredentialAccount)

	c, err := LoadUnchecked(paths)
	if err != nil {
		t.Fatal(err)
	}
	if c.Credential.Val != CredentialAccount {
		t.Errorf("Credential = %q, want the value from the environment", c.Credential.Val)
	}
	if c.Credential.From != SourceEnv {
		t.Errorf("Credential source = %q, want %q", c.Credential.From, SourceEnv)
	}
}

// TestAccountWithoutAKeyIsRefused.
//
// A credential asked for must exist. Under "account" the daemon presents a key
// on every request, so a section without one describes a deployment that cannot
// work — and it used to start anyway and reach the service anonymously, which is
// the worst of both answers: the file says one thing, the wire another, and
// nothing tells the operator which one won.
//
// Refused at LOAD rather than at the first request: a deployment that cannot
// serve should say so before it accepts a client.
func TestAccountWithoutAKeyIsRefused(t *testing.T) {
	paths := writeConfig(t, `{
		"credential": "account",
		"providers": {"opencode": {"type": "opencode"}}
	}`)

	_, err := LoadFrom(paths)
	if err == nil {
		t.Fatal("a keyless account credential was accepted")
	}
	// The message has to name the fix, not only the fault: the operator is
	// holding a broken configuration and needs the way out of it.
	for _, want := range []string{"account", "requires a key", "api_key"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// TestAccountIsSatisfiedByAnyOfTheThreeSources.
//
// There are three places a key can come from, and all of them count: the field,
// the variable named beside it, and the list under settings. A check that knew
// only the first would refuse a perfectly valid pool.
func TestAccountIsSatisfiedByAnyOfTheThreeSources(t *testing.T) {
	cases := []struct {
		name    string
		section string
		env     bool
	}{
		{"the key field", `"api_key": "sk-direct"`, false},
		{"the named variable", `"api_key_env": "TEST_ROUTER_KEY"`, true},
		{"the settings list", `"settings": {"keys": ["sk-one", "sk-two"]}`, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.env {
				t.Setenv("TEST_ROUTER_KEY", "sk-from-env")
			}
			paths := writeConfig(t, `{
				"credential": "account",
				"providers": {"opencode": {"type": "opencode", `+c.section+`}}
			}`)

			if _, err := LoadFrom(paths); err != nil {
				t.Fatalf("a key supplied through %s was not accepted: %v", c.name, err)
			}
		})
	}
}

// TestANamedVariableThatIsNotSetDoesNotCount.
//
// A key named but never exported is not a key. Trusting the name alone would let
// the daemon start and then reach the service without a credential, which is the
// failure the whole check exists to prevent — and it is the likeliest one in a
// unit file, where the variable arrives from an EnvironmentFile that may not
// have been written yet.
func TestANamedVariableThatIsNotSetDoesNotCount(t *testing.T) {
	paths := writeConfig(t, `{
		"credential": "account",
		"providers": {"opencode": {"type": "opencode", "api_key_env": "NOT_SET_ANYWHERE"}}
	}`)

	if _, err := LoadFrom(paths); err == nil {
		t.Fatal("a keyless provider naming an unset variable was accepted")
	}
}

// TestAnonymousNeedsNoKeyAtAll.
//
// The mode the free tier runs in. A check that demanded a key here would make the
// gateway unusable in its ordinary deployment.
func TestAnonymousNeedsNoKeyAtAll(t *testing.T) {
	paths := writeConfig(t, `{
		"credential": "anonymous",
		"providers": {"opencode": {"type": "opencode"}}
	}`)

	if _, err := LoadFrom(paths); err != nil {
		t.Fatalf("the anonymous credential was refused for want of a key: %v", err)
	}
}

// TestThePerProviderCredentialIsWhatIsChecked.
//
// The axis has a per-provider override, and the check has to follow it: a global
// "anonymous" with one keyed section is the normal shape, and a global "account"
// with one anonymous section is how a single service is reached without an
// account. Reading the global value alone would refuse the first and accept the
// second while it is broken.
func TestThePerProviderCredentialIsWhatIsChecked(t *testing.T) {
	t.Run("a keyed section under a global anonymous is refused without a key", func(t *testing.T) {
		paths := writeConfig(t, `{
			"credential": "anonymous",
			"providers": {"picky": {"type": "opencode", "credential": "account"}}
		}`)
		if _, err := LoadFrom(paths); err == nil {
			t.Fatal("a section asking for account without a key was accepted")
		}
	})

	t.Run("an anonymous section under a global account needs no key", func(t *testing.T) {
		paths := writeConfig(t, `{
			"credential": "account",
			"providers": {
				"keyed":  {"type": "opencode", "api_key": "sk-x"},
				"public": {"type": "opencode", "credential": "anonymous"}
			}
		}`)
		if _, err := LoadFrom(paths); err != nil {
			t.Fatalf("an explicitly anonymous section was refused: %v", err)
		}
	})
}

// TestADisabledSectionIsNotChecked.
//
// A parked provider is not going to serve, so its missing key is not a fault —
// and refusing it would make the section impossible to park and repair, since the
// way back is to fix it and switch it on. This is the same reasoning that keeps
// the file-editing commands off the validating loader.
func TestADisabledSectionIsNotChecked(t *testing.T) {
	paths := writeConfig(t, `{
		"credential": "account",
		"providers": {
			"live":    {"type": "opencode", "api_key": "sk-x"},
			"parked":  {"type": "opencode", "enabled": false}
		}
	}`)

	if _, err := LoadFrom(paths); err != nil {
		t.Fatalf("a disabled section without a key was refused: %v", err)
	}
}

// TestTheFileEditingLoaderDoesNotApplyTheKeyCheck.
//
// The commands that change one key at a time run on the UNCHECKED loader, and
// they have to: a configuration is allowed to be incomplete while it is being
// assembled. Refusing it there would seal it shut against the very command that
// can reopen it — set the credential, then add the key, and the first step would
// fail.
func TestTheFileEditingLoaderDoesNotApplyTheKeyCheck(t *testing.T) {
	paths := writeConfig(t, `{
		"credential": "account",
		"providers": {"opencode": {"type": "opencode"}}
	}`)

	if _, err := LoadUnchecked(paths); err != nil {
		t.Fatalf("the unchecked loader applied the key check and left the file unrepairable: %v", err)
	}
	// The validating loader, over the same file, must refuse: the two differ
	// exactly here.
	if _, err := LoadFrom(paths); err == nil {
		t.Fatal("the validating loader accepted the same file")
	}
}
