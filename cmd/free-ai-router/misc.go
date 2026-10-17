package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"free-ai-router/internal/config"
	"free-ai-router/internal/state"
)

// cmdStop stops the daemon.
//
// Implemented through a signal to the process: the daemon catches SIGTERM and
// exits cleanly, waiting for the current requests to finish. There is no
// separate control channel for stopping — the signal is meant for this, and it
// works even when the control surface is unreachable.
func cmdStop(args []string) error {
	cfg, _, err := loadConfig(args)
	if err != nil || cfg == nil {
		return err
	}

	pid, pidErr := findDaemonPID()
	if pidErr != nil || pid == 0 {
		return fmt.Errorf("daemon not found: there is nothing to stop")
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("process %d: %w", pid, err)
	}
	if err := proc.Signal(os.Interrupt); err != nil {
		return fmt.Errorf("signal to process %d: %w", pid, err)
	}
	fmt.Printf("stopped process %d\n", pid)
	return nil
}

// cmdLogs prints the log.
func cmdLogs(args []string) error {
	cfg, err := loadConfigSimple(args)
	if err != nil || cfg == nil {
		return err
	}

	count := 50
	for i, a := range args {
		if a == "-n" && i+1 < len(args) {
			if n, convErr := strconv.Atoi(args[i+1]); convErr == nil && n > 0 {
				count = n
			}
		}
		if strings.HasPrefix(a, "-n=") {
			if n, convErr := strconv.Atoi(strings.TrimPrefix(a, "-n=")); convErr == nil && n > 0 {
				count = n
			}
		}
	}

	lines, err := state.Tail(cfg.Paths.LogFile, count)
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		fmt.Println("the log is empty")
		return nil
	}
	for _, line := range lines {
		fmt.Println(line)
	}
	return nil
}

// cmdData shows the trace on disk and can clean it.
func cmdData(args []string) error {
	cfg, f, err := loadConfig(args)
	if err != nil || cfg == nil {
		return err
	}

	// Subcommand parsing: the first word after the flags.
	var action, scope string
	for _, a := range f.positional {
		switch {
		case action == "":
			action = a
		case scope == "":
			scope = a
		}
	}

	if action == "clear" {
		return dataClear(cfg, scope)
	}

	// Showing without arguments: what lies there and how much it takes.
	fmt.Printf("%-12s %-45s %10s\n", "WHAT", "WHERE", "SIZE")
	fmt.Printf("%-12s %-45s %10s\n", "configuration", cfg.Paths.ConfigFile, humanSize(fileSize(cfg.Paths.ConfigFile)))
	fmt.Printf("%-12s %-45s %10s\n", "state", cfg.Paths.StateFile, humanSize(fileSize(cfg.Paths.StateFile)))

	logBytes, logFiles, _ := state.LogFileSize(cfg.Paths.LogFile)
	fmt.Printf("%-12s %-45s %10s", "log", cfg.Paths.LogFile, humanSize(logBytes))
	if logFiles > 1 {
		fmt.Printf(" (%d files)", logFiles)
	}
	fmt.Println()

	fmt.Println()
	fmt.Println("clearing: data clear logs | stats | all")
	fmt.Println("all does not touch the configuration: deleting the file that describes")
	fmt.Println("the daemon's design is not maintenance but the loss of a deployment.")
	return nil
}

// dataClear clears the chosen scope.
//
// The configuration belongs to NO scope, all included. Deleting the file that
// describes the daemon's design is not maintenance but the loss of a deployment:
// it shows which providers and keys were configured, and that cannot be restored
// from memory.
func dataClear(cfg *config.Config, scope string) error {
	switch scope {
	case "logs":
		if err := state.Truncate(cfg.Paths.LogFile); err != nil {
			return err
		}
		fmt.Println("the log has been cleared")
		return nil

	case "stats":
		st, err := state.Open(cfg.Paths.StateFile)
		if err != nil && st == nil {
			return err
		}
		if st != nil {
			if err := st.Clear(); err != nil {
				return err
			}
		}
		fmt.Println("the counters have been cleared")
		return nil

	case "all":
		if err := state.Truncate(cfg.Paths.LogFile); err != nil {
			return err
		}
		st, err := state.Open(cfg.Paths.StateFile)
		if err != nil && st == nil {
			return err
		}
		if st != nil {
			if err := st.Clear(); err != nil {
				return err
			}
		}
		fmt.Println("the log and the counters have been cleared")
		fmt.Printf("the configuration is untouched: %s\n", cfg.Paths.ConfigFile)
		return nil

	case "":
		return fmt.Errorf("no scope given: logs | stats | all")

	default:
		return fmt.Errorf("unknown scope %q (logs | stats | all)", scope)
	}
}

// fileSize returns the file size, zero if it does not exist.
func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// humanSize prints the size in convenient units.
//
// The units are binary: the size of a file on disk is measured in them, and
// mixing with decimal ones is not allowed — the operator compares the output
// with what the system shows.
func humanSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMGT"[exp])
}

// writePIDFile records the running daemon's identifier.
//
// The directory is created rather than assumed: the runtime directory is
// cleared by the system between sessions, so on a fresh boot it does not exist
// yet, and failing there would disable `stop` for the whole session.
func writePIDFile(path string) error {
	if path == "" {
		return fmt.Errorf("no pid file path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	return os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644)
}

// removePIDFile clears the identifier.
//
// The error is deliberately dropped: this runs on the way out, when the
// process is leaving anyway. Reporting a failure to delete a bookkeeping file
// would turn a clean shutdown into a failed one, and a leftover file is caught
// by the liveness check in findDaemonPID.
func removePIDFile(path string) {
	if path != "" {
		_ = os.Remove(path)
	}
}

// findDaemonPID reports the identifier of a running daemon.
//
// The file is trusted only after the process it names is confirmed to exist:
// an identifier outlives nothing, and after an unclean shutdown the number it
// holds may belong to an unrelated process that the system happened to assign
// it to. Signalling that process would be worse than reporting nothing, so a
// dead identifier is treated exactly like an absent file.
func findDaemonPID() (int, error) {
	cfg, err := config.Load()
	if err != nil {
		return 0, err
	}
	path := cfg.Paths.PIDFile
	if path == "" {
		return 0, nil
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading %s: %w", path, err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		// A corrupt file is a stale claim, not an error worth surfacing: the
		// caller's question is whether a daemon is running, and the answer is
		// no.
		return 0, nil
	}
	if !processAlive(pid) {
		return 0, nil
	}
	return pid, nil
}

// processAlive reports whether a process with this identifier exists.
//
// Signal 0 performs the permission and existence checks without delivering
// anything, which is the portable way to ask. On Linux the process table is
// visible for our own processes, so this answers accurately for the daemon.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
