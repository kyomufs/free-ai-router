package pool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPassRunnerRunsTheCommand: the pass is an argv run in the work directory, with
// its output kept where the failure can be read.
func TestPassRunnerRunsTheCommand(t *testing.T) {
	dir := t.TempDir()
	runner := PassRunner{
		Command: []string{"/bin/sh", "-c", "printf ran > marker; echo done"},
		Dir:     dir,
		Timeout: 10 * time.Second,
	}
	if err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "marker")); err != nil {
		t.Fatalf("the pass did not run in its work directory: %v", err)
	}
	log, err := os.ReadFile(filepath.Join(dir, "proxprobe.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "done") {
		t.Fatalf("log %q", log)
	}
}

// TestPassRunnerReportsTheLogTail: the operator fixes the pass from the error line, so
// the error carries the last of the pass's own output.
func TestPassRunnerReportsTheLogTail(t *testing.T) {
	runner := PassRunner{
		Command: []string{"/bin/sh", "-c", "echo 'source goida: EOF' >&2; exit 3"},
		Dir:     t.TempDir(),
		Timeout: 10 * time.Second,
	}
	err := runner.Run(context.Background())
	if err == nil {
		t.Fatal("a failing pass must be an error")
	}
	if !strings.Contains(err.Error(), "source goida: EOF") {
		t.Fatalf("err %v", err)
	}
}

// TestPassRunnerWithoutACommand: an empty command means an outside scheduler owns the
// pass, and the pool then only reads its artifacts.
func TestPassRunnerWithoutACommand(t *testing.T) {
	if err := (PassRunner{}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}
