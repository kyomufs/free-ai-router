package pool

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// PassRunner runs proxprobe's full pass: refresh, parse, merge, tcp, http.
//
// The command is an argv rather than a shell line: the pool does no quoting, and
// every argument — the zen URL included — is the operator's explicit decision.
type PassRunner struct {
	Command []string
	Dir     string
	Timeout time.Duration
	// LogPath is where the pass's own output goes. Empty means proxprobe.log in the
	// work directory, which is the one place the pass already writes to.
	LogPath string
}

// Run executes one pass.
//
// A pass that fails is not fatal to the pool: the current list keeps serving, and
// the failure is reported with the pass's own last lines, because the reason is
// usually one the operator has to fix (a source that stopped answering).
func (r PassRunner) Run(ctx context.Context) error {
	if len(r.Command) == 0 {
		return nil
	}
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}

	logPath := r.LogPath
	if logPath == "" {
		logPath = filepath.Join(r.Dir, "proxprobe.log")
	}
	if dir := filepath.Dir(logPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("pool: proxprobe log directory: %w", err)
		}
	}
	logFile, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("pool: proxprobe log: %w", err)
	}

	cmd := exec.CommandContext(ctx, r.Command[0], r.Command[1:]...)
	if r.Dir != "" {
		cmd.Dir = r.Dir
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// A core left behind by a killed pass must not hold the pipe open forever.
	cmd.WaitDelay = 5 * time.Second

	runErr := cmd.Run()
	logFile.Close()

	if runErr == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("pool: proxprobe pass timed out after %s: %w", r.Timeout, ctxErr)
	}
	return fmt.Errorf("pool: proxprobe pass failed: %w%s", runErr, tailOf(logPath, 3))
}

// tailOf appends the last lines of the pass log to an error, where they are the only
// clue an operator gets without opening the file.
func tailOf(path string, lines int) string {
	body, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	all := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(all) == 0 || (len(all) == 1 && strings.TrimSpace(all[0]) == "") {
		return ""
	}
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	for i, line := range all {
		all[i] = strings.TrimSpace(line)
	}
	return ": " + strings.Join(all, " | ")
}
