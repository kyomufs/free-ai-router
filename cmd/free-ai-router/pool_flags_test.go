package main

import (
	"testing"

	"free-ai-router/internal/config"
	"free-ai-router/internal/pool"
)

// TestPoolFlagsLandInTheTransportSection: a pool flag is one key of
// transport_settings.pool, not a setting of its own, and it must survive into the
// decoder that reads that section.
func TestPoolFlagsLandInTheTransportSection(t *testing.T) {
	cfg := config.Default()
	f := &flags{
		poolCorePath:     "/nix/store/x-mihomo/bin/mihomo",
		poolInterval:     "4h",
		poolMaxNodes:     "7",
		poolProbeModel:   "mimo-v2.6-flash",
		poolSource:       pool.SourceProxprobe,
		poolProxprobeDir: "/var/lib/free-ai-router/proxprobe",
		poolProxprobeCmd: "/run/current-system/sw/bin/proxprobe all -workdir /var/lib/free-ai-router/proxprobe",
	}
	f.applyPool(cfg)

	section, ok := cfg.TransportConfig["pool"].(map[string]any)
	if !ok {
		t.Fatalf("the pool section is missing: %#v", cfg.TransportConfig)
	}

	got, err := pool.DecodeSettings(section)
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxNodes != 7 {
		t.Errorf("max_nodes %d, want 7", got.MaxNodes)
	}
	if got.Interval.String() != "4h0m0s" {
		t.Errorf("interval %s, want 4h", got.Interval)
	}
	if got.CorePath != "/nix/store/x-mihomo/bin/mihomo" {
		t.Errorf("core_path %q", got.CorePath)
	}
	if got.ProbeModel != "mimo-v2.6-flash" {
		t.Errorf("probe_model %q", got.ProbeModel)
	}
	if got.ProxprobeWorkDir != "/var/lib/free-ai-router/proxprobe" {
		t.Errorf("proxprobe_workdir %q", got.ProxprobeWorkDir)
	}
	if len(got.ProxprobeCommand) != 4 || got.ProxprobeCommand[0] != "/run/current-system/sw/bin/proxprobe" {
		t.Errorf("proxprobe_command %v", got.ProxprobeCommand)
	}
}

// TestPoolFlagsLeaveTheSectionAlone: a flag that was not given must not overwrite
// what the file holds.
func TestPoolFlagsLeaveTheSectionAlone(t *testing.T) {
	cfg := config.Default()
	cfg.TransportConfig = map[string]any{
		"pool": map[string]any{
			"source":            pool.SourceProxprobe,
			"max_nodes":         float64(12),
			"core":              "mihomo",
			"proxprobe_workdir": "/var/lib/free-ai-router/proxprobe",
		},
	}

	f := &flags{poolProbeModel: "m"}
	f.applyPool(cfg)

	got, err := pool.DecodeSettings(cfg.TransportConfig["pool"].(map[string]any))
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxNodes != 12 {
		t.Errorf("max_nodes %d, want the file's 12", got.MaxNodes)
	}
	if got.Core != "mihomo" {
		t.Errorf("core %q, want the file's mihomo", got.Core)
	}
	if got.ProbeModel != "m" {
		t.Errorf("probe_model %q, want the flag's m", got.ProbeModel)
	}
}
