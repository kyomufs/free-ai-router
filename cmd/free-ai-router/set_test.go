package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"free-ai-router/internal/config"
	"free-ai-router/internal/transport"
)

// TestWriteConfigKeyKeepsTheRestOfTheFile.
//
// The file belongs to the operator, and the daemon knows only part of it. Reading
// it into the configuration type and rendering it back would discard everything
// the type does not know about — an unknown key, an ordering, a comment in the
// shape of an extra field.
func TestWriteConfigKeyKeepsTheRestOfTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{"mode":"local","providers":{"opencode":{"type":"opencode","settings":{"retries":3}}},"unknown_key":"kept"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := writeConfigKey(path, "credential", "account"); err != nil {
		t.Fatal(err)
	}

	var doc map[string]any
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the result is not valid JSON: %v", err)
	}

	if doc["credential"] != "account" {
		t.Errorf("credential = %v, want account", doc["credential"])
	}
	if doc["mode"] != "local" {
		t.Errorf("mode was lost: %v", doc["mode"])
	}
	if doc["unknown_key"] != "kept" {
		t.Errorf("an unknown key was dropped: %v", doc)
	}

	// The nested section has to survive in full: losing a provider while
	// flipping an unrelated axis would be the worst possible outcome of this
	// command.
	providers, ok := doc["providers"].(map[string]any)
	if !ok {
		t.Fatalf("the providers section was lost: %v", doc)
	}
	opencode, ok := providers["opencode"].(map[string]any)
	if !ok {
		t.Fatalf("a provider was lost: %v", providers)
	}
	settings, ok := opencode["settings"].(map[string]any)
	if !ok || settings["retries"] != float64(3) {
		t.Errorf("the provider's settings were lost: %v", opencode)
	}
}

// TestWriteConfigKeyCreatesWhatIsMissing.
//
// The first run on a fresh machine has no file and no directory, and that is the
// normal case: the built-in values work without a file.
func TestWriteConfigKeyCreatesWhatIsMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "deeper")
	path := filepath.Join(dir, "config.json")

	if err := writeConfigKey(path, "family", "v6"); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the file was not created: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["family"] != "v6" {
		t.Errorf("family = %v, want v6", doc["family"])
	}
}

