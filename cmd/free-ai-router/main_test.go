package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// capture runs a function with stdout redirected and returns what it printed.
//
// The command writes to stdout directly, and the only way to see what an
// operator would see is to stand in the same place they do.
func capture(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w

	fn()

	os.Stdout = saved
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// writeConfig puts a minimal, valid configuration on disk.
func writeConfig(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"mode":"local","listen":"127.0.0.1:0","transport":"direct","upstream":"https://example.invalid"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestPrintConfigNeedsNoCommand.
//
// The usage text documents --print-config as a flag, not as a subcommand, so the
// documented form is `free-ai-router --config X --print-config`. It used to
// answer `unknown command "--config"`: the first argument was read as a verb and
// a flag read as a verb fails. The flag answers a question and exits, which is
// exactly what a verb-less invocation should do.
func TestPrintConfigNeedsNoCommand(t *testing.T) {
	path := writeConfig(t)

	var err error
	out := capture(t, func() {
		err = run([]string{"--config", path, "--print-config"})
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, want := range []string{"mode", "transport", "file:"} {
		if !strings.Contains(out, want) {
			t.Errorf("the printed configuration has no %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, path) {
		t.Errorf("the printed configuration does not name the file it read (%s):\n%s", path, out)
	}
}

// TestFlagsAloneDoNotStartTheDaemon.
//
// Every flag except --print-config would mean "start a daemon" if it were
// accepted without a command, and a daemon started by accident — someone typing
// `--json` to see machine-readable output — is worse than an error. So the verb
// stays mandatory for everything else.
func TestFlagsAloneDoNotStartTheDaemon(t *testing.T) {
	path := writeConfig(t)

	err := run([]string{"--config", path, "--json"})
	if err == nil {
		t.Fatal("a flag alone started the daemon")
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("error = %q, want it to refuse the flag as a command", err)
	}
}

// TestHasFlagIgnoresTheNegatedForm.
//
// `--print-config=false` is a request NOT to print. Treating it as a request to
// print would start a daemon in the one case where the operator explicitly asked
// for nothing to happen — so only the bare form counts.
func TestHasFlagIgnoresTheNegatedForm(t *testing.T) {
	if !hasFlag([]string{"--config", "x", "--print-config"}, "--print-config") {
		t.Error("the bare form was not seen")
	}
	if hasFlag([]string{"--print-config=false"}, "--print-config") {
		t.Error("the negated form was taken for a request to print")
	}
}
