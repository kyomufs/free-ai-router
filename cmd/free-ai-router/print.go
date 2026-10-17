package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"free-ai-router/internal/config"
)

// printEffectiveConfig печатает результат слияния трёх уровней.
//
// ЗАЧЕМ ЭТО НУЖНО. Когда настройка приходит из трёх источников, понять, чей
// верх, можно только увидев источник рядом со значением. Без этого отладка
// слияния превращается в угадывание: оператор меняет переменную, а значение не
// двигается, потому что его перекрывает флаг.
//
// Вывод идёт в порядке осей, а не в порядке полей структуры: строки должны
// читаться в том же порядке, в каком о осях говорится в справке.
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
		fmt.Println("провайдеры:")
		ids := make([]string, 0, len(cfg.Providers))
		for id := range cfg.Providers {
			ids = append(ids, id)
		}
		sort.Strings(ids)

		for _, id := range ids {
			p := cfg.Providers[id]
			state := "включён"
			if !p.IsEnabled() {
				state = "выключен"
			}
			key := "нет"
			if p.ProviderKey() != "" {
				key = "есть"
			}
			fmt.Printf("  %-20s тип=%-10s %s, ключ=%s, моделей=%d\n",
				id, p.Type, state, key, len(p.Models))
		}
	}

	fmt.Println()
	fmt.Printf("файл:     %s\n", cfg.Paths.ConfigFile)
	fmt.Printf("состояние: %s\n", cfg.Paths.StateFile)
	fmt.Printf("журнал:   %s\n", cfg.Paths.LogFile)
	return nil
}

// maskSecret скрывает значение секрета, оставляя признак его наличия.
//
// Токен не печатается: вывод попадает в терминал, в историю оболочки и в
// переписку при отладке. Знать нужно, задан ли он, а не каков он.
func maskSecret(v string) string {
	if strings.TrimSpace(v) == "" {
		return ""
	}
	return "задан"
}

// printJSON печатает значение как JSON.
func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// usage печатает справку.
func usage() {
	fmt.Fprint(os.Stderr, `free-ai-router — шлюз-агрегатор бесплатных AI-провайдеров

Использование:
  free-ai-router up       [флаги]     запустить демон
  free-ai-router status   [--json]    состояние демона
  free-ai-router stop                 остановить демон
  free-ai-router tui                  терминальный интерфейс
  free-ai-router models   [--json]    каталог моделей
  free-ai-router stats    [--json]    счётчики по дням
  free-ai-router keys     [--json]    ключи провайдеров
  free-ai-router logs     [-n N]      журнал
  free-ai-router data     [clear]     что лежит на диске, и очистка
  free-ai-router config   [--print]   эффективная конфигурация
  free-ai-router options  [--json]    доступные значения
  free-ai-router version              версия

Флаги запуска:
  --mode        local | server        форма развёртывания
  --listen      АДРЕС                 адрес прослушивания
  --token       ЗНАЧЕНИЕ              токен для режима server
  --transport   direct                транспорт
  --upstream    АДРЕС                 адрес по умолчанию для провайдеров
  --print-config                      показать конфигурацию и выйти
  --detach                            запустить в фоне

Настройка собирается из трёх уровней, старшие перекрывают младшие:
  флаги  →  переменные окружения  →  config.json
`)
}
