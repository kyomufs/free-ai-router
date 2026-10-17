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
//
// THE PATHS ARE RESOLVED FIRST. A path override decides WHICH file is read, so
// it cannot be layered on afterwards: a value applied after the read would have
// come from the wrong file, and nothing would say so. Hence the order — resolve
// the two paths, read, and only then layer the ordinary values.
func loadConfig(args []string) (*config.Config, *flags, error) {
	f, err := parseFlags(args)
	if err != nil {
		return nil, nil, err
	}
	if f == nil {
		return nil, nil, nil
	}

	cfg, err := loadConfigFor(f)
	if err != nil {
		return nil, nil, err
	}
	return cfg, f, nil
}

// resolvePaths turns the flags and the environment into the two paths.
//
// The environment first, the flags over it: the two move the same paths, and a
// flag given on the command line is the more immediate instruction.
func resolvePaths(f *flags) (config.Paths, config.PathOverrides, error) {
	paths, err := config.DefaultPaths()
	if err != nil {
		return config.Paths{}, config.PathOverrides{}, err
	}

	ov := config.EnvPathOverrides()
	if f.configPath != "" {
		ov.ConfigFile = f.configPath
	}
	if f.statePath != "" {
		ov.StateFile = f.statePath
	}
	return paths, ov, nil
}

// loadConfigFor reads and checks the configuration for already-parsed flags: the
// result is what the daemon would run with.
func loadConfigFor(f *flags) (*config.Config, error) {
	cfg, err := loadConfigUnchecked(f)
	if err != nil {
		return nil, err
	}
	// Checked once, at the end, over the MERGED result: the file alone is not
	// what runs, and a value from a flag was never in the file to be checked
	// there.
	return cfg, cfg.Validate()
}

// loadConfigUnchecked reads the configuration and applies the flags, WITHOUT
// checking that the daemon could start with the result.
//
// For the commands that edit the file. They change one key, and a configuration
// is allowed to be incomplete while it is being assembled.
func loadConfigUnchecked(f *flags) (*config.Config, error) {
	paths, ov, err := resolvePaths(f)
	if err != nil {
		return nil, err
	}

	cfg, err := config.LoadUnchecked(config.ResolvePaths(paths, ov))
	if err != nil {
		return nil, err
	}
	if err := cfg.ApplyFlags(f.values()); err != nil {
		return nil, err
	}
	return cfg, nil
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
			"credential":             cfg.Credential.Val,
			"credential_source":      cfg.Credential.From,
			"key_rotation":           cfg.KeyRotation.Val,
			"key_rotation_source":    cfg.KeyRotation.From,
			"family":                 cfg.Family.Val,
			"family_source":          cfg.Family.From,
			"first_event_timeout":    cfg.FirstEventTimeout.Val.String(),
			"idle_timeout":           cfg.IdleTimeout.Val.String(),
			"reasoning_idle_timeout": cfg.ReasoningIdleTimeout.Val.String(),
			"config_file":            cfg.Paths.ConfigFile,
			"state_file":             cfg.Paths.StateFile,
			"log_file":               cfg.Paths.LogFile,
			"providers":              providersDetail(cfg),
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

// providersDetail describes the configured providers for machine consumption.
//
// The effective axes are included next to the declared ones: a consumer asking
// which credential a provider uses needs the answer in force, not the raw entry,
// and computing the inheritance is the daemon's job rather than the caller's.
//
// A provider is described twice over, and the two are different questions:
// `has_key` says whether the section supplies one, `key_used` says whether it is
// presented. Under credential "anonymous" a configured pool stays unused on
// purpose, and a consumer reading only the first field would report a mode the
// requests do not follow.
func providersDetail(cfg *config.Config) []map[string]any {
	ids := providerNames(cfg)
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		p := cfg.Providers[id]
		credential := cfg.CredentialFor(id)
		out = append(out, map[string]any{
			"id":                   id,
			"type":                 p.Type,
			"credential":           p.Credential,
			"credential_effective": credential,
			"key_rotation":         p.KeyRotation,
			"rotation_effective":   cfg.RotationFor(id),
			"rotates":              cfg.RotatesFor(id),
			"base_url":             p.BaseURL,
			"has_key":              p.HasKeys(),
			"key_used":             p.HasKeys() && credential == config.CredentialAccount,
		})
	}
	return out
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
