package main

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"free-ai-router/internal/catalog"
	"free-ai-router/internal/config"
	"free-ai-router/internal/provider"
	"free-ai-router/internal/state"
	"free-ai-router/internal/tui"
)

// cmdTUI starts the terminal interface.
//
// The interface reads data with the same code as the read commands: each screen
// has one source, and a divergence between what the interface shows and what the
// command prints is impossible by construction.
func cmdTUI(args []string) error {
	cfg, err := loadConfigSimple(args)
	if err != nil || cfg == nil {
		return err
	}

	source, err := newTUISource(cfg)
	if err != nil {
		return err
	}

	model := tui.New(source)
	// Full-screen mode is NOT enabled: the interface coexists with the terminal,
	// and the output stays fit for copying.
	program := tea.NewProgram(model)
	_, err = program.Run()
	return err
}

// tuiSource serves data to the interface screens.
type tuiSource struct {
	cfg       *config.Config
	providers *provider.Registry
}

// newTUISource assembles the data source.
func newTUISource(cfg *config.Config) (*tuiSource, error) {
	providers, err := catalog.Build(cfg)
	if err != nil {
		// The providers may not be configured — the interface must still
		// open: the operator needs to see the configuration and the counters
		// to understand what exactly is not configured.
		return &tuiSource{cfg: cfg}, nil
	}
	return &tuiSource{cfg: cfg, providers: providers}, nil
}

// Status implements tui.DataSource.
func (s *tuiSource) Status() ([]tui.Section, error) {
	sections := []tui.Section{
		{
			Title: "daemon",
			Rows: []tui.Row{
				{Name: "mode", Value: s.cfg.Mode.Val, Note: "source: " + string(s.cfg.Mode.From)},
				{Name: "address", Value: s.cfg.Listen.Val},
				{Name: "transport", Value: s.cfg.Transport.Val},
			},
		},
	}

	// Providers.
	var providerRows []tui.Row
	for _, id := range providerNames(s.cfg) {
		pc := s.cfg.Providers[id]
		keys := "none"
		if pc.ProviderKey() != "" {
			keys = "present"
		}
		providerRows = append(providerRows, tui.Row{
			Name: id,
			// The number of models is not shown in the overview row: the
			// catalog lives with the service and is learned through a request.
			// A request to render the list would be a network call, and the
			// settings overview is obliged to answer instantly.
			Value: fmt.Sprintf("type %s, key %s", pc.Type, keys),
		})
	}
	if len(providerRows) == 0 {
		providerRows = append(providerRows, tui.Row{
			Name:  "none",
			Value: "no providers configured",
			Note:  "add a providers section to the configuration",
		})
	}
	sections = append(sections, tui.Section{Title: "providers", Rows: providerRows})

	// Counters.
	st, err := state.Open(s.cfg.Paths.StateFile)
	if err != nil && st == nil {
		return sections, err
	}
	if st != nil {
		snap := st.Snapshot()
		var rows []tui.Row
		if len(snap.Days) == 0 {
			rows = append(rows, tui.Row{Name: "no data", Value: "there have been no requests yet"})
		}
		for i, d := range snap.Days {
			if i >= 7 {
				break // we show a week: more does not fit and is not needed
			}
			rows = append(rows, tui.Row{
				Name:  d.Day,
				Value: fmt.Sprintf("ok %d, refused %d", d.OK, d.Refused),
			})
		}
		sections = append(sections, tui.Section{Title: "counters", Rows: rows})
	}

	return sections, nil
}

// Keys implements tui.DataSource.
func (s *tuiSource) Keys() ([]tui.Section, error) {
	var rows []tui.Row
	for _, id := range providerNames(s.cfg) {
		pc := s.cfg.Providers[id]
		keys := keysFromConfig(pc)
		if len(keys) == 0 {
			rows = append(rows, tui.Row{
				Name:  id,
				Value: "no keys",
				Note:  "works anonymously",
			})
			continue
		}
		for _, k := range keys {
			fp := ""
			if str, ok := k.(string); ok {
				fp = fingerprint(str)
			}
			rows = append(rows, tui.Row{Name: id, Value: fp})
		}
	}
	if len(rows) == 0 {
		rows = append(rows, tui.Row{Name: "none", Value: "no providers configured"})
	}
	return []tui.Section{
		{
			Title: "provider keys",
			Rows:  rows,
		},
		{
			Title: "explanation",
			Rows: []tui.Row{
				{Name: "", Value: "fingerprints are shown, not keys"},
				{Name: "", Value: "a fingerprint is enough to tell keys apart"},
			},
		},
	}, nil
}

// Options implements tui.DataSource.
//
// The row about key rotation is always shown, but when there are no keys it
// carries an explanation rather than a value: a setting that toggles nothing
// reads as broken.
func (s *tuiSource) Options() ([]tui.Section, error) {
	var rows []tui.Row
	for _, id := range providerNames(s.cfg) {
		pc := s.cfg.Providers[id]
		if len(keysFromConfig(pc)) == 0 {
			rows = append(rows, tui.Row{
				Name:  "key rotation: " + id,
				Value: "unavailable",
				Note:  "the provider has no keys",
			})
			continue
		}
		rows = append(rows, tui.Row{
			Name:  "key rotation: " + id,
			Value: "enabled",
			Note:  "key change on refusal",
		})
	}
	if len(rows) == 0 {
		rows = append(rows, tui.Row{Name: "none", Value: "no providers configured"})
	}

	return []tui.Section{
		{Title: "settings", Rows: rows},
		{
			Title: "what changes",
			Rows: []tui.Row{
				{Name: "", Value: "the set of keys and their enabled state"},
				{Name: "", Value: "the rest — from the configuration, at startup"},
			},
		},
	}, nil
}
