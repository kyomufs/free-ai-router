package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"free-ai-router/internal/config"
	"free-ai-router/internal/state"
)

// cmdStop останавливает демон.
//
// Реализовано через сигнал процессу: демон ловит SIGTERM и завершается
// корректно, дожидаясь окончания текущих запросов. Отдельного канала
// управления для остановки нет — сигнал для этого и предназначен, и он
// работает, даже если поверхность управления недоступна.
func cmdStop(args []string) error {
	cfg, _, err := loadConfig(args)
	if err != nil || cfg == nil {
		return err
	}

	pid, pidErr := findDaemonPID()
	if pidErr != nil || pid == 0 {
		return fmt.Errorf("демон не найден: нечего останавливать")
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("процесс %d: %w", pid, err)
	}
	if err := proc.Signal(os.Interrupt); err != nil {
		return fmt.Errorf("сигнал процессу %d: %w", pid, err)
	}
	fmt.Printf("остановлен процесс %d\n", pid)
	return nil
}

// findDaemonPID ищет работающий демон.
//
// Пока возвращает ноль: поиск процесса требует места, куда демон пишет свой
// идентификатор, а это часть управления жизненным циклом, которая появится
// вместе с фоновым запуском. Ноль означает «не найден», и вызывающий говорит
// об этом прямо, а не делает вид, что остановил.
func findDaemonPID() (int, error) { return 0, nil }

// cmdLogs печатает журнал.
func cmdLogs(args []string) error {
	cfg, err := loadConfigSimple(args)
	if err != nil || cfg == nil {
		return err
	}

	count := 50
	for i, a := range args {
		if a == "-n" && i+1 < len(args) {
			if n, convErr := strconv.Atoi(args[i+1]); convErr == nil && n > 0 {
				count = n
			}
		}
		if strings.HasPrefix(a, "-n=") {
			if n, convErr := strconv.Atoi(strings.TrimPrefix(a, "-n=")); convErr == nil && n > 0 {
				count = n
			}
		}
	}

	lines, err := state.Tail(cfg.Paths.LogFile, count)
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		fmt.Println("журнал пуст")
		return nil
	}
	for _, line := range lines {
		fmt.Println(line)
	}
	return nil
}

// cmdData показывает след на диске и умеет его чистить.
func cmdData(args []string) error {
	cfg, f, err := loadConfig(args)
	if err != nil || cfg == nil {
		return err
	}

	// Разбор подкоманды: первое слово после флагов.
	var action, scope string
	for _, a := range f.positional {
		switch {
		case action == "":
			action = a
		case scope == "":
			scope = a
		}
	}

	if action == "clear" {
		return dataClear(cfg, scope)
	}

	// Показ без аргументов: что лежит и сколько занимает.
	fmt.Printf("%-12s %-45s %10s\n", "ЧТО", "ГДЕ", "РАЗМЕР")
	fmt.Printf("%-12s %-45s %10s\n", "конфигурация", cfg.Paths.ConfigFile, humanSize(fileSize(cfg.Paths.ConfigFile)))
	fmt.Printf("%-12s %-45s %10s\n", "состояние", cfg.Paths.StateFile, humanSize(fileSize(cfg.Paths.StateFile)))

	logBytes, logFiles, _ := state.LogFileSize(cfg.Paths.LogFile)
	fmt.Printf("%-12s %-45s %10s", "журнал", cfg.Paths.LogFile, humanSize(logBytes))
	if logFiles > 1 {
		fmt.Printf(" (%d файлов)", logFiles)
	}
	fmt.Println()

	fmt.Println()
	fmt.Println("очистка: data clear logs | stats | all")
	fmt.Println("all не трогает конфигурацию: удаление файла, описывающего")
	fmt.Println("устройство демона, это не обслуживание, а потеря развёртывания.")
	return nil
}

// dataClear очищает выбранную область.
//
// Конфигурация НЕ входит ни в одну область, включая all. Удаление файла,
// описывающего устройство демона, — не обслуживание, а потеря развёртывания: из
// него видно, какие провайдеры и ключи были настроены, и восстановить это по
// памяти нельзя.
func dataClear(cfg *config.Config, scope string) error {
	switch scope {
	case "logs":
		if err := state.Truncate(cfg.Paths.LogFile); err != nil {
			return err
		}
		fmt.Println("журнал очищен")
		return nil

	case "stats":
		st, err := state.Open(cfg.Paths.StateFile)
		if err != nil && st == nil {
			return err
		}
		if st != nil {
			if err := st.Clear(); err != nil {
				return err
			}
		}
		fmt.Println("счётчики очищены")
		return nil

	case "all":
		if err := state.Truncate(cfg.Paths.LogFile); err != nil {
			return err
		}
		st, err := state.Open(cfg.Paths.StateFile)
		if err != nil && st == nil {
			return err
		}
		if st != nil {
			if err := st.Clear(); err != nil {
				return err
			}
		}
		fmt.Println("журнал и счётчики очищены")
		fmt.Printf("конфигурация не тронута: %s\n", cfg.Paths.ConfigFile)
		return nil

	case "":
		return fmt.Errorf("не указана область: logs | stats | all")

	default:
		return fmt.Errorf("неизвестная область %q (logs | stats | all)", scope)
	}
}

// fileSize возвращает размер файла, ноль если его нет.
func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// humanSize печатает размер в удобных единицах.
//
// Единицы двоичные: размер файла на диске измеряется ими, и смешивать с
// десятичными нельзя — оператор сравнивает вывод с тем, что показывает система.
func humanSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d Б", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cиБ", float64(bytes)/float64(div), "КМГТ"[exp])
}
