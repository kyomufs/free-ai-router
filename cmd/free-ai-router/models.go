package main

import (
	"context"
	"fmt"

	"free-ai-router/internal/catalog"
)

// cmdModels показывает каталог моделей.
//
// КАТАЛОГ СПРАШИВАЕТСЯ У СЕРВИСА, а не читается из конфигурации: списка
// моделей в конфигурации больше нет. Состав известен только сервису, и
// показать его может только запрос к нему.
//
// Демон для этого не нужен и намеренно не требуется: команда собирает
// провайдеров тем же кодом, что и демон, и спрашивает их напрямую. Требовать
// запущенный демон значило бы не дать посмотреть каталог до первого запуска —
// ровно тогда, когда он нужнее всего.
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
			// Провайдер, каталог которого не ответил, называется отдельно:
			// иначе пустой список выглядел бы как отсутствие моделей у
			// сервиса, а не как недоступность каталога.
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
		fmt.Printf("каталог недоступен — %s\n", r)
	}
	if len(rows) == 0 {
		if len(refused) == 0 {
			fmt.Println("ни один провайдер не объявил моделей")
		}
		return nil
	}

	fmt.Printf("%-10s %-38s %9s %8s %5s %5s\n", "ПРОВАЙДЕР", "МОДЕЛЬ", "ОКНО", "ОТВЕТ", "ФОТО", "ДУМАЕТ")
	for _, r := range rows {
		fmt.Printf("%-10s %-38s %9d %8d %5s %5s\n",
			r.Provider, r.ID, r.Context, r.Output, yesNo(r.Vision), yesNo(r.Reasoning))
	}
	fmt.Printf("\nвсего моделей: %d\n", len(rows))
	return nil
}

// yesNo переводит признак в короткую пометку для таблицы.
func yesNo(v bool) string {
	if v {
		return "да"
	}
	return "—"
}
