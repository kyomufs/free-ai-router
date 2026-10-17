package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// openTemp создаёт состояние во временном каталоге.
func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s, path
}

// TestCountersAccumulate — счётчики складываются по дням, моделям и ключам.
func TestCountersAccumulate(t *testing.T) {
	s, _ := openTemp(t)

	s.RecordSuccess("модель-а", "aaaa1111")
	s.RecordSuccess("модель-а", "aaaa1111")
	s.RecordSuccess("модель-б", "bbbb2222")
	s.RecordRefusal("модель-а", "aaaa1111")

	snap := s.Snapshot()
	if len(snap.Days) != 1 {
		t.Fatalf("Days = %d, want 1", len(snap.Days))
	}
	day := snap.Days[0]
	if day.OK != 3 {
		t.Fatalf("OK = %d, want 3", day.OK)
	}
	if day.Refused != 1 {
		t.Fatalf("Refused = %d, want 1", day.Refused)
	}

	byModel := map[string]ModelSnapshot{}
	for _, m := range day.ByModel {
		byModel[m.Model] = m
	}
	if got := byModel["модель-а"]; got.OK != 2 || got.Refused != 1 {
		t.Fatalf("model а = %+v, want ok=2 refused=1", got)
	}
	if got := byModel["модель-б"]; got.OK != 1 || got.Refused != 0 {
		t.Fatalf("model б = %+v, want ok=1 refused=0", got)
	}
	if len(day.Keys) != 2 {
		t.Fatalf("Keys = %d, want 2", len(day.Keys))
	}
}

// TestEmptyModelAndKeyDoNotCreateEntries — пустая модель или ключ не должны
// заводить пустые записи: они бы искажали списки.
func TestEmptyModelAndKeyDoNotCreateEntries(t *testing.T) {
	s, _ := openTemp(t)
	s.RecordSuccess("", "")
	s.RecordRefusal("", "")

	day := s.Snapshot().Days[0]
	if len(day.ByModel) != 0 {
		t.Fatalf("ByModel = %+v, want empty", day.ByModel)
	}
	if len(day.Keys) != 0 {
		t.Fatalf("Keys = %+v, want empty", day.Keys)
	}
	if day.OK != 1 || day.Refused != 1 {
		t.Fatalf("totals = ok %d refused %d, want 1 and 1", day.OK, day.Refused)
	}
}

// TestSaveAndReload — состояние переживает перезапуск. Это его единственная
// задача.
func TestSaveAndReload(t *testing.T) {
	s, path := openTemp(t)
	s.RecordSuccess("модель", "key1")
	s.RecordRefusal("модель", "key1")
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open after Save: %v", err)
	}
	snap := reopened.Snapshot()
	if len(snap.Days) != 1 {
		t.Fatalf("Days = %d after reload, want 1", len(snap.Days))
	}
	if snap.Days[0].OK != 1 || snap.Days[0].Refused != 1 {
		t.Fatalf("counters lost: %+v", snap.Days[0])
	}
}

// TestCorruptFileDoesNotBlockStart — повреждённый файл не мешает работе, но и
// не исчезает молча: причина возвращается.
func TestCorruptFileDoesNotBlockStart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("{это не json"), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err == nil {
		t.Fatal("a corrupt file must be reported")
	}
	if s == nil {
		t.Fatal("a corrupt file must still yield a usable store")
	}
	// Магазин обязан быть рабочим.
	s.RecordSuccess("модель", "k")
	if got := s.Snapshot().Days[0].OK; got != 1 {
		t.Fatalf("OK = %d, want 1", got)
	}

	// Запись ЗАМЕНЯЕТ повреждённый файл корректным.
	//
	// Так и должно быть: счётчики сохраняются на диск при каждой записи, иначе
	// они не переживут перезапуск — а это их единственная задача. Содержимое
	// повреждённого файла уже потеряно, восстанавливать нечего, поэтому его
	// место занимает новое состояние. Прежнее содержимое не исчезает молча:
	// причина возвращена из Open выше.
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var shape fileShape
	if uerr := json.Unmarshal(raw, &shape); uerr != nil {
		t.Fatalf("the file was not replaced with valid state: %v (%s)", uerr, raw)
	}
	if shape.Version != schemaVersion {
		t.Fatalf("version = %d, want %d", shape.Version, schemaVersion)
	}
}

