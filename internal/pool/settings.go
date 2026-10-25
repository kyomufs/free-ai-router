package pool

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Settings is the pool's configuration, taken from transport_settings.pool.
//
// Every value has a default: a pool that is switched on has to work without being
// spelled out, because the numbers below are measurements rather than taste.
type Settings struct {
	Core string
	// Source is kept as a setting even though proxprobe is the only one: a file that
	// names a source is a record of a decision, and an unknown name must fail here
	// rather than silently select a pipeline it did not mean.
	Source           string
	CorePath         string
	ProxprobeWorkDir string
	ProxprobeCommand []string
	ProxprobeMaxAge  time.Duration
	PassTimeout      time.Duration
	FirstEvalUTC     string
	Interval         time.Duration
	ProbeTimeout     time.Duration
	ProbeWorkers     int
	MaxProbe         int
	ProbeModel       string
	MaxNodes         int
	HealthInterval   time.Duration
	DeadThreshold    int
	ReevalMinGap     time.Duration
	Grace            time.Duration
}

// DefaultSettings returns the measured defaults.
func DefaultSettings() Settings {
	return Settings{
		Core:           "mihomo",
		Source:         SourceProxprobe,
		PassTimeout:    10 * time.Minute,
		FirstEvalUTC:   "03:00",
		Interval:       8 * time.Hour,
		ProbeTimeout:   12 * time.Second,
		ProbeWorkers:   6,
		MaxProbe:       60,
		MaxNodes:       20,
		HealthInterval: 30 * time.Second,
		DeadThreshold:  2,
		ReevalMinGap:   10 * time.Minute,
		Grace:          30 * time.Second,
	}
}

// DecodeSettings reads the transport settings.
// Section returns the pool's own settings out of a transport settings map.
//
// The registry hands a transport the whole map — the flat axes such as the address
// family live there too — while the pool's keys are nested under "pool" so that a
// file can describe several transports without their keys colliding. A map that is
// already the section is taken as one: a caller that built the transport directly
// has nothing to unwrap.
func Section(cfg map[string]any) map[string]any {
	if raw, ok := cfg["pool"].(map[string]any); ok {
		return raw
	}
	return cfg
}

func DecodeSettings(cfg map[string]any) (Settings, error) {
	cfg = Section(cfg)
	s := DefaultSettings()
	s.Core = stringValue(cfg, "core", s.Core)
	s.Source = strings.ToLower(stringValue(cfg, "source", s.Source))
	s.CorePath = stringValue(cfg, "core_path", s.CorePath)
	s.ProxprobeWorkDir = stringValue(cfg, "proxprobe_workdir", s.ProxprobeWorkDir)
	s.ProxprobeCommand = wordsValue(cfg, "proxprobe_command")
	s.ProxprobeMaxAge = durationValue(cfg, "proxprobe_max_age", s.ProxprobeMaxAge)
	s.PassTimeout = durationValue(cfg, "pass_timeout", s.PassTimeout)
	s.FirstEvalUTC = stringValue(cfg, "first_eval_utc", s.FirstEvalUTC)
	s.ProbeModel = stringValue(cfg, "probe_model", s.ProbeModel)
	s.Interval = durationValue(cfg, "interval", s.Interval)
	s.ProbeTimeout = durationValue(cfg, "probe_timeout", s.ProbeTimeout)
	s.HealthInterval = durationValue(cfg, "health_interval", s.HealthInterval)
	s.ReevalMinGap = durationValue(cfg, "reeval_min_gap", s.ReevalMinGap)
	s.Grace = durationValue(cfg, "grace", s.Grace)
	s.ProbeWorkers = intValue(cfg, "probe_workers", s.ProbeWorkers)
	s.MaxProbe = intValue(cfg, "max_probe", s.MaxProbe)
	s.MaxNodes = intValue(cfg, "max_nodes", s.MaxNodes)
	s.DeadThreshold = intValue(cfg, "dead_threshold", s.DeadThreshold)

	if s.Core == "" {
		s.Core = "mihomo"
	}
	if s.Interval <= 0 {
		return s, fmt.Errorf("pool: interval must be positive")
	}
	if s.PassTimeout <= 0 {
		s.PassTimeout = 10 * time.Minute
	}
	// A pass and the evaluation that reads it run at the same instants, so the
	// freshness bound defaults to one interval: anything older is the previous slot's
	// work and describes servers that were measured before the last rotation.
	if s.ProxprobeMaxAge <= 0 {
		s.ProxprobeMaxAge = s.Interval
	}
	if s.Source != SourceProxprobe {
		return s, fmt.Errorf("pool: unknown source %q; the only source is %q", s.Source, SourceProxprobe)
	}
	if strings.TrimSpace(s.ProxprobeWorkDir) == "" {
		return s, fmt.Errorf("pool: proxprobe_workdir is required")
	}
	return s, nil
}

// FirstEvaluation is the anchor of the schedule: the first slot on the given day.
func (s Settings) FirstEvaluation(now time.Time) time.Time {
	hour, minute := 3, 0
	if h, m, ok := strings.Cut(s.FirstEvalUTC, ":"); ok {
		if v, err := strconv.Atoi(h); err == nil && v >= 0 && v < 24 {
			hour = v
		}
		if v, err := strconv.Atoi(m); err == nil && v >= 0 && v < 60 {
			minute = v
		}
	}
	now = now.UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, time.UTC)
}

func stringValue(cfg map[string]any, key, fallback string) string {
	raw, ok := cfg[key]
	if !ok || raw == nil {
		return fallback
	}
	if v, ok := raw.(string); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

func stringsValue(cfg map[string]any, key string) []string {
	raw, ok := cfg[key]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return append([]string(nil), v...)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		// One string may carry several URLs separated by commas or newlines:
		// that is how the same setting is usually pasted.
		fields := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == '\n' })
		out := make([]string, 0, len(fields))
		for _, field := range fields {
			if field = strings.TrimSpace(field); field != "" {
				out = append(out, field)
			}
		}
		return out
	}
	return nil
}

// wordsValue reads an argv. A string is split on whitespace — there is no shell
// here, so a quoted argument has to arrive as an array — and an array is taken as
// it is written.
func wordsValue(cfg map[string]any, key string) []string {
	raw, ok := cfg[key]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return append([]string(nil), v...)
	case []any:
		words := make([]string, 0, len(v))
		for _, item := range v {
			if word, ok := item.(string); ok && strings.TrimSpace(word) != "" {
				words = append(words, word)
			}
		}
		return words
	case string:
		return strings.Fields(v)
	}
	return nil
}

func intValue(cfg map[string]any, key string, fallback int) int {
	raw, ok := cfg[key]
	if !ok || raw == nil {
		return fallback
	}
	switch v := raw.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return int(i)
		}
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return i
		}
	}
	return fallback
}

func durationValue(cfg map[string]any, key string, fallback time.Duration) time.Duration {
	raw, ok := cfg[key]
	if !ok || raw == nil {
		return fallback
	}
	switch v := raw.(type) {
	case time.Duration:
		return v
	case float64:
		return time.Duration(v * float64(time.Second))
	case int:
		return time.Duration(v) * time.Second
	case string:
		if d, err := time.ParseDuration(strings.TrimSpace(v)); err == nil {
			return d
		}
		if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return time.Duration(secs) * time.Second
		}
	}
	return fallback
}

// coreByName resolves the core implementation. Only mihomo exists: it is the only
// measured core that renders the whole profile the pass carries (see the design's matrix).
func coreByName(name string) (Core, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "mihomo":
		return NewMihomo(), nil
	}
	return nil, fmt.Errorf("pool: core %q is not supported", name)
}
