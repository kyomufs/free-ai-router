package state

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Журнал — единственное место, куда пишет демон.
//
// ЗАЧЕМ ОДИН. Раньше записи о жизни демона и записи о запросах жили в разных
// местах, и восстановить картину по ним было нельзя: половина контекста
// терялась при переходе от одного файла к другому. Здесь оба вида записей
// идут в один поток, в порядке появления, и потому читаются как рассказ.
//
// РОТАЦИЯ делается НА СТАРТЕ, а не на каждой записи. Это единственный момент,
// когда файл можно переместить, не гоняя наперегонки с собственным писателем:
// обработчики запросов пишут в журнал параллельно, и переименование под ними
// потеряло бы строки.

// Ограничения журнала.
const (
	// rotateAt — порог, после которого журнал пролистывается.
	rotateAt = 8 << 20 // 8 MiB
	// keepGenerations — сколько прошлых файлов хранится.
	keepGenerations = 3
)

// Logger — журнал демона.
type Logger struct {
	mu   sync.Mutex
	path string
	file *os.File
}

// OpenLogger открывает журнал по указанному пути.
//
// Ротация выполняется здесь: при открытии старый файл при необходимости
// сдвигается, и только потом появляется дескриптор для записи.
func OpenLogger(path string) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("state: creating the log directory: %w", err)
	}
	if err := rotate(path); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("state: opening the log: %w", err)
	}
	return &Logger{path: path, file: f}, nil
}

// rotate сдвигает журнал, если он дорос до порога.
//
// Имена поколений: app.log.1 — свежайшее прошлое, app.log.3 — самое старое.
// Сдвиг идёт от старших к младшим, иначе поколения затёрли бы друг друга.
func rotate(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("state: inspecting the log: %w", err)
	}
	if info.Size() < rotateAt {
		return nil
	}

	oldest := fmt.Sprintf("%s.%d", path, keepGenerations)
	_ = os.Remove(oldest)

	for i := keepGenerations - 1; i >= 1; i-- {
		from := fmt.Sprintf("%s.%d", path, i)
		to := fmt.Sprintf("%s.%d", path, i+1)
		if _, err := os.Stat(from); err != nil {
			continue // этого поколения ещё нет
		}
		if err := os.Rename(from, to); err != nil {
			return fmt.Errorf("state: rotating %s: %w", from, err)
		}
	}

	if err := os.Rename(path, path+".1"); err != nil {
		return fmt.Errorf("state: rotating %s: %w", path, err)
	}
	return nil
}

// Printf пишет строку в журнал.
//
// Формат: время, уровень, сообщение. Уровень — слово, а не код: журнал читает
// человек, и искать таблицу соответствия кодов ему незачем.
func (l *Logger) Printf(level, format string, args ...any) {
	if l == nil {
		return
	}
	line := fmt.Sprintf("%s %s %s\n",
		time.Now().Format("2006-01-02 15:04:05.000"),
		level,
		fmt.Sprintf(format, args...),
	)

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_, _ = l.file.WriteString(line)
	}
}

// Info записывает событие жизни демона.
func (l *Logger) Info(format string, args ...any) { l.Printf("INFO", format, args...) }

// Warn записывает то, что стоит внимания, но не мешает работе.
func (l *Logger) Warn(format string, args ...any) { l.Printf("WARN", format, args...) }

// Error записывает отказ.
func (l *Logger) Error(format string, args ...any) { l.Printf("ERROR", format, args...) }

// Close закрывает журнал.
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// Path возвращает путь журнала.
func (l *Logger) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Truncate обрезает журнал НА МЕСТЕ.
//
// Именно на месте, а не удалением: демон держит дескриптор открытым, и
// удаление файла оставило бы его писать в никуда, пока процесс жив. Обрезка
// через тот же дескриптор сохраняет непрерывность записи.
func Truncate(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("state: truncating %s: %w", path, err)
	}
	return f.Close()
}

// LogFileSize возвращает размер журнала вместе с поколениями.
//
// Нужен команде `data`: оператор должен видеть, сколько занимает журнал, а не
// только текущий файл.
func LogFileSize(path string) (bytes int64, files int, err error) {
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	base := filepath.Base(path)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name != base && !strings.HasPrefix(name, base+".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		bytes += info.Size()
		files++
	}
	return bytes, files, nil
}

// Tail возвращает последние n строк журнала.
//
// Читается с конца файла блоками, а не целиком: журнал может быть в мегабайтах,
// а нужны обычно последние строки.
func Tail(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("state: reading the log: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	const block = 64 << 10
	size := info.Size()
	var (
		buf    []byte
		offset = size
	)
	for offset > 0 && strings.Count(string(buf), "\n") <= n {
		readSize := int64(block)
		if offset < readSize {
			readSize = offset
		}
		offset -= readSize

		chunk := make([]byte, readSize)
		if _, err := f.ReadAt(chunk, offset); err != nil && err != io.EOF {
			return nil, err
		}
		buf = append(chunk, buf...)
	}

	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil, nil
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := make([]string, len(lines))
	copy(out, lines)
	return out, nil
}

// LogFiles возвращает поколения журнала ОТ СВЕЖЕГО К СТАРОМУ.
//
// Порядок задан явно, а не сортировкой имён: `sort.Strings` поставил бы
// "app.log" перед "app.log.1", то есть текущий файл оказался бы раньше
// поколений, хотя он самый свежий по смыслу. Именно так этот вывод и
// читается — «сначала то, что произошло недавно».
func LogFiles(path string) []string {
	out := []string{path}
	for i := 1; i <= keepGenerations; i++ {
		candidate := fmt.Sprintf("%s.%d", path, i)
		if _, err := os.Stat(candidate); err == nil {
			out = append(out, candidate)
		}
	}
	return out
}
