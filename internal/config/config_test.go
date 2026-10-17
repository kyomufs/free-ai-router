package config

import (
	"os"
	"path/filepath"
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
	c.ApplyFlags("", "127.0.0.1:7777", "", "", "")

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
	c.ApplyFlags("", "", "", "", "")

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

	c, err := LoadFrom(paths)
	if err != nil {
		t.Fatal(err)
	}
	if c.Paths.StateFile != override {
		t.Fatalf("StateFile = %q, want %q", c.Paths.StateFile, override)
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
