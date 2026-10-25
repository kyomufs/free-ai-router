package pool

import (
	"testing"
	"time"
)

func TestDecodeSettingsDefaults(t *testing.T) {
	s, err := DecodeSettings(map[string]any{
		"source":            SourceProxprobe,
		"proxprobe_workdir": "/var/lib/free-ai-router/proxprobe",
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Core != "mihomo" || s.MaxProbe != 60 || s.MaxNodes != 20 || s.ProbeWorkers != 6 {
		t.Fatalf("defaults %+v", s)
	}
	if s.Interval != 8*time.Hour || s.Grace != 30*time.Second {
		t.Fatalf("durations %+v", s)
	}
}

func TestDecodeSettingsOverrides(t *testing.T) {
	s, err := DecodeSettings(map[string]any{
		"source":            SourceProxprobe,
		"proxprobe_workdir": "/var/lib/free-ai-router/proxprobe",
		"proxprobe_max_age": "9h",
		"core_path":         "/nix/store/x/bin/mihomo",
		"first_eval_utc":    "02:30",
		"interval":          "6h",
		"max_nodes":         float64(5),
		"dead_threshold":    float64(3),
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.FirstEvalUTC != "02:30" || s.Interval != 6*time.Hour || s.ProxprobeMaxAge != 9*time.Hour {
		t.Fatalf("values %+v", s)
	}
	if s.MaxNodes != 5 || s.DeadThreshold != 3 {
		t.Fatalf("numbers %+v", s)
	}
	first := s.FirstEvaluation(time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC))
	if first.Hour() != 2 || first.Minute() != 30 {
		t.Fatalf("first evaluation %s", first)
	}
}

func TestDecodeSettingsNeedsASource(t *testing.T) {
	// The default source is the pass, and a pass without a directory to read is a
	// configuration error rather than an empty list at the first rotation.
	if _, err := DecodeSettings(map[string]any{}); err == nil {
		t.Fatal("a proxprobe pool without a work directory is a configuration error")
	}
	if _, err := DecodeSettings(map[string]any{"source": "builtin", "proxprobe_workdir": "/tmp/p"}); err == nil {
		t.Fatal("a removed source must be refused, not silently ignored")
	}
	if _, err := DecodeSettings(map[string]any{"source": "elsewhere", "proxprobe_workdir": "/tmp/p"}); err == nil {
		t.Fatal("an unknown source must be refused, not silently replaced")
	}
}

func TestDecodeSettingsProxprobe(t *testing.T) {
	s, err := DecodeSettings(map[string]any{
		"source":            "proxprobe",
		"proxprobe_workdir": "/var/lib/free-ai-router/proxprobe",
		"proxprobe_command": "proxprobe all -config /etc/sources.yaml -core mihomo",
		"interval":          "8h",
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Source != SourceProxprobe || s.ProxprobeWorkDir != "/var/lib/free-ai-router/proxprobe" {
		t.Fatalf("source %+v", s)
	}
	if len(s.ProxprobeCommand) != 6 || s.ProxprobeCommand[0] != "proxprobe" {
		t.Fatalf("command %v", s.ProxprobeCommand)
	}
	// A pass and the read that follows it run in the same slot, so the default
	// freshness bound is one interval.
	if s.ProxprobeMaxAge != 8*time.Hour || s.PassTimeout != 10*time.Minute {
		t.Fatalf("durations %+v", s)
	}
}

func TestDecodeSettingsProxprobeCommandArray(t *testing.T) {
	s, err := DecodeSettings(map[string]any{
		"proxprobe_workdir": "/w",
		"proxprobe_command": []any{"proxprobe", "all", "-url", "https://example.test/models"},
		"pass_timeout":      "2m",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.ProxprobeCommand) != 4 || s.ProxprobeCommand[3] != "https://example.test/models" {
		t.Fatalf("command %v", s.ProxprobeCommand)
	}
	if s.PassTimeout != 2*time.Minute {
		t.Fatalf("pass timeout %s", s.PassTimeout)
	}
}

func TestCoreByNameRefusesUnknownCores(t *testing.T) {
	if _, err := coreByName("sing-box"); err == nil {
		t.Fatal("an unsupported core must be refused, not silently replaced")
	}
	if core, err := coreByName(""); err != nil || core.ID() != "mihomo" {
		t.Fatalf("default core %v %v", core, err)
	}
}

// TestTheSectionIsThePoolPartOfTheSettings: the registry hands one map to every
// transport, so the pool's keys must not be read from the top level — a flat
// "core" there would be the direct transport's business.
func TestTheSectionIsThePoolPartOfTheSettings(t *testing.T) {
	nested := map[string]any{
		"family": "v4",
		"pool": map[string]any{
			"source":            SourceProxprobe,
			"proxprobe_workdir": "/var/lib/free-ai-router/proxprobe",
			"max_nodes":         float64(9),
		},
	}
	got, err := DecodeSettings(nested)
	if err != nil {
		t.Fatalf("nested: %v", err)
	}
	if got.MaxNodes != 9 || got.ProxprobeWorkDir == "" {
		t.Fatalf("nested: %+v", got)
	}

	flat := map[string]any{"source": SourceProxprobe, "proxprobe_workdir": "/var/lib/free-ai-router/proxprobe"}
	if _, err := DecodeSettings(flat); err != nil {
		t.Fatalf("flat: %v", err)
	}
}
