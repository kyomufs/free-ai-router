package main

import (
	"context"
	"fmt"

	"free-ai-router/internal/catalog"
)

// cmdModels shows the catalog of models.
//
// THE CATALOG IS QUERIED FROM THE SERVICE, not read from the configuration: the
// list of models no longer exists in the configuration. The composition is known
// only to the service, and only a request to it can show it.
//
// A daemon is not needed for this and is deliberately not required: the command
// assembles the providers with the same code as the daemon and asks them
// directly. Requiring a running daemon would mean not letting the catalog be
// viewed before the first start — exactly when it is needed most.
func cmdModels(ctx context.Context, args []string) error {
	cfg, f, err := loadConfig(args)
	if err != nil || cfg == nil {
		return err
	}

	reg, err := catalog.Build(cfg)
	if err != nil {
		return err
	}

	type row struct {
		Provider  string `json:"provider"`
		ID        string `json:"id"`
		Name      string `json:"name"`
		Context   int    `json:"context"`
		Output    int    `json:"output"`
		Vision    bool   `json:"vision"`
		Reasoning bool   `json:"reasoning"`
	}

	var rows []row
	var refused []string

	for _, p := range reg.All() {
		models, err := p.Models(ctx)
		if err != nil {
			// A provider whose catalog did not answer is named separately:
			// otherwise an empty list would look like the service having no
			// models rather than the catalog being unavailable.
			refused = append(refused, fmt.Sprintf("%s: %v", p.ID(), err))
			continue
		}
		for _, m := range models {
			rows = append(rows, row{
				Provider:  string(p.ID()),
				ID:        m.ID,
				Name:      m.Name,
				Context:   m.Context,
				Output:    m.MaxOutput,
				Vision:    m.Vision,
				Reasoning: m.Reasoning,
			})
		}
	}

	if f.json {
		return printJSON(map[string]any{"models": rows, "unavailable": refused})
	}

	for _, r := range refused {
		fmt.Printf("catalog unavailable — %s\n", r)
	}
	if len(rows) == 0 {
		if len(refused) == 0 {
			fmt.Println("no provider declared any models")
		}
		return nil
	}

	fmt.Printf("%-10s %-38s %9s %8s %5s %5s\n", "PROVIDER", "MODEL", "CONTEXT", "OUTPUT", "VISION", "REASONING")
	for _, r := range rows {
		fmt.Printf("%-10s %-38s %9d %8d %5s %5s\n",
			r.Provider, r.ID, r.Context, r.Output, yesNo(r.Vision), yesNo(r.Reasoning))
	}
	fmt.Printf("\ntotal models: %d\n", len(rows))
	return nil
}

// yesNo turns a flag into a short mark for the table.
func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "—"
}
