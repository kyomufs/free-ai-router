package main

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"free-ai-router/internal/config"
)

// flags — разобранные аргументы командной строки.
//
// Значения по умолчанию — пустые строки, а не значения из конфигурации.
// Пустая строка означает «флаг не задан», и это важно: иначе запуск без флагов
// затирал бы конфигурацию умолчаниями.
type flags struct {
	mode      string
	listen    string
	token     string
	transport string
	upstream  string

	// json включает машиночитаемый вывод.
	json bool
	// printConfig печатает эффективную конфигурацию и выходит.
	printConfig bool
	// detach запускает демон в фоне.
	detach bool

	// Позиционные аргументы, оставшиеся после разбора.
	positional []string
}

// parseFlags разбирает аргументы.
//
// Разбор идёт вручную, а не через flag.Parse: командам нужен РАЗНЫЙ набор
// флагов, и общий набор заставлял бы принимать то, что команде не нужно.
func parseFlags(args []string) (*flags, error) {
	f := &flags{}
	fs := flag.NewFlagSet("free-ai-router", flag.ContinueOnError)
	// Ошибки печатаем сами: у flag своё сообщение, и оно дублировало бы наше.
	fs.SetOutput(io.Discard)

	fs.StringVar(&f.mode, "mode", "", "форма развёртывания: local | server")
	fs.StringVar(&f.listen, "listen", "", "адрес прослушивания")
	fs.StringVar(&f.token, "token", "", "токен доступа для режима server")
	fs.StringVar(&f.transport, "transport", "", "транспорт: direct")
	fs.StringVar(&f.upstream, "upstream", "", "адрес по умолчанию для провайдеров")
	fs.BoolVar(&f.json, "json", false, "машиночитаемый вывод")
	fs.BoolVar(&f.printConfig, "print-config", false, "показать эффективную конфигурацию и выйти")
	fs.BoolVar(&f.detach, "detach", false, "запустить демон в фоне")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil, nil
		}
		return nil, fmt.Errorf("разбор аргументов: %w", err)
	}
	f.positional = fs.Args()
	return f, nil
}

// applyToFlags накладывает разобранные флаги на конфигурацию.
func (f *flags) applyToConfig(c interface {
	ApplyFlags(mode, listen, token, transport, upstream string)
}) {
	c.ApplyFlags(f.mode, f.listen, f.token, f.transport, f.upstream)
}

// joinArgs собирает описание оставшихся аргументов для сообщения об ошибке.
func joinArgs(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return strings.Join(args, " ")
}

// loadConfigSimple читает конфигурацию с флагами, но без их разбора в
// переменную: командам, которым флаги нужны лишь как переопределение, не
// требуется доступ к самим значениям.
func loadConfigSimple(args []string) (*config.Config, error) {
	cfg, _, err := loadConfig(args)
	return cfg, err
}
