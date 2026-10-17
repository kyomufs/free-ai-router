package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"free-ai-router/internal/config"
	"free-ai-router/internal/state"
)

// Команды чтения работают ЛОКАЛЬНО — с конфигурацией, состоянием и журналом.
//
// Это осознанное решение: они должны отвечать даже когда демон лежит. Оператор,
// у которого демон не поднялся, идёт смотреть счётчики и журнал первым делом, и
// команда, требующая живого демона, была бы бесполезна ровно тогда, когда
// нужнее всего.

// loadConfig читает конфигурацию и разбирает флаги.
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

// cmdConfig показывает эффективную конфигурацию.
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

// cmdStatus показывает состояние.
//
// Читается С ДИСКА, а не с демона: команда должна отвечать и когда демон
// остановлен. Живой демон обслуживает ту же информацию богаче — через
// поверхность управления, — но требовать его здесь нельзя.
func cmdStatus(args []string) error {
	cfg, f, err := loadConfig(args)
	if err != nil || cfg == nil {
		return err
	}

	st, openErr := state.Open(cfg.Paths.StateFile)
	if openErr != nil {
		fmt.Fprintf(os.Stderr, "предупреждение: состояние: %v\n", openErr)
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

	fmt.Printf("режим      %s\n", cfg.Mode.Val)
	fmt.Printf("адрес      %s\n", cfg.Listen.Val)
	fmt.Printf("транспорт  %s\n", cfg.Transport.Val)
	fmt.Printf("провайдеры %s\n", strings.Join(providerNames(cfg), ", "))

	if len(snap.Days) == 0 {
		fmt.Println("\nсчётчиков пока нет")
		return nil
	}
	fmt.Println()
	fmt.Printf("%-12s %8s %8s\n", "ДЕНЬ", "УСПЕХ", "ОТКАЗ")
	for _, d := range snap.Days {
		fmt.Printf("%-12s %8d %8d\n", d.Day, d.OK, d.Refused)
	}
	return nil
}

// cmdStats показывает счётчики по дням с разбивкой по моделям.
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
		fmt.Println("счётчиков пока нет")
		return nil
	}

	for _, d := range snap.Days {
		fmt.Printf("%s: успех %d, отказ %d\n", d.Day, d.OK, d.Refused)
		if len(d.ByModel) == 0 {
			continue
		}
		fmt.Printf("  %-40s %8s %8s\n", "МОДЕЛЬ", "УСПЕХ", "ОТКАЗ")
		for _, m := range d.ByModel {
			fmt.Printf("  %-40s %8d %8d\n", m.Model, m.OK, m.Refused)
		}
	}
	return nil
}

// cmdKeys показывает ключи провайдеров.
//
// Показываются ОТПЕЧАТКИ, а не ключи: нужно понять, сколько их и какие в
// работе, а не увидеть секрет.
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
		fmt.Println("провайдеры не настроены")
		return nil
	}
	for _, p := range out {
		switch {
		case p.Count == 0:
			fmt.Printf("%s: ключей нет (анонимная работа)\n", p.Provider)
		default:
			fmt.Printf("%s: %d ключ(ей): %s\n", p.Provider, p.Count, strings.Join(p.Keys, ", "))
		}
	}
	return nil
}

// cmdOptions показывает доступные значения словарей.
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

// providerNames возвращает идентификаторы провайдеров в стабильном порядке.
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
