package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"free-ai-router/internal/config"
)

// THE SECOND WAY TO SET AN AXIS.
//
// Flags and environment variables are ephemeral: they change one run and write
// nothing. This is the other half — a subcommand changes ONE field of the
// configuration and keeps it, so the value survives a restart.
//
// Both halves are needed, and they must not be mixed. A flag is for trying a
// value out; a subcommand is for adopting it. Doing the second through the first
// is how a deployment ends up depending on a shell alias.
//
// ON NIXOS THE FILE IS NOT THE OPERATOR'S. The module renders it in full and
// rewrites it on every activation, so a value set here is lost at the next
// switch — by design, and the reason flags exist at all. The command says so
// rather than letting the loss be discovered later.

// settable — an axis a subcommand can write.
type settable struct {
	// command — the subcommand name.
	command string
	// key — the key in the configuration file.
	key string
	// values — the allowed set, the same one validation ranges over.
	values []string
}

// settableAxes returns the writable axes.
//
// A function rather than a slice: the value lists must be read every time, so
// that this command and the validation can never disagree.
func settableAxes() []settable {
	return []settable{
		{"mode", "mode", config.ModeValues()},
		{"cred", "credential", config.CredentialValues()},
		{"rotate", "key_rotation", config.RotationValues()},
		{"family", "family", config.FamilyValues()},
	}
}

// findSettable returns the writable axis for a subcommand name.
func findSettable(command string) (settable, bool) {
	for _, s := range settableAxes() {
		if s.command == command {
			return s, true
		}
	}
	return settable{}, false
}

// cmdSet reports or changes one axis.
//
// Without a value it reports what is in force and where that came from, which is
// the question asked while debugging a merge. With a value it writes the file.
func cmdSet(s settable, args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	if f == nil {
		return nil
	}

	// Loaded WITHOUT the check that the daemon could start with it: this command
	// edits the file, and a file it had just made unstartable has to stay
	// readable — otherwise "mode server" before the token exists would seal the
	// configuration shut against the one command that can reopen it.
	cfg, err := loadConfigUnchecked(f)
	if err != nil {
		return err
	}

	want := ""
	if len(f.positional) > 0 {
		want = f.positional[0]
	}

	if want == "" {
		return reportAxis(s, cfg, f.json)
	}

	if !allowed(s.values, want) {
		return fmt.Errorf("invalid %s %q (want %s)", s.key, want, strings.Join(s.values, "|"))
	}

	// Writing a value the daemon would refuse is reported, not blocked: the
	// setting can be completed by the time it starts — a token arrives through
	// the environment, for instance — and refusing here would leave the operator
	// with no way to make the change at all.
	warn := ""
	if s.key == "mode" && want == config.ModeServer && strings.TrimSpace(cfg.Token.Val) == "" {
		warn = "server mode requires a token: set it in the file or pass it in the environment"
	}

	if err := writeConfigKey(cfg.Paths.ConfigFile, s.key, want); err != nil {
		return err
	}

	if f.json {
		return printJSON(map[string]any{
			"axis":   s.key,
			"value":  want,
			"file":   cfg.Paths.ConfigFile,
			"source": string(config.SourceFile),
			"warn":   warn,
		})
	}

	fmt.Printf("%s: %s\n", s.key, want)
	fmt.Printf("written to %s\n", cfg.Paths.ConfigFile)
	if warn != "" {
		fmt.Fprintf(os.Stderr, "warning: %s\n", warn)
	}
	fmt.Println("a running daemon keeps its configuration until it is restarted")
	return nil
}

// reportAxis prints the value in force and its source.
func reportAxis(s settable, cfg *config.Config, asJSON bool) error {
	cur := axisValue(cfg, s.key)
	if asJSON {
		return printJSON(map[string]any{
			"axis":   s.key,
			"value":  cur.Val,
			"source": string(cur.From),
			"values": s.values,
			"file":   cfg.Paths.ConfigFile,
		})
	}
	fmt.Printf("%s: %s (%s)\n", s.key, cur.Val, cur.From)
	fmt.Printf("allowed: %s\n", strings.Join(s.values, " | "))
	return nil
}

// axisValue returns the value in force for a settable key.
func axisValue(cfg *config.Config, key string) config.Value[string] {
	switch key {
	case "mode":
		return cfg.Mode
	case "credential":
		return cfg.Credential
	case "key_rotation":
		return cfg.KeyRotation
	case "family":
		return cfg.Family
	}
	// Unreachable: axisValue is only called with a key from settableAxes.
	return config.Value[string]{}
}

// allowed reports whether the value is one of the permitted ones.
func allowed(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// writeConfigKey sets one key in the configuration file, leaving the rest alone.
//
// WHY THE RAW DOCUMENT. The file is read as a generic JSON object and written
// back as one. Reading it into the configuration type and re-rendering it would
// discard everything that type does not know about — and the file belongs to the
// operator, not to the daemon. A key the daemon has never heard of is still the
// operator's to keep.
func writeConfigKey(path, key, value string) error {
	doc := map[string]any{}

	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if len(bytes.TrimSpace(raw)) > 0 {
			if err := json.Unmarshal(raw, &doc); err != nil {
				// Refused rather than overwritten: the file may hold settings
				// that are not recoverable from anywhere else, and a syntax
				// error is no reason to lose them.
				return fmt.Errorf("config: parsing %s: %w", path, err)
			}
		}
	case os.IsNotExist(err):
		// No file yet. The new one is created from nothing.
	default:
		return fmt.Errorf("config: reading %s: %w", path, err)
	}

	doc[key] = value

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("config: rendering %s: %w", path, err)
	}
	out = append(out, '\n')

	return writeFileAtomic(path, out)
}

// writeFileAtomic replaces a file through a temporary one in the same directory.
//
// A rename within one filesystem is atomic, so an interrupted write cannot leave
// a half-written configuration behind. That matters more here than for state: a
// truncated file loses settings nothing can reconstruct.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("config: creating %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return fmt.Errorf("config: creating a temporary file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()

	// Removed on every failure path: a temporary file left behind in the
	// configuration directory would look like a configuration to whatever
	// reads that directory next.
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("config: writing %s: %w", tmpName, err)
	}
	// Flushed before the rename: a rename makes the name visible, not the
	// contents, and a crash between the two would publish an empty file.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("config: flushing %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: closing %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("config: setting the mode on %s: %w", tmpName, err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("config: replacing %s: %w", path, err)
	}
	tmpName = "" // renamed — nothing left to remove
	return nil
}
