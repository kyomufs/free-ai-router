// Package state хранит то, что переживает рестарт: счётчики и журнал.
//
// ЧТО ЗДЕСЬ ЕСТЬ И ПОЧЕМУ. Шлюз — посредник, поэтому большая часть привычного
// состояния ему не нужна. Нет ни «что уже израсходовано, чтобы не тратить
// повторно» (шлюз не выбирает адрес выхода), ни истории провайдеров, ни
// записей о ротациях. Остаётся ровно то, что отвечает на вопросы оператора:
//
//	сколько запросов прошло и сколько отказано, по дням и по моделям;
//	что делал демон, в одном журнале.
//
// ФАЙЛОВ РОВНО ДВА: состояние и журнал. Это осознанное ограничение — набор
// файлов, разбросанных по каталогам, невозможно ни забэкапить, ни понять.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Схема состояния. Версия нужна, чтобы будущая смена формы могла отличить
// старый файл от нового, а не разобрать его наугад.
const schemaVersion = 1

// Окно хранения. Обрезается при записи, поэтому «файл никогда не удаляется»
// не означает «растёт бесконечно».
const retentionDays = 30

// Store — состояние демона.
//
// Все методы безопасны для одновременного вызова: счётчики обновляются из
// обработчиков запросов, которые работают параллельно.
type Store struct {
	mu   sync.Mutex
	path string

	// days — счётчики по дням.
	days map[string]*DayCounters
}

// DayCounters — счётчики одного дня.
type DayCounters struct {
	// OK — число успешных запросов.
	OK int64 `json:"ok"`
	// Refused — число отказов, полученных от провайдера.
	Refused int64 `json:"refused"`
	// ByModel — разбивка по моделям.
	ByModel map[string]*ModelCounters `json:"by_model,omitempty"`
	// Keys — разбивка по отпечаткам ключей. Пусто при анонимной работе.
	Keys map[string]*KeyCounters `json:"keys,omitempty"`
}

// ModelCounters — счётчики одной модели.
type ModelCounters struct {
	OK      int64 `json:"ok"`
	Refused int64 `json:"refused"`
	// LastSeen — момент последнего запроса к модели.
	LastSeen int64 `json:"last_seen,omitempty"`
}

// KeyCounters — счётчики одного ключа.
type KeyCounters struct {
	OK      int64 `json:"ok"`
	Refused int64 `json:"refused"`
	// LastRefusalAt — момент последнего отказа по этому ключу.
	LastRefusalAt int64 `json:"last_refusal_at,omitempty"`
}

// fileShape — форма файла на диске.
type fileShape struct {
	Version   int                     `json:"version"`
	UpdatedAt int64                   `json:"updated_at"`
	Days      map[string]*DayCounters `json:"days"`
}

// Open открывает состояние по указанному пути.
//
// Повреждённый файл не блокирует старт: он заменяется пустым, а причина
// попадает в журнал. Отказ стартовать из-за счётчиков был бы несоразмерен —
// они нужны для справки, а не для работы.
func Open(path string) (*Store, error) {
	s := &Store{path: path, days: make(map[string]*DayCounters)}

	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("state: reading %s: %w", path, err)
	}
	if len(raw) == 0 {
		return s, nil
	}

	var shape fileShape
	if err := json.Unmarshal(raw, &shape); err != nil {
		// Файл повреждён: начинаем с чистого, но НЕ удаляем его молча —
		// оператор должен увидеть, что произошло.
		return s, fmt.Errorf("state: %s is unreadable, starting empty: %w", path, err)
	}

	if shape.Days != nil {
		s.days = shape.Days
	}
	s.mu.Lock()
	s.pruneLocked(time.Now())
	s.mu.Unlock()
	return s, nil
}

// RecordSuccess записывает успешный запрос.
//
// Запись идёт и в память, и НА ДИСК. На диск — потому что счётчики существуют
// ровно ради того, чтобы пережить перезапуск: держать их только в памяти
// значило бы терять их при каждом рестарте, то есть не иметь вовсе.
//
// Цена записи невелика: файл маленький, а запросов у шлюза немного. Это дешевле,
// чем потерять счётчики.
func (s *Store) RecordSuccess(model, keyFingerprint string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	day := s.dayLocked(now)
	day.OK++
	if model != "" {
		m := s.modelLocked(day, model)
		m.OK++
		m.LastSeen = now.UnixMilli()
	}
	if keyFingerprint != "" {
		k := s.keyLocked(day, keyFingerprint)
		k.OK++
	}
	_ = s.saveLocked()
}

// RecordRefusal записывает отказ провайдера.
//
// Отказ записывается ДО любого решения о повторе: иначе счётчики не ответят на
// вопрос «что израсходовано». И сохраняется на диск сразу — по той же причине,
// что и успех: счётчик, не переживший перезапуск, бесполезен.
func (s *Store) RecordRefusal(model, keyFingerprint string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	day := s.dayLocked(now)
	day.Refused++
	if model != "" {
		m := s.modelLocked(day, model)
		m.Refused++
		m.LastSeen = now.UnixMilli()
	}
	if keyFingerprint != "" {
		k := s.keyLocked(day, keyFingerprint)
		k.Refused++
		k.LastRefusalAt = now.UnixMilli()
	}
	_ = s.saveLocked()
}

// dayLocked возвращает счётчики дня, создавая их при необходимости.
// Вызывающий держит блокировку.
func (s *Store) dayLocked(t time.Time) *DayCounters {
	return s.dayForLocked(dayKey(t))
}

