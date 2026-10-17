// Команда free-ai-router — шлюз-агрегатор бесплатных AI-провайдеров.
//
// ЧТО ДЕЛАЕТ ТОЧКА ВХОДА. Собирает работающий демон из частей: читает
// конфигурацию, строит реестр провайдеров, поднимает транспорт и механизмы,
// навешивает поверхности и запускает сервер. Вся логика живёт в пакетах; здесь
// только соединение и разбор командной строки.
//
// ТРИ УРОВНЯ НАСТРОЙКИ, в порядке старшинства:
//
//	флаги этой команды   побеждают всё
//	переменные окружения перекрывают файл
//	config.json          базовый слой
//
// ПЕРЕОПРЕДЕЛЕНИЕ ЧЕРЕЗ ФЛАГИ существует не для удобства, а потому что
// декларативная система не всегда под рукой: на NixOS конфигурацией владеет
// модуль, и смена настройки означала бы пересборку. Флаги дают тот же демон с
// непривычной настройкой, не трогая ни файл, ни модуль.
package main

import (
	"context"
	"fmt"
	"os"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "ошибка: %v\n", err)
		os.Exit(1)
	}
}

// run разбирает команду и выполняет её.
func run(args []string) error {
	if len(args) == 0 {
		usage()
		return fmt.Errorf("команда не указана")
	}

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "up", "serve", "start":
		return cmdUp(rest)
	case "status":
		return cmdStatus(rest)
	case "stop":
		return cmdStop(rest)
	case "tui":
		return cmdTUI(rest)
	case "models":
		return cmdModels(context.Background(), rest)
	case "stats":
		return cmdStats(rest)
	case "keys":
		return cmdKeys(rest)
	case "logs":
		return cmdLogs(rest)
	case "data":
		return cmdData(rest)
	case "config":
		return cmdConfig(rest)
	case "options":
		return cmdOptions(rest)
	case "version", "--version", "-v":
		fmt.Println(version())
		return nil
	case "help", "--help", "-h":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("неизвестная команда %q", cmd)
	}
}
