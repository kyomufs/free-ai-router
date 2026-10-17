package state

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The log is the single place the daemon writes to.
//
// WHY ONE. Previously the records about the daemon's life and the records about
// requests lived in different places, and the picture could not be
// reconstructed from them: half the context was lost in the jump from one file
// to the other. Here both kinds of records go into one stream, in the order
// they appear, and therefore read as a story.
//
// ROTATION happens AT STARTUP, not on every write. That is the only moment
// when the file can be moved without racing its own writer: request handlers
// write to the log concurrently, and renaming under them would lose lines.

// Log limits.
const (
	// rotateAt is the threshold past which the log is rotated.
	rotateAt = 8 << 20 // 8 MiB
	// keepGenerations is how many previous files are kept.
	keepGenerations = 3
)

// Logger is the daemon's log.
type Logger struct {
	mu   sync.Mutex
	path string
	file *os.File
}

// OpenLogger opens the log at the given path.
//
// Rotation happens here: on open the old file is shifted if needed, and only
// then does a descriptor for writing appear.
func OpenLogger(path string) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("state: creating the log directory: %w", err)
	}
	if err := rotate(path); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("state: opening the log: %w", err)
	}
	return &Logger{path: path, file: f}, nil
}

// rotate shifts the log if it has grown to the threshold.
//
// Generation names: app.log.1 is the most recent past, app.log.3 the oldest.
// The shift runs from the higher numbers to the lower ones, otherwise the
// generations would overwrite each other.
func rotate(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("state: inspecting the log: %w", err)
	}
	if info.Size() < rotateAt {
		return nil
	}

	oldest := fmt.Sprintf("%s.%d", path, keepGenerations)
	_ = os.Remove(oldest)

	for i := keepGenerations - 1; i >= 1; i-- {
		from := fmt.Sprintf("%s.%d", path, i)
		to := fmt.Sprintf("%s.%d", path, i+1)
		if _, err := os.Stat(from); err != nil {
			continue // this generation does not exist yet
		}
		if err := os.Rename(from, to); err != nil {
			return fmt.Errorf("state: rotating %s: %w", from, err)
		}
	}

	if err := os.Rename(path, path+".1"); err != nil {
		return fmt.Errorf("state: rotating %s: %w", path, err)
	}
	return nil
}

// Printf writes a line to the log.
//
// Format: time, level, message. The level is a word, not a code: a human reads
// the log, and there is no reason for them to look up a code mapping table.
func (l *Logger) Printf(level, format string, args ...any) {
	if l == nil {
		return
	}
	line := fmt.Sprintf("%s %s %s\n",
		time.Now().Format("2006-01-02 15:04:05.000"),
		level,
		fmt.Sprintf(format, args...),
	)

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_, _ = l.file.WriteString(line)
	}
}

// Info records an event in the daemon's life.
func (l *Logger) Info(format string, args ...any) { l.Printf("INFO", format, args...) }

// Warn records what deserves attention but does not get in the way of work.
func (l *Logger) Warn(format string, args ...any) { l.Printf("WARN", format, args...) }

// Error records a refusal.
func (l *Logger) Error(format string, args ...any) { l.Printf("ERROR", format, args...) }

// Close closes the log.
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// Path returns the log path.
func (l *Logger) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Truncate truncates the log IN PLACE.
//
// In place, rather than by deleting: the daemon keeps the descriptor open, and
// removing the file would leave it writing into nowhere for as long as the
// process lives. Truncating through the same descriptor keeps the writes
// continuous.
func Truncate(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("state: truncating %s: %w", path, err)
	}
	return f.Close()
}

// LogFileSize returns the log size together with the generations.
//
// Needed by the `data` command: the operator should see how much the log takes
// up, not just the current file.
func LogFileSize(path string) (bytes int64, files int, err error) {
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	base := filepath.Base(path)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name != base && !strings.HasPrefix(name, base+".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		bytes += info.Size()
		files++
	}
	return bytes, files, nil
}

// Tail returns the last n lines of the log.
//
// It reads from the end of the file in blocks, not as a whole: the log can be
// megabytes long, while it is usually the last lines that are needed.
func Tail(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("state: reading the log: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	const block = 64 << 10
	size := info.Size()
	var (
		buf    []byte
		offset = size
	)
	for offset > 0 && strings.Count(string(buf), "\n") <= n {
		readSize := int64(block)
		if offset < readSize {
			readSize = offset
		}
		offset -= readSize

		chunk := make([]byte, readSize)
		if _, err := f.ReadAt(chunk, offset); err != nil && err != io.EOF {
			return nil, err
		}
		buf = append(chunk, buf...)
	}

	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil, nil
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := make([]string, len(lines))
	copy(out, lines)
	return out, nil
}

// LogFiles returns the log generations FROM NEWEST TO OLDEST.
//
// The order is given explicitly rather than by sorting the names: `sort.Strings`
// would put "app.log" before "app.log.1", that is the current file would come
// before the generations, although it is the newest in meaning. And this is
// exactly how the output is read: "first what happened recently".
func LogFiles(path string) []string {
	out := []string{path}
	for i := 1; i <= keepGenerations; i++ {
		candidate := fmt.Sprintf("%s.%d", path, i)
		if _, err := os.Stat(candidate); err == nil {
			out = append(out, candidate)
		}
	}
	return out
}