// TestMissingFileIsNotAnError — отсутствующий файл это нормальный старт.
func TestMissingFileIsNotAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "нет.json")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if snap := s.Snapshot(); len(snap.Days) != 0 {
		t.Fatalf("a fresh store has %d days, want 0", len(snap.Days))
	}
}

// TestEmptyFileIsNotAnError — пустой файл равнозначен отсутствующему.
func TestEmptyFileIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err != nil {
		t.Fatalf("an empty file must be accepted: %v", err)
	}
}

// TestSaveIsAtomic — запись идёт через временный файл и переименование.
//
// Проверяется наблюдаемое следствие: после записи рядом не остаётся мусора, а
// файл читается как валидный JSON.
func TestSaveIsAtomic(t *testing.T) {
	s, path := openTemp(t)
	s.RecordSuccess("модель", "k")
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var shape fileShape
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatalf("the saved file is not valid JSON: %v", err)
	}
	if shape.Version != schemaVersion {
		t.Fatalf("version = %d, want %d", shape.Version, schemaVersion)
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != filepath.Base(path) {
			t.Fatalf("Save left a temporary file behind: %s", e.Name())
		}
	}
}

// TestPruneDropsOldDays — окно хранения ограничено, иначе файл рос бы вечно.
func TestPruneDropsOldDays(t *testing.T) {
	s, _ := openTemp(t)

	// Кладём заведомо старый день и свежий.
	s.mu.Lock()
	s.days["2000-01-01"] = &DayCounters{OK: 999}
	s.days[dayKeyForTest()] = &DayCounters{OK: 1}
	s.mu.Unlock()

	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	snap := s.Snapshot()
	for _, d := range snap.Days {
		if d.Day == "2000-01-01" {
			t.Fatal("a day outside the retention window survived the prune")
		}
	}
	if len(snap.Days) != 1 {
		t.Fatalf("Days = %d, want only today", len(snap.Days))
	}
}

// TestClearEmptiesEverything — очистка обнуляет счётчики и сохраняет пустое
// состояние.
func TestClearEmptiesEverything(t *testing.T) {
	s, path := openTemp(t)
	s.RecordSuccess("модель", "k")
	if err := s.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if snap := s.Snapshot(); len(snap.Days) != 0 {
		t.Fatalf("Clear left %d days", len(snap.Days))
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if snap := reopened.Snapshot(); len(snap.Days) != 0 {
		t.Fatalf("Clear did not persist: %d days on reload", len(snap.Days))
	}
}

// TestSnapshotIsDeterministicallyOrdered — без явного порядка сравнить два
// снимка было бы невозможно.
func TestSnapshotIsDeterministicallyOrdered(t *testing.T) {
	s, _ := openTemp(t)
	for _, m := range []string{"я", "б", "а"} {
		s.RecordSuccess(m, "k")
	}

	first := s.Snapshot()
	second := s.Snapshot()
	if len(first.Days) != len(second.Days) {
		t.Fatal("snapshot length varies between calls")
	}
	for i := range first.Days {
		a, b := first.Days[i].ByModel, second.Days[i].ByModel
		if len(a) != len(b) {
			t.Fatal("model list length varies between calls")
		}
		for j := range a {
			if a[j].Model != b[j].Model {
				t.Fatalf("model order varies: %q then %q", a[j].Model, b[j].Model)
			}
		}
		// И порядок обязан быть алфавитным.
		for j := 1; j < len(a); j++ {
			if a[j-1].Model > a[j].Model {
				t.Fatalf("models are not sorted: %q before %q", a[j-1].Model, a[j].Model)
			}
		}
	}
}

// TestDayKeyIsUTC — граница суток берётся по UTC, потому что провайдеры
// обнуляют суточные лимиты по UTC.
func TestDayKeyIsUTC(t *testing.T) {
	if got := dayKey(parseTime(t, "2026-10-17T23:30:00Z")); got != "2026-10-17" {
		t.Fatalf("dayKey(23:30Z) = %q", got)
	}
	if got := dayKey(parseTime(t, "2026-10-18T00:30:00Z")); got != "2026-10-18" {
		t.Fatalf("dayKey(00:30Z) = %q", got)
	}
}

// --- вспомогательные ---

// dayKeyForTest возвращает сегодняшний ключ дня.
func dayKeyForTest() string { return dayKey(time.Now()) }

// parseTime разбирает момент времени в формате RFC3339.
func parseTime(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parseTime(%q): %v", s, err)
	}
	return at
}
