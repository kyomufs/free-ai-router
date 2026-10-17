// Package state stores what survives a restart: counters and the log.
//
// WHAT IS HERE AND WHY. The gateway is a middleman, so most of the usual state
// is of no use to it. There is neither "what has already been spent, so as not
// to spend it again" (the gateway does not choose the egress address), nor a
// history of providers, nor records of rotations. What remains is exactly what
// answers the operator's questions:
//
//	how many requests went through and how many were refused, by day and by model;
//	what the daemon did, in a single log.
//
// THERE ARE EXACTLY TWO FILES: state and log. This is a deliberate limitation —
// a set of files scattered across directories can be neither backed up nor
// understood.
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

// The state schema. The version is needed so that a future change of shape can
// tell an old file from a new one instead of parsing it at random.
const schemaVersion = 1

// The retention window. It is trimmed on write, so "the file is never deleted"
// does not mean "it grows without bound".
const retentionDays = 30

// Store is the daemon's state.
//
// All methods are safe for concurrent calls: the counters are updated from
// request handlers, which run in parallel.
type Store struct {
	mu   sync.Mutex
	path string

	// days are the counters by day.
	days map[string]*DayCounters
}

// DayCounters are the counters of a single day.
type DayCounters struct {
	// OK is the number of successful requests.
	OK int64 `json:"ok"`
	// Refused is the number of refusals received from the provider.
	Refused int64 `json:"refused"`
	// ByModel is the breakdown by model.
	ByModel map[string]*ModelCounters `json:"by_model,omitempty"`
	// Keys is the breakdown by key fingerprints. Empty when running anonymously.
	Keys map[string]*KeyCounters `json:"keys,omitempty"`
}

// ModelCounters are the counters of a single model.
type ModelCounters struct {
	OK      int64 `json:"ok"`
	Refused int64 `json:"refused"`
	// LastSeen is the moment of the last request to the model.
	LastSeen int64 `json:"last_seen,omitempty"`
}

// KeyCounters are the counters of a single key.
type KeyCounters struct {
	OK      int64 `json:"ok"`
	Refused int64 `json:"refused"`
	// LastRefusalAt is the moment of the last refusal for this key.
	LastRefusalAt int64 `json:"last_refusal_at,omitempty"`
}

// fileShape is the shape of the file on disk.
type fileShape struct {
	Version   int                     `json:"version"`
	UpdatedAt int64                   `json:"updated_at"`
	Days      map[string]*DayCounters `json:"days"`
}

// Open opens the state at the given path.
//
// A corrupt file does not block startup: it is replaced with an empty one, and
// the reason goes into the log. Refusing to start because of the counters would
// be out of proportion — they exist for reference, not for work.
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
		// The file is corrupt: we start from a clean one, but do NOT delete
		// it silently — the operator must see what happened.
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

// RecordSuccess records a successful request.
//
// The record goes both into memory and ONTO DISK. Onto disk because the
// counters exist precisely to survive a restart: keeping them only in memory
// would mean losing them on every restart, that is not having them at all.
//
// The cost of the write is small: the file is small, and the gateway does not
// serve many requests. That is cheaper than losing the counters.
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

// RecordRefusal records a provider refusal.
//
// The refusal is recorded BEFORE any decision about a retry: otherwise the
// counters would not answer the question "what has been spent". And it is saved
// to disk straight away, for the same reason as a success: a counter that did
// not survive a restart is useless.
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

// dayLocked returns the counters of the day, creating them if necessary.
// The caller holds the lock.
func (s *Store) dayLocked(t time.Time) *DayCounters {
	return s.dayForLocked(dayKey(t))
}

// dayForLocked returns the counters for a day key.
func (s *Store) dayForLocked(key string) *DayCounters {
	d, ok := s.days[key]
	if !ok {
		d = &DayCounters{ByModel: make(map[string]*ModelCounters), Keys: make(map[string]*KeyCounters)}
		s.days[key] = d
	}
	return d
}

// modelLocked returns the counters of a model within a day.
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

// keyLocked returns the counters of a key within a day.
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

// Save writes the state to disk.
//
// The write is atomic: first to a temporary file alongside, then a rename.
// A direct write would leave a truncated file if the process dies halfway.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

// saveLocked performs the write. The caller holds the lock.
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
	defer os.Remove(tmpName) // if the rename did not happen

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

// pruneLocked drops the days outside the retention window.
//
// Otherwise "the file is never deleted" would mean "it grows without bound".
func (s *Store) pruneLocked(now time.Time) {
	cutoff := dayKey(now.AddDate(0, 0, -retentionDays))
	for key := range s.days {
		if key < cutoff {
			delete(s.days, key)
		}
	}
}

// Clear zeroes the counters.
func (s *Store) Clear() error {
	s.mu.Lock()
	s.days = make(map[string]*DayCounters)
	err := s.saveLocked()
	s.mu.Unlock()
	return err
}

// Snapshot is a snapshot of the state for reading.
type Snapshot struct {
	// Version is the schema version.
	Version int `json:"version"`
	// UpdatedAt is the moment of the snapshot.
	UpdatedAt int64 `json:"updated_at"`
	// Today is the key of today.
	Today string `json:"today"`
	// Days are the counters by day, the newest first.
	Days []DaySnapshot `json:"days"`
}

// DaySnapshot are the counters of a single day, for reading.
type DaySnapshot struct {
	Day      string          `json:"day"`
	OK       int64           `json:"ok"`
	Refused  int64           `json:"refused"`
	ByModel  []ModelSnapshot `json:"by_model,omitempty"`
	KeyCount int             `json:"key_count,omitempty"`
	Keys     []KeySnapshot   `json:"keys,omitempty"`
}

// ModelSnapshot are the counters of a single model.
type ModelSnapshot struct {
	Model    string `json:"model"`
	OK       int64  `json:"ok"`
	Refused  int64  `json:"refused"`
	LastSeen int64  `json:"last_seen,omitempty"`
}

// KeySnapshot are the counters of a single key.
type KeySnapshot struct {
	Fingerprint   string `json:"fingerprint"`
	OK            int64  `json:"ok"`
	Refused       int64  `json:"refused"`
	LastRefusalAt int64  `json:"last_refusal_at,omitempty"`
}

// Snapshot returns a snapshot for reading.
//
// The order is given explicitly: days from the newest to the oldest, models and
// keys alphabetically. Without that the output would change between calls, and
// comparing two snapshots would be impossible.
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

// dayKey returns the key of a day.
//
// The date is taken in UTC: providers reset their daily limits by UTC, and a
// local date would diverge from their day boundary.
func dayKey(t time.Time) string { return t.UTC().Format("2006-01-02") }