// dayForLocked возвращает счётчики по ключу дня.
func (s *Store) dayForLocked(key string) *DayCounters {
	d, ok := s.days[key]
	if !ok {
		d = &DayCounters{ByModel: make(map[string]*ModelCounters), Keys: make(map[string]*KeyCounters)}
		s.days[key] = d
	}
	return d
}

// modelLocked возвращает счётчики модели внутри дня.
func (s *Store) modelLocked(day *DayCounters, model string) *ModelCounters {
	if day.ByModel == nil {
		day.ByModel = make(map[string]*ModelCounters)
	}
	m, ok := day.ByModel[model]
	if !ok {
		m = &ModelCounters{}
		day.ByModel[model] = m
	}
	return m
}

// keyLocked возвращает счётчики ключа внутри дня.
func (s *Store) keyLocked(day *DayCounters, fingerprint string) *KeyCounters {
	if day.Keys == nil {
		day.Keys = make(map[string]*KeyCounters)
	}
	k, ok := day.Keys[fingerprint]
	if !ok {
		k = &KeyCounters{}
		day.Keys[fingerprint] = k
	}
	return k
}

// Save пишет состояние на диск.
//
// Запись атомарна: сначала во временный файл рядом, затем переименование.
// Прямая запись оставила бы обрезанный файл, если процесс умрёт посередине.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

// saveLocked выполняет запись. Вызывающий держит блокировку.
func (s *Store) saveLocked() error {
	s.pruneLocked(time.Now())

	shape := fileShape{
		Version:   schemaVersion,
		UpdatedAt: time.Now().UnixMilli(),
		Days:      s.days,
	}
	raw, err := json.MarshalIndent(shape, "", "  ")
	if err != nil {
		return fmt.Errorf("state: encoding: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("state: creating the directory: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("state: creating a temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // если переименование не состоялось

	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("state: writing: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("state: closing: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("state: replacing %s: %w", s.path, err)
	}
	return nil
}

// pruneLocked выкидывает дни за пределами окна хранения.
//
// Иначе «файл никогда не удаляется» означало бы «растёт безгранично».
func (s *Store) pruneLocked(now time.Time) {
	cutoff := dayKey(now.AddDate(0, 0, -retentionDays))
	for key := range s.days {
		if key < cutoff {
			delete(s.days, key)
		}
	}
}

// Clear обнуляет счётчики.
func (s *Store) Clear() error {
	s.mu.Lock()
	s.days = make(map[string]*DayCounters)
	err := s.saveLocked()
	s.mu.Unlock()
	return err
}

// Snapshot — снимок состояния для чтения.
type Snapshot struct {
	// Version — версия схемы.
	Version int `json:"version"`
	// UpdatedAt — момент снимка.
	UpdatedAt int64 `json:"updated_at"`
	// Today — ключ сегодняшнего дня.
	Today string `json:"today"`
	// Days — счётчики по дням, свежие первыми.
	Days []DaySnapshot `json:"days"`
}

// DaySnapshot — счётчики одного дня для чтения.
type DaySnapshot struct {
	Day      string          `json:"day"`
	OK       int64           `json:"ok"`
	Refused  int64           `json:"refused"`
	ByModel  []ModelSnapshot `json:"by_model,omitempty"`
	KeyCount int             `json:"key_count,omitempty"`
	Keys     []KeySnapshot   `json:"keys,omitempty"`
}

// ModelSnapshot — счётчики одной модели.
type ModelSnapshot struct {
	Model    string `json:"model"`
	OK       int64  `json:"ok"`
	Refused  int64  `json:"refused"`
	LastSeen int64  `json:"last_seen,omitempty"`
}

// KeySnapshot — счётчики одного ключа.
type KeySnapshot struct {
	Fingerprint   string `json:"fingerprint"`
	OK            int64  `json:"ok"`
	Refused       int64  `json:"refused"`
	LastRefusalAt int64  `json:"last_refusal_at,omitempty"`
}

// Snapshot возвращает снимок для чтения.
//
// Порядок задан явно: дни от свежих к старым, модели и ключи по алфавиту.
// Без этого вывод менялся бы между вызовами, и сравнить два снимка было бы
// невозможно.
func (s *Store) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	out := Snapshot{
		Version:   schemaVersion,
		UpdatedAt: now.UnixMilli(),
		Today:     dayKey(now),
	}

	keys := make([]string, 0, len(s.days))
	for key := range s.days {
		keys = append(keys, key)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))

	for _, key := range keys {
		d := s.days[key]
		day := DaySnapshot{Day: key, OK: d.OK, Refused: d.Refused}

		models := make([]string, 0, len(d.ByModel))
		for name := range d.ByModel {
			models = append(models, name)
		}
		sort.Strings(models)
		for _, name := range models {
			m := d.ByModel[name]
			day.ByModel = append(day.ByModel, ModelSnapshot{
				Model: name, OK: m.OK, Refused: m.Refused, LastSeen: m.LastSeen,
			})
		}

		day.KeyCount = len(d.Keys)
		fingerprints := make([]string, 0, len(d.Keys))
		for fp := range d.Keys {
			fingerprints = append(fingerprints, fp)
		}
		sort.Strings(fingerprints)
		for _, fp := range fingerprints {
			k := d.Keys[fp]
			day.Keys = append(day.Keys, KeySnapshot{
				Fingerprint: fp, OK: k.OK, Refused: k.Refused, LastRefusalAt: k.LastRefusalAt,
			})
		}

		out.Days = append(out.Days, day)
	}
	return out
}

// dayKey возвращает ключ дня.
//
// Дата берётся в UTC: провайдеры обнуляют суточные лимиты по UTC, и местная
// дата разошлась бы с их границей суток.
func dayKey(t time.Time) string { return t.UTC().Format("2006-01-02") }
