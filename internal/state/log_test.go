package state

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoggerWritesAndReadsBack — журнал пишется и читается.
func TestLoggerWritesAndReadsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	l, err := OpenLogger(path)
	if err != nil {
		t.Fatalf("OpenLogger: %v", err)
	}
	defer l.Close()

	l.Info("демон запущен на %s", "127.0.0.1:8787")
	l.Warn("модель %q недоступна", "м")
	l.Error("отказ: %d", 403)

	lines, err := Tail(path, 10)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("Tail returned %d lines, want 3: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "INFO") || !strings.Contains(lines[0], "демон запущен") {
		t.Fatalf("the first line lacks the level or the message: %q", lines[0])
	}
	if !strings.Contains(lines[1], "WARN") {
		t.Fatalf("the second line lacks WARN: %q", lines[1])
	}
	if !strings.Contains(lines[2], "ERROR") {
		t.Fatalf("the third line lacks ERROR: %q", lines[2])
	}
}

// TestTailReturnsLastLines — нужны последние строки, а не первые.
func TestTailReturnsLastLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	l, err := OpenLogger(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		l.Info("строка %d", i)
	}
	l.Close()

	lines, err := Tail(path, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 5 {
		t.Fatalf("Tail returned %d lines, want 5", len(lines))
	}
	if !strings.Contains(lines[4], "строка 49") {
		t.Fatalf("the last line is %q, want the newest entry", lines[4])
	}
	if strings.Contains(lines[0], "строка 0") {
		t.Fatal("Tail returned the oldest lines instead of the newest")
	}
}

// TestTailOnMissingFile — отсутствующий журнал это не ошибка: демон мог ещё
// ничего не записать.
func TestTailOnMissingFile(t *testing.T) {
	lines, err := Tail(filepath.Join(t.TempDir(), "нет.log"), 10)
	if err != nil {
		t.Fatalf("Tail on a missing file: %v", err)
	}
	if len(lines) != 0 {
		t.Fatalf("Tail returned %d lines, want none", len(lines))
	}
}

// TestRotateShiftsGenerations — ротация сдвигает поколения, не затирая их.
func TestRotateShiftsGenerations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	// Наполняем файл выше порога ротации.
	big := strings.Repeat("x", rotateAt+1)
	if err := os.WriteFile(path, []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	// Кладём уже существующее поколение, чтобы проверить сдвиг.
	if err := os.WriteFile(path+".1", []byte("прошлое поколение"), 0o644); err != nil {
		t.Fatal(err)
	}

	l, err := OpenLogger(path)
	if err != nil {
		t.Fatalf("OpenLogger: %v", err)
	}
	defer l.Close()

	// Прежнее поколение уехало на позицию 2.
	raw, err := os.ReadFile(path + ".2")
	if err != nil {
		t.Fatalf("the previous generation was not shifted: %v", err)
	}
	if string(raw) != "прошлое поколение" {
		t.Fatalf("generation 2 = %q, want the previous content", raw)
	}
	// Большой файл стал поколением 1.
	if _, err := os.ReadFile(path + ".1"); err != nil {
		t.Fatalf("the rotated file did not become generation 1: %v", err)
	}
	// Текущий файл пуст: он только что создан.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatalf("the new log is %d bytes, want empty", info.Size())
	}
}

// TestRotateKeepsBoundedGenerations — число поколений ограничено, иначе
// журнал рос бы безгранично.
//
// Замечание о форме: сама rotate() только СДВИГАЕТ файлы — текущий уезжает в
// .1, а новый создаётся при открытии. Поэтому здесь проверяется число
// сохранённых поколений, а не общее число файлов.
func TestRotateKeepsBoundedGenerations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	for i := 0; i < keepGenerations+3; i++ {
		big := strings.Repeat("y", rotateAt+1)
		if err := os.WriteFile(path, []byte(big), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := rotate(path); err != nil {
			t.Fatalf("rotate #%d: %v", i, err)
		}
	}

	// Старше keepGenerations поколений быть не должно.
	if _, err := os.Stat(fmt.Sprintf("%s.%d", path, keepGenerations+1)); err == nil {
		t.Fatalf("generation %d survived, so the log grows without bound", keepGenerations+1)
	}
	// А сами поколения на месте.
	for i := 1; i <= keepGenerations; i++ {
		if _, err := os.Stat(fmt.Sprintf("%s.%d", path, i)); err != nil {
			t.Fatalf("generation %d is missing: %v", i, err)
		}
	}
}

