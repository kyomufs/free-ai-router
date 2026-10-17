package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"free-ai-router/internal/config"
)

// printEffectiveConfig prints the result of merging the three levels.
//
// WHY THIS IS NEEDED. When a setting comes from three sources, the only way to
// understand which one wins is to see the source next to the value. Without it,
// debugging the merge turns into guesswork: the operator changes a variable, and
// the value does not move because a flag overrides it.
//
// The output follows the order of the axes, not the order of the struct fields:
// the lines should read in the same order in which the axes are described in the
// help.
func printEffectiveConfig(cfg *config.Config) error {
	rows := []struct {
		key   string
		value string
		from  config.Source
	}{
		{"mode", cfg.Mode.Val, cfg.Mode.From},
		{"listen", cfg.Listen.Val, cfg.Listen.From},
		{"token", maskSecret(cfg.Token.Val), cfg.Token.From},
		{"transport", cfg.Transport.Val, cfg.Transport.From},
		{"upstream", cfg.Upstream.Val, cfg.Upstream.From},
		{"family", cfg.Family.Val, cfg.Family.From},
		{"credential", cfg.Credential.Val, cfg.Credential.From},
		{"key_rotation", cfg.KeyRotation.Val, cfg.KeyRotation.From},
		{"first_event_timeout", cfg.FirstEventTimeout.Val.String(), cfg.FirstEventTimeout.From},
		{"idle_timeout", cfg.IdleTimeout.Val.String(), cfg.IdleTimeout.From},
		{"reasoning_idle_timeout", cfg.ReasoningIdleTimeout.Val.String(), cfg.ReasoningIdleTimeout.From},
	}

	width := 0
	for _, r := range rows {
		if len(r.key) > width {
			width = len(r.key)
		}
	}
	for _, r := range rows {
		fmt.Printf("%-*s  %-28s (%s)\n", width, r.key, r.value, r.from)
	}

	if len(cfg.Providers) > 0 {
		fmt.Println()
		fmt.Println("providers:")
		ids := make([]string, 0, len(cfg.Providers))
		for id := range cfg.Providers {
			ids = append(ids, id)
		}
		sort.Strings(ids)

		for _, id := range ids {
			p := cfg.Providers[id]
			state := "enabled"
			if !p.IsEnabled() {
				state = "disabled"
			}
			key := "none"
			if p.ProviderKey() != "" {
				key = "present"
			}
			// The effective axes are shown next to the declared ones: an
			// inherited value is invisible otherwise, and the question asked of
			// this output is what is in force, not what was written down.
			cred := cfg.CredentialFor(id)
			if p.Credential != "" {
				cred += "*"
			}
			rotation := cfg.RotationFor(id)
			if cfg.RotatesFor(id) {
				rotation += " (active)"
			}
			// The number of models is not shown: the catalog lives with the
			// service and is learned through a request, not from the
			// configuration. Here, while the settings are being parsed, nobody
			// has asked for it yet — and showing zero would mean reporting that
			// there are no models.
			fmt.Printf("  %-20s type=%-10s %s, key=%s, credential=%s, rotation=%s\n",
				id, p.Type, state, key, cred, rotation)
		}
	}

	fmt.Println()
	fmt.Printf("file:      %s\n", cfg.Paths.ConfigFile)
	fmt.Printf("state:     %s\n", cfg.Paths.StateFile)
	fmt.Printf("log:       %s\n", cfg.Paths.LogFile)
	return nil
}

// maskSecret hides the value of the secret while leaving the mark of its
// presence.
//
// The token is not printed: the output lands in the terminal, in the shell
// history and in correspondence during debugging. What needs to be known is
// whether it is set, not what it is.
func maskSecret(v string) string {
	if strings.TrimSpace(v) == "" {
		return ""
	}
	return "set"
}

// printJSON prints the value as JSON.
func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// usage prints the help.
func usage() {
	fmt.Fprint(os.Stderr, `free-ai-router — a gateway that aggregates free AI providers

Usage:
  free-ai-router up       [flags]     start the daemon
  free-ai-router status   [--json]    daemon state
  free-ai-router stop                 stop the daemon
  free-ai-router tui                  terminal interface
  free-ai-router models   [--json]    catalog of models
  free-ai-router stats    [--json]    counters by day
  free-ai-router keys     [--json]    provider keys
  free-ai-router logs     [-n N]      log
  free-ai-router data     [clear]     what lies on disk, and clearing
  free-ai-router config   [--json]    effective configuration
  free-ai-router axes     [--json]    every setting, its values and its defaults
  free-ai-router systemd  [--install] the systemd user unit, printed or written
  free-ai-router version              version

One axis at a time, kept in the file:
  free-ai-router mode      [local|server]      deployment form
  free-ai-router cred      [anonymous|account] what is presented to the provider
  free-ai-router rotate    [on|off]            spend the pool on a refusal
  free-ai-router family    [auto|v4|v6]        address family the traffic leaves from
  free-ai-router <axis>                        without a value: what is in force, and from where

Startup flags:
  --mode                  local | server      deployment form
  --listen                ADDRESS             listen address
  --token                 VALUE               token for server mode
  --transport             direct              transport
  --upstream              ADDRESS             default address for providers
  --family                auto | v4 | v6      address family
  --credential            anonymous | account  what is presented to the provider
  --rotate                on | off            spend the pool on a refusal naming the key
  --first-event-timeout   DURATION            wait for the first event
  --idle-timeout          DURATION            allowed silence between events
  --reasoning-idle-timeout DURATION           silence allowed while reasoning
  --config                PATH                configuration file to read
  --state                 PATH                state file to write
  --print-config                              show the configuration and exit
  --detach                                    start in the background
  --json                                      machine-readable output

Settings are assembled from three levels, the higher overriding the lower:
  flags  →  environment variables  →  config.json

Every setting also has an environment variable, printed by `+"`axes`"+`.
The one-axis commands write config.json and survive a restart; on NixOS that file
belongs to the module and is rewritten on every activation, so a change made that
way lasts until the next switch — pass a flag instead.
`)
}
