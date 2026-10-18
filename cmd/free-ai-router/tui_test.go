package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestDaemonUptimeComesFromThePIDFile checks the counter in the banner's corner.
//
// THE AGE IS ONLY REPORTED FOR A LIVE PROCESS. An unclean shutdown leaves the
// file behind, and its age is then a number about a period during which nothing
// was running — the monitor would claim a daemon that has not been up since. The
// answer to "how long has it been up" is zero, which the frame draws as a dashed
// placeholder rather than as a daemon that started this instant.
func TestDaemonUptimeComesFromThePIDFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.pid")

	// The running process is this test, whose identifier is certain to be alive.
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		t.Fatalf("writing the pid file: %v", err)
	}

	started := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, started, started); err != nil {
		t.Fatalf("dating the pid file: %v", err)
	}

	got := daemonUptime(path)
	if got < 2*time.Hour || got > 2*time.Hour+time.Minute {
		t.Errorf("a daemon started two hours ago reports %v", got)
	}

	// The same file naming a process that is not there: the leftover of a run
	// that ended badly, and not a running daemon.
	if err := os.WriteFile(path, []byte("999999\n"), 0o644); err != nil {
		t.Fatalf("writing the stale pid file: %v", err)
	}
	if got := daemonUptime(path); got != 0 {
		t.Errorf("a stale pid file reports %v, want 0", got)
	}

	// A file that is not there, and no file named at all.
	if got := daemonUptime(filepath.Join(dir, "missing.pid")); got != 0 {
		t.Errorf("a missing pid file reports %v, want 0", got)
	}
	if got := daemonUptime(""); got != 0 {
		t.Errorf("no pid file reports %v, want 0", got)
	}

	// A clock that ran backwards must not produce a negative age: the counter is
	// eight cells of digits and has no room for a sign.
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		t.Fatalf("rewriting the pid file: %v", err)
	}
	ahead := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, ahead, ahead); err != nil {
		t.Fatalf("dating the pid file into the future: %v", err)
	}
	if got := daemonUptime(path); got != 0 {
		t.Errorf("a pid file from the future reports %v, want 0", got)
	}
}