// TestRotateIsNoopBelowThreshold — маленький журнал не трогается.
func TestRotateIsNoopBelowThreshold(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	if err := os.WriteFile(path, []byte("мало"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rotate(path); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if _, err := os.Stat(path + ".1"); err == nil {
		t.Fatal("a small log was rotated")
	}
}

// TestTruncateKeepsTheFile — обрезка сохраняет файл на месте, а не удаляет
// его: демон держит дескриптор открытым, и удаление оставило бы его писать в
// никуда.
func TestTruncateKeepsTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	if err := os.WriteFile(path, []byte("данные\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Truncate(path); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the file disappeared after Truncate: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("the file is %d bytes, want empty", info.Size())
	}
}

// TestTruncateOnMissingFile — отсутствующий файл не ошибка.
func TestTruncateOnMissingFile(t *testing.T) {
	if err := Truncate(filepath.Join(t.TempDir(), "нет.log")); err != nil {
		t.Fatalf("Truncate on a missing file: %v", err)
	}
}

// TestLogFileSizeCountsGenerations — команда `data` должна видеть полный
// размер, а не только текущий файл.
func TestLogFileSizeCountsGenerations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	if err := os.WriteFile(path, []byte("текущий"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".1", []byte("прошлый"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Посторонний файл не должен попасть в подсчёт.
	if err := os.WriteFile(filepath.Join(dir, "другое.json"), []byte("мимо"), 0o644); err != nil {
		t.Fatal(err)
	}

	bytes, files, err := LogFileSize(path)
	if err != nil {
		t.Fatalf("LogFileSize: %v", err)
	}
	if files != 2 {
		t.Fatalf("files = %d, want 2", files)
	}
	if want := int64(len("текущий") + len("прошлый")); bytes != want {
		t.Fatalf("bytes = %d, want %d", bytes, want)
	}
}

// TestLogFilesOrderedNewestFirst — порядок задан вручную: сортировка имён
// поставила бы текущий файл перед поколениями, а он самый свежий по смыслу.
func TestLogFilesOrderedNewestFirst(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	for _, name := range []string{"app.log", "app.log.1", "app.log.2"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got := LogFiles(path)
	want := []string{path, path + ".1", path + ".2"}
	if len(got) != len(want) {
		t.Fatalf("LogFiles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("LogFiles = %v, want %v", got, want)
		}
	}
}

// TestConcurrentWritesDoNotInterleave — строки не перемешиваются: журнал
// читают глазами, и склеенная строка бесполезна.
func TestConcurrentWritesDoNotInterleave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	l, err := OpenLogger(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	const writers, each = 8, 40
	done := make(chan struct{})
	for w := 0; w < writers; w++ {
		go func(id int) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < each; i++ {
				l.Info("писатель %d строка %d", id, i)
			}
		}(w)
	}
	for w := 0; w < writers; w++ {
		<-done
	}

	lines, err := Tail(path, writers*each+10)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != writers*each {
		t.Fatalf("lines = %d, want %d (writes were lost)", len(lines), writers*each)
	}
	for _, line := range lines {
		// Каждая строка обязана начинаться с метки времени и содержать ровно
		// одно сообщение.
		if !strings.Contains(line, "INFO") {
			t.Fatalf("a line lacks its level: %q", line)
		}
		if strings.Count(line, "писатель") != 1 {
			t.Fatalf("lines interleaved: %q", line)
		}
	}
}

// TestNilLoggerIsSafe — вызовы на nil-журнале не паникуют: так код, которому
// журнал не передали, остаётся рабочим.
func TestNilLoggerIsSafe(t *testing.T) {
	var l *Logger
	l.Info("ничего")
	l.Warn("ничего")
	l.Error("ничего")
	if err := l.Close(); err != nil {
		t.Fatalf("Close on nil: %v", err)
	}
	if l.Path() != "" {
		t.Fatalf("Path on nil = %q, want empty", l.Path())
	}
}
