package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"free-ai-router/internal/config"
	"free-ai-router/internal/state"
)

// Read commands work LOCALLY — against the configuration, state and log.
//
// This is a deliberate decision: they must answer even when the daemon is down.
// An operator whose daemon failed to start goes to look at the counters and the
// log first thing, and a command requiring a live daemon would be useless
// exactly when it is needed most.

// loadConfig reads the configuration and parses the flags.
func loadConfig(args []string) (*config.Config, *flags, error) {
	f, err := parseFlags(args)
	if err != nil {
		return nil, nil, err
	}
	if f == nil {
		return nil, nil, nil
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	f.applyToConfig(cfg)
	return cfg, f, nil
}

// cmdConfig shows the effective configuration.
func cmdConfig(args []string) error {
	cfg, f, err := loadConfig(args)
	if err != nil || cfg == nil {
		return err
	}
	if f.json {
		return printJSON(map[string]any{
			"mode":                   cfg.Mode.Val,
			"mode_source":            cfg.Mode.From,
			"listen":                 cfg.Listen.Val,
			"listen_source":          cfg.Listen.From,
			"transport":              cfg.Transport.Val,
			"upstream":               cfg.Upstream.Val,
			"first_event_timeout":    cfg.FirstEventTimeout.Val.String(),
			"idle_timeout":           cfg.IdleTimeout.Val.String(),
			"reasoning_idle_timeout": cfg.ReasoningIdleTimeout.Val.String(),
			"config_file":            cfg.Paths.ConfigFile,
			"state_file":             cfg.Paths.StateFile,
			"log_file":               cfg.Paths.LogFile,
			"providers":              providerNames(cfg),
		})
	}
	return printEffectiveConfig(cfg)
}

// cmdStatus shows the state.
//
// It is read FROM DISK, not from the daemon: the command must answer even when
// the daemon is stopped. A live daemon serves the same information more richly
// — through the control surface — but requiring it here is not acceptable.
func cmdStatus(args []string) error {
	cfg, f, err := loadConfig(args)
	if err != nil || cfg == nil {
		return err
	}

	st, openErr := state.Open(cfg.Paths.StateFile)
	if openErr != nil {
		fmt.Fprintf(os.Stderr, "warning: state: %v\n", openErr)
	}

	snap := state.Snapshot{}
	if st != nil {
		snap = st.Snapshot()
	}

	if f.json {
		return printJSON(map[string]any{
			"mode":      cfg.Mode.Val,
			"listen":    cfg.Listen.Val,
			"transport": cfg.Transport.Val,
			"providers": providerNames(cfg),
			"stats":     snap,
		})
	}

	fmt.Printf("mode       %s\n", cfg.Mode.Val)
	fmt.Printf("address    %s\n", cfg.Listen.Val)
	fmt.Printf("transport  %s\n", cfg.Transport.Val)
	fmt.Printf("providers  %s\n", strings.Join(providerNames(cfg), ", "))

	if len(snap.Days) == 0 {
		fmt.Println("\nno counters yet")
		return nil
	}
	fmt.Println()
	fmt.Printf("%-12s %8s %8s\n", "DAY", "OK", "REFUSED")
	for _, d := range snap.Days {
		fmt.Printf("%-12s %8d %8d\n", d.Day, d.OK, d.Refused)
	}
	return nil
}

// cmdStats shows the counters by day, broken down by model.
func cmdStats(args []string) error {
	cfg, f, err := loadConfig(args)
	if err != nil || cfg == nil {
		return err
	}

	st, openErr := state.Open(cfg.Paths.StateFile)
	if openErr != nil && st == nil {
		return openErr
	}
	snap := state.Snapshot{}
	if st != nil {
		snap = st.Snapshot()
	}

	if f.json {
		return printJSON(snap)
	}
	if len(snap.Days) == 0 {
		fmt.Println("no counters yet")
		return nil
	}

	for _, d := range snap.Days {
		fmt.Printf("%s: ok %d, refused %d\n", d.Day, d.OK, d.Refused)
		if len(d.ByModel) == 0 {
			continue
		}
		fmt.Printf("  %-40s %8s %8s\n", "MODEL", "OK", "REFUSED")
		for _, m := range d.ByModel {
			fmt.Printf("  %-40s %8d %8d\n", m.Model, m.OK, m.Refused)
		}
	}
	return nil
}

// cmdKeys shows the provider keys.
//
// FINGERPRINTS are shown, not keys: the point is to learn how many there are
// and which are in use, not to see the secret.
func cmdKeys(args []string) error {
	cfg, f, err := loadConfig(args)
	if err != nil || cfg == nil {
		return err
	}

	type providerKeys struct {
		Provider   string   `json:"provider"`
		Keys       []string `json:"keys"`
		Count      int      `json:"count"`
		Configured bool     `json:"configured"`
	}
	var out []providerKeys

	for _, id := range providerNames(cfg) {
		pc := cfg.Providers[id]
		keys := keysFromConfig(pc)
		fps := make([]string, 0, len(keys))
		for _, k := range keys {
			if s, ok := k.(string); ok && s != "" {
				fps = append(fps, fingerprint(s))
			}
		}
		out = append(out, providerKeys{
			Provider:   id,
			Keys:       fps,
			Count:      len(fps),
			Configured: pc.ProviderKey() != "",
		})
	}

	if f.json {
		return printJSON(map[string]any{"providers": out})
	}
	if len(out) == 0 {
		fmt.Println("no providers configured")
		return nil
	}
	for _, p := range out {
		switch {
		case p.Count == 0:
			fmt.Printf("%s: no keys (anonymous operation)\n", p.Provider)
		default:
			fmt.Printf("%s: %d key(s): %s\n", p.Provider, p.Count, strings.Join(p.Keys, ", "))
		}
	}
	return nil
}

// cmdOptions shows the available vocabulary values.
func cmdOptions(args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	if f == nil {
		return nil
	}

	opts := map[string][]string{
		"modes":      {config.ModeLocal, config.ModeServer},
		"toggle":     {"on", "off"},
		"scopes":     {"logs", "stats", "all"},
		"transports": {"direct"},
	}
	if f.json {
		return printJSON(opts)
	}
	keys := make([]string, 0, len(opts))
	for k := range opts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("%-12s %s\n", k, strings.Join(opts[k], " | "))
	}
	return nil
}

// providerNames returns the provider identifiers in a stable order.
func providerNames(cfg *config.Config) []string {
	out := make([]string, 0, len(cfg.Providers))
	for id, p := range cfg.Providers {
		if p.IsEnabled() {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}
