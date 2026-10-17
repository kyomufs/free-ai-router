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

// cmdTUI запускает терминальный интерфейс.
//
// Интерфейс читает данные тем же кодом, что и команды чтения: у каждого
// экрана один источник, и расхождение между тем, что показывает интерфейс, и
// тем, что печатает команда, невозможно по построению.
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
	// Полноэкранный режим НЕ включается: интерфейс сосуществует с терминалом,
	// и вывод остаётся пригодным для копирования.
	program := tea.NewProgram(model)
	_, err = program.Run()
	return err
}

// tuiSource отдаёт данные экранам интерфейса.
type tuiSource struct {
	cfg       *config.Config
	providers *provider.Registry
}

// newTUISource собирает источник данных.
func newTUISource(cfg *config.Config) (*tuiSource, error) {
	providers, err := catalog.Build(cfg)
	if err != nil {
		// Провайдеры могут быть не настроены — интерфейс всё равно должен
		// открыться: оператору нужно увидеть конфигурацию и счётчики, чтобы
		// понять, что именно не настроено.
		return &tuiSource{cfg: cfg}, nil
	}
	return &tuiSource{cfg: cfg, providers: providers}, nil
}

// Status реализует tui.DataSource.
func (s *tuiSource) Status() ([]tui.Section, error) {
	sections := []tui.Section{
		{
			Title: "демон",
			Rows: []tui.Row{
				{Name: "режим", Value: s.cfg.Mode.Val, Note: "источник: " + string(s.cfg.Mode.From)},
				{Name: "адрес", Value: s.cfg.Listen.Val},
				{Name: "транспорт", Value: s.cfg.Transport.Val},
			},
		},
	}

	// Провайдеры.
	var providerRows []tui.Row
	for _, id := range providerNames(s.cfg) {
		pc := s.cfg.Providers[id]
		keys := "нет"
		if pc.ProviderKey() != "" {
			keys = "есть"
		}
		providerRows = append(providerRows, tui.Row{
			Name:  id,
			Value: fmt.Sprintf("тип %s, моделей %d, ключ %s", pc.Type, len(pc.Models), keys),
		})
	}
	if len(providerRows) == 0 {
		providerRows = append(providerRows, tui.Row{
			Name:  "нет",
			Value: "провайдеры не настроены",
			Note:  "добавьте секцию providers в конфигурацию",
		})
	}
	sections = append(sections, tui.Section{Title: "провайдеры", Rows: providerRows})

	// Счётчики.
	st, err := state.Open(s.cfg.Paths.StateFile)
	if err != nil && st == nil {
		return sections, err
	}
	if st != nil {
		snap := st.Snapshot()
		var rows []tui.Row
		if len(snap.Days) == 0 {
			rows = append(rows, tui.Row{Name: "нет данных", Value: "запросов пока не было"})
		}
		for i, d := range snap.Days {
			if i >= 7 {
				break // показываем неделю: больше не помещается и не нужно
			}
			rows = append(rows, tui.Row{
				Name:  d.Day,
				Value: fmt.Sprintf("успех %d, отказ %d", d.OK, d.Refused),
			})
		}
		sections = append(sections, tui.Section{Title: "счётчики", Rows: rows})
	}

	return sections, nil
}

// Keys реализует tui.DataSource.
func (s *tuiSource) Keys() ([]tui.Section, error) {
	var rows []tui.Row
	for _, id := range providerNames(s.cfg) {
		pc := s.cfg.Providers[id]
		keys := keysFromConfig(pc)
		if len(keys) == 0 {
			rows = append(rows, tui.Row{
				Name:  id,
				Value: "ключей нет",
				Note:  "работает анонимно",
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
		rows = append(rows, tui.Row{Name: "нет", Value: "провайдеры не настроены"})
	}
	return []tui.Section{
		{
			Title: "ключи провайдеров",
			Rows:  rows,
		},
		{
			Title: "пояснение",
			Rows: []tui.Row{
				{Name: "", Value: "показаны отпечатки, а не ключи"},
				{Name: "", Value: "отпечаток достаточен, чтобы различить ключи"},
			},
		},
	}, nil
}

// Options реализует tui.DataSource.
//
// Строка про ротацию ключей показывается всегда, но при отсутствии ключей
// несёт пояснение, а не значение: настройка, которая ничего не переключает,
// читается как сломанная.
func (s *tuiSource) Options() ([]tui.Section, error) {
	var rows []tui.Row
	for _, id := range providerNames(s.cfg) {
		pc := s.cfg.Providers[id]
		if len(keysFromConfig(pc)) == 0 {
			rows = append(rows, tui.Row{
				Name:  "ротация ключей: " + id,
				Value: "недоступна",
				Note:  "у провайдера нет ключей",
			})
			continue
		}
		rows = append(rows, tui.Row{
			Name:  "ротация ключей: " + id,
			Value: "включена",
			Note:  "смена ключа при отказе",
		})
	}
	if len(rows) == 0 {
		rows = append(rows, tui.Row{Name: "нет", Value: "провайдеры не настроены"})
	}

	return []tui.Section{
		{Title: "настройки", Rows: rows},
		{
			Title: "что меняется",
			Rows: []tui.Row{
				{Name: "", Value: "состав ключей и их включённость"},
				{Name: "", Value: "остальное — из конфигурации, при запуске"},
			},
		},
	}, nil
}