// TestWriteConfigKeyRefusesToDestroyABrokenFile.
//
// A syntax error is no reason to lose the settings: they are not recoverable from
// anywhere else. The command reports and leaves the file alone.
func TestWriteConfigKeyRefusesToDestroyABrokenFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{"mode":"local",`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := writeConfigKey(path, "family", "v6"); err == nil {
		t.Fatal("a broken file was overwritten")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != body {
		t.Errorf("the file was modified:\n got %q\nwant %q", after, body)
	}
}

// TestWriteConfigKeyLeavesNoTemporaryFileBehind.
//
// The write goes through a temporary file, and one left in the configuration
// directory would look like a configuration to whatever reads that directory
// next.
func TestWriteConfigKeyLeavesNoTemporaryFileBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	if err := writeConfigKey(path, "mode", "server"); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "config.json" {
			t.Errorf("unexpected file left behind: %s", e.Name())
		}
	}
}

// TestSettableValuesComeFromTheConfiguration pins the single source.
//
// Written out by hand, these lists would agree with validation until one of them
// was edited — and then a subcommand would write a value the daemon refuses to
// start with.
func TestSettableValuesComeFromTheConfiguration(t *testing.T) {
	cases := []struct {
		command string
		key     string
		want    []string
	}{
		{"mode", "mode", config.ModeValues()},
		{"cred", "credential", config.CredentialValues()},
		{"rotate", "key_rotation", config.RotationValues()},
		{"family", "family", config.FamilyValues()},
	}
	if len(cases) != len(settableAxes()) {
		t.Fatalf("settableAxes has %d entries, the test covers %d", len(settableAxes()), len(cases))
	}
	for _, c := range cases {
		s, ok := findSettable(c.command)
		if !ok {
			t.Errorf("%s: not a subcommand", c.command)
			continue
		}
		if s.key != c.key {
			t.Errorf("%s: key is %q, want %q", c.command, s.key, c.key)
		}
		if strings.Join(s.values, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: values are %v, want %v", c.command, s.values, c.want)
		}
	}
}

// TestEverySettableAxisIsInTheDictionary.
//
// A subcommand that writes a key the dictionary does not describe would be a
// setting the NixOS module cannot check, which is how a module drifts from its
// binary.
func TestEverySettableAxisIsInTheDictionary(t *testing.T) {
	for _, s := range settableAxes() {
		if !axisExists(s.key) {
			t.Errorf("%s: %s is not in the axes dictionary", s.command, s.key)
		}
	}
}

// TestTheAxisNameIsAcceptedAsACommand.
//
// The axis has ONE name, and it is the one the flag, the environment variable
// and the configuration key all carry. The short subcommand is a convenience
// beside it, not a replacement: an operator who has just read "--credential" in
// the axes table types "credential" a second later, and a refusal there teaches
// nothing except that the tool disagrees with itself.
//
// The check is written against settableAxes rather than a list of literals, so
// a new axis is covered the moment it is declared.
func TestTheAxisNameIsAcceptedAsACommand(t *testing.T) {
	for _, s := range settableAxes() {
		found, ok := findSettable(s.key)
		if !ok {
			t.Errorf("%s: the axis name is not accepted as a subcommand", s.key)
			continue
		}
		if found.command != s.command {
			t.Errorf("%s resolved to the subcommand %q, want %q", s.key, found.command, s.command)
		}
		// Both spellings must reach the same axis, or one of them writes a
		// different key than it reports.
		if found.key != s.key {
			t.Errorf("%s writes %q, want %q", s.key, found.key, s.key)
		}
	}
}

// TestFlagsMoveThePathsBeforeTheFileIsRead is requirement 2 as a test.
//
// The override decides WHICH file is read, so it cannot be layered on afterwards:
// a value applied after the read would have come from the wrong file.
func TestFlagsMoveThePathsBeforeTheFileIsRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "probe.json")
	statePath := filepath.Join(dir, "probe-state.json")
	if err := os.WriteFile(path, []byte(`{"mode":"local","credential":"account"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	f, err := parseFlags([]string{"--config", path, "--state", statePath, "--family", "v6"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfigFor(f)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Paths.ConfigFile != path {
		t.Errorf("read %q, want %q", cfg.Paths.ConfigFile, path)
	}
	if cfg.Paths.StateFile != statePath {
		t.Errorf("state file is %q, want %q", cfg.Paths.StateFile, statePath)
	}
	// The journal follows the state file's directory: taken from the flag, not
	// from the default root, or the two would end up in different places.
	if cfg.Paths.LogFile != filepath.Join(dir, "app.log") {
		t.Errorf("log file is %q", cfg.Paths.LogFile)
	}

	// The file's values are in force, and the flag sits on top of them.
	if cfg.Credential.Val != config.CredentialAccount {
		t.Errorf("Credential = %q, want the value from the file", cfg.Credential.Val)
	}
	if cfg.Family.Val != config.FamilyV6 {
		t.Errorf("Family = %q, want v6 from the flag", cfg.Family.Val)
	}
	if cfg.Family.From != config.SourceFlag {
		t.Errorf("Family source = %q, want %q", cfg.Family.From, config.SourceFlag)
	}
}

// TestAFlagIsRefusedWhenItContradictsTheFile.
//
// The file is checked when it is read, and the flags afterwards. Without the
// second pass, "--mode cloud" would be accepted and travel on until something
// tripped over it.
func TestAFlagIsRefusedWhenItContradictsTheFile(t *testing.T) {
	f, err := parseFlags([]string{"--mode", "cloud"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfigFor(f); err == nil {
		t.Fatal("an invalid value from a flag was accepted")
	}
}

// TestTheFamilyAxisReachesTheTransport.
//
// The transport has pinned the address family all along; the axis was simply
// never reaching it, and the setting did nothing.
func TestTheFamilyAxisReachesTheTransport(t *testing.T) {
	cfg := config.Default()
	cfg.Family.Val = config.FamilyV6
	// Two sources for one value: the axis wins, and the bag keeps the rest of
	// its keys.
	cfg.TransportConfig = map[string]any{"family": "v4", "timeout_ms": 5000}

	settings := transportSettings(cfg)
	if settings["family"] != config.FamilyV6 {
		t.Errorf("family = %v, want %q", settings["family"], config.FamilyV6)
	}
	if settings["timeout_ms"] != 5000 {
		t.Errorf("the transport's own keys were dropped: %v", settings)
	}

	// And it survives the trip into the transport, which is the point.
	tr, err := transport.NewRegistryWithDefaults().Build("direct", settings)
	if err != nil {
		t.Fatalf("the transport refused the settings: %v", err)
	}
	if tr.ID() != "direct" {
		t.Errorf("transport is %q, want direct", tr.ID())
	}
}

// TestTransportSettingsDoesNotAliasTheConfiguration.
//
// The settings map is handed to a transport, which may keep it. Folding the axis
// into the configuration's own map would let one transport's edit reach the
// configuration.
func TestTransportSettingsDoesNotAliasTheConfiguration(t *testing.T) {
	cfg := config.Default()
	cfg.TransportConfig = map[string]any{"timeout_ms": 5000}

	settings := transportSettings(cfg)
	settings["family"] = "v6"

	if cfg.TransportConfig["family"] != nil {
		t.Errorf("the configuration was modified: %v", cfg.TransportConfig)
	}
}
