// Package state stores what survives a restart: counters and the log.
//
// WHAT IS HERE AND WHY. The gateway is a middleman, so most of the usual state
// is of no use to it. There is neither "what has already been spent, so as not
// to spend it again" (the gateway does not choose the egress address), nor a
// history of providers, nor records of rotations. What remains is exactly what
// answers the operator's questions:
//
//	how many requests went through, were refused and were aborted, by day, by
//	  egress address and by model;
//	what the daemon did, in a single log.
//
// THERE ARE EXACTLY TWO FILES: state and log. This is a deliberate limitation —
// a set of files scattered across directories can be neither backed up nor
// understood.
package state

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// The state schema. The version is needed so that a future change of shape can
// tell an old file from a new one instead of parsing it at random.
//
// Version 2 added the third outcome ("aborted") and the address dimension. A
// version 1 file carries neither, and needs no migration branch: the absent
// counters read as zero, and an empty address breakdown is exactly what such a
// file means.
const schemaVersion = 2

// The retention window. It is trimmed on write, so "the file is never deleted"
// does not mean "it grows without bound".
const retentionDays = 30

// RetentionDays is the window as the reader of a day selector needs it.
//
// Exported because the number is the answer to a question asked OUTSIDE this
// package: a reader asking for a day the state no longer holds is asking about
// the retention, and "not kept" without a window is a shrug rather than an
// answer. It is the same constant the trim uses — one number, one place.
func RetentionDays() int { return retentionDays }

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

// Outcome is one recorded request outcome: which model was used, from which
// egress address, and with which credential.
//
// The three fields are separate so that a call site reads as one sentence
// instead of a row of positional arguments. An empty model, address or key is
// legal and means the caller did not know it: it still counts towards the
// totals, and it creates no entry of its own.
type Outcome struct {
	Model   string
	Address string
	Key     string
}

// DayCounters are the counters of a single day.
type DayCounters struct {
	// OK is the number of successful requests.
	OK int64 `json:"ok"`
	// Refused is the number of refusals received from the provider.
	Refused int64 `json:"refused"`
	// Aborted is the number of requests that ended without an answer from the
	// provider: an unreachable upstream or a cancellation by the client. It is
	// kept apart from Refused because a client giving up is not the provider
	// refusing, and the error column of the monitor measures the provider.
	Aborted int64 `json:"aborted"`
	// ByModel is the breakdown by model.
	ByModel map[string]*ModelCounters `json:"by_model,omitempty"`
	// ByAddress is the breakdown by the egress address the request left from.
	// Empty when the daemon does not know its address: the provider's anonymous
	// limiter buckets quota by that address, and the IPv4 and IPv6 buckets are
	// independent.
	ByAddress map[string]*AddressCounters `json:"by_address,omitempty"`
	// Keys is the breakdown by key fingerprints. Empty when running anonymously.
	Keys map[string]*KeyCounters `json:"keys,omitempty"`
}

// ModelCounters are the counters of a single model.
type ModelCounters struct {
	OK      int64 `json:"ok"`
	Refused int64 `json:"refused"`
	Aborted int64 `json:"aborted"`
	// LastSeen is the moment of the last request to the model.
	LastSeen int64 `json:"last_seen,omitempty"`
}

// AddressCounters are the counters of a single egress address.
type AddressCounters struct {
	// Family is "v4" or "v6", derived from the address. Empty when the address
	// cannot be parsed: better an empty cell than a guess. The family is a label
	// for the operator, not a bucket — the limiter buckets by the address string.
	Family  string `json:"family,omitempty"`
	OK      int64  `json:"ok"`
	Refused int64  `json:"refused"`
	Aborted int64  `json:"aborted"`
	// FirstSeen is the moment this address first carried a request. It is what
	// orders the addresses of a day: the uplink is switched by hand, so the
	// order the addresses appeared in IS the order of the switches, and a reader
	// follows it without having to compare timestamps.
	FirstSeen int64 `json:"first_seen,omitempty"`
	// LastSeen is the moment it last carried one. Zero means the mark predates
	// this field.
	LastSeen int64 `json:"last_seen,omitempty"`
	// ByModel is the breakdown by model within this address.
	ByModel map[string]*ModelCounters `json:"by_model,omitempty"`
}

// KeyCounters are the counters of a single key.
type KeyCounters struct {
	OK      int64 `json:"ok"`
	Refused int64 `json:"refused"`
	Aborted int64 `json:"aborted"`
	// LastSeen is the moment the key was last used, whatever the outcome.
	//
	// It is separate from LastRefusalAt because the two answer different
	// questions: this one is "which key is in use right now", and a key that is
	// working is exactly the one that never appears in the refusal mark. Without
	// it the monitor could show which keys exist but not which one is spending
	// the quota.
	LastSeen int64 `json:"last_seen,omitempty"`
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

	// A version 1 file needs no migration branch. It has neither "aborted" nor
	// "by_address", so encoding/json leaves both at their zero value, and zero
	// is what the absence means: no aborts recorded, no address known. The
	// counters that ARE there are kept as they are — they are the operator's
	// data — and the next write stamps the file as version 2.
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
func (s *Store) RecordSuccess(o Outcome) { s.record(o, outcomeOK) }

// RecordRefusal records a provider refusal.
//
// The refusal is recorded BEFORE any decision about a retry: otherwise the
// counters would not answer the question "what has been spent". And it is saved
// to disk straight away, for the same reason as a success: a counter that did
// not survive a restart is useless.
func (s *Store) RecordRefusal(o Outcome) { s.record(o, outcomeRefused) }

// RecordAbort records a request that ended without an answer from the provider:
// an unreachable upstream or a cancellation by the client.
//
// This is deliberately NOT folded into a refusal. A client that gave up is not
// the provider refusing anything, and the error column of the monitor is read
// as the provider's fault — counting aborts there would blame it for the
// client's own timeout.
func (s *Store) RecordAbort(o Outcome) { s.record(o, outcomeAborted) }

// record applies one outcome at every level of the schema.
//
// One body for the three entry points on purpose: every outcome increments the
// day, the address, the model and the key the same way, and three copies of
// that would be three places to keep in step.
func (s *Store) record(o Outcome, kind outcomeKind) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	day := s.dayLocked(now)
	kind.bump(&day.OK, &day.Refused, &day.Aborted)

	if o.Model != "" {
		m := s.modelLocked(day, o.Model)
		kind.bump(&m.OK, &m.Refused, &m.Aborted)
		m.LastSeen = now.UnixMilli()
	}

	// An empty address creates NO entry. The daemon does not always know its
	// egress address, and a row named "unknown" would be an invented bucket in
	// the matrix — worse than a missing one. The totals still move.
	if o.Address != "" {
		a := s.addressLocked(day, o.Address)
		kind.bump(&a.OK, &a.Refused, &a.Aborted)
		// The first mark is written once and then never moved: it is what makes
		// the addresses of a day readable as a sequence of uplinks rather than as
		// a set. Every outcome moves the last mark, for the same reason every
		// outcome moves a key's.
		if a.FirstSeen == 0 {
			a.FirstSeen = now.UnixMilli()
		}
		a.LastSeen = now.UnixMilli()
		if o.Model != "" {
			am := s.addressModelLocked(a, o.Model)
			kind.bump(&am.OK, &am.Refused, &am.Aborted)
			am.LastSeen = now.UnixMilli()
		}
	}

	if o.Key != "" {
		k := s.keyLocked(day, o.Key)
		kind.bump(&k.OK, &k.Refused, &k.Aborted)
		// Every outcome moves the mark: the key was used, which is the question
		// this field answers. Only a refusal moves the refusal mark: an abort is
		// not a refusal.
		k.LastSeen = now.UnixMilli()
		if kind == outcomeRefused {
			k.LastRefusalAt = now.UnixMilli()
		}
	}

	_ = s.saveLocked()
}

// outcomeKind is which of the three counters an outcome increments.
type outcomeKind int

const (
	outcomeOK outcomeKind = iota
	outcomeRefused
	outcomeAborted
)

// bump increments the counter named by the kind.
func (kind outcomeKind) bump(ok, refused, aborted *int64) {
	switch kind {
	case outcomeOK:
		*ok++
	case outcomeRefused:
		*refused++
	case outcomeAborted:
		*aborted++
	}
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
		d = &DayCounters{
			ByModel:   make(map[string]*ModelCounters),
			ByAddress: make(map[string]*AddressCounters),
			Keys:      make(map[string]*KeyCounters),
		}
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

// addressLocked returns the counters of an egress address within a day.
//
// The caller must pass a non-empty address: record checks that before calling.
// An empty one would be the invented row the schema forbids.
func (s *Store) addressLocked(day *DayCounters, address string) *AddressCounters {
	if day.ByAddress == nil {
		day.ByAddress = make(map[string]*AddressCounters)
	}
	a, ok := day.ByAddress[address]
	if !ok {
		a = &AddressCounters{Family: addressFamily(address)}
		day.ByAddress[address] = a
	}
	return a
}

// addressModelLocked returns the counters of a model within an address.
func (s *Store) addressModelLocked(a *AddressCounters, model string) *ModelCounters {
	if a.ByModel == nil {
		a.ByModel = make(map[string]*ModelCounters)
	}
	m, ok := a.ByModel[model]
	if !ok {
		m = &ModelCounters{}
		a.ByModel[model] = m
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
	Day       string            `json:"day"`
	OK        int64             `json:"ok"`
	Refused   int64             `json:"refused"`
	Aborted   int64             `json:"aborted"`
	ByModel   []ModelSnapshot   `json:"by_model,omitempty"`
	ByAddress []AddressSnapshot `json:"by_address,omitempty"`
	KeyCount  int               `json:"key_count,omitempty"`
	Keys      []KeySnapshot     `json:"keys,omitempty"`
}

// ModelSnapshot are the counters of a single model.
type ModelSnapshot struct {
	Model    string `json:"model"`
	OK       int64  `json:"ok"`
	Refused  int64  `json:"refused"`
	Aborted  int64  `json:"aborted"`
	LastSeen int64  `json:"last_seen,omitempty"`
}

// AddressSnapshot are the counters of a single egress address.
//
// It carries its own totals and its own per-model breakdown: that pair is what
// the terminal matrix draws, and a caller must not have to reassemble it from
// JSON himself.
type AddressSnapshot struct {
	Address string          `json:"address"`
	Family  string          `json:"family,omitempty"`
	OK      int64           `json:"ok"`
	Refused int64           `json:"refused"`
	Aborted int64           `json:"aborted"`
	ByModel []ModelSnapshot `json:"by_model,omitempty"`
	// FirstSeen is the moment this address first carried a request. Zero means
	// the mark predates the field.
	FirstSeen int64 `json:"first_seen,omitempty"`
	// LastSeen is the moment it last carried one.
	LastSeen int64 `json:"last_seen,omitempty"`
}

// KeySnapshot are the counters of a single key.
type KeySnapshot struct {
	Fingerprint string `json:"fingerprint"`
	OK          int64  `json:"ok"`
	Refused     int64  `json:"refused"`
	Aborted     int64  `json:"aborted"`
	// LastSeen is the moment the key was last used. Zero means it never was.
	LastSeen int64 `json:"last_seen,omitempty"`
	// LastRefusalAt is the moment of the last refusal for this key.
	LastRefusalAt int64 `json:"last_refusal_at,omitempty"`
}

// Snapshot returns a snapshot for reading.
//
// The order is given explicitly: days from the newest to the oldest, models and
// keys alphabetically, addresses from the first used to the last. Without an
// order stated here the output would change between calls, and comparing two
// snapshots would be impossible.
//
// THE ADDRESSES ARE IN USE ORDER, NOT ALPHABETICAL. The egress address changes
// when the uplink is switched, by hand or by a route, and the addresses of a day
// then read as the sequence of uplinks that carried it. Alphabetical order threw
// that sequence away for a property nobody was asking about, and put a new
// address wherever its digits happened to sort.
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
		day := DaySnapshot{Day: key, OK: d.OK, Refused: d.Refused, Aborted: d.Aborted}

		day.ByModel = modelSnapshots(d.ByModel)

		addresses := make([]string, 0, len(d.ByAddress))
		for address := range d.ByAddress {
			addresses = append(addresses, address)
		}
		// The address string breaks a tie: two addresses first seen in the same
		// millisecond — which is what a restored state file or a clock with a
		// coarse resolution produces — must still come out in one fixed order.
		sort.Slice(addresses, func(i, j int) bool {
			a, b := d.ByAddress[addresses[i]], d.ByAddress[addresses[j]]
			if a.FirstSeen != b.FirstSeen {
				return a.FirstSeen < b.FirstSeen
			}
			return addresses[i] < addresses[j]
		})
		for _, address := range addresses {
			a := d.ByAddress[address]
			day.ByAddress = append(day.ByAddress, AddressSnapshot{
				Address:   address,
				Family:    a.Family,
				OK:        a.OK,
				Refused:   a.Refused,
				Aborted:   a.Aborted,
				ByModel:   modelSnapshots(a.ByModel),
				FirstSeen: a.FirstSeen,
				LastSeen:  a.LastSeen,
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
				Fingerprint: fp, OK: k.OK, Refused: k.Refused, Aborted: k.Aborted,
				LastSeen: k.LastSeen, LastRefusalAt: k.LastRefusalAt,
			})
		}

		out.Days = append(out.Days, day)
	}
	return out
}

// modelSnapshots converts a by-model map into an alphabetically ordered list.
//
// The order is given explicitly: without it the output would change between
// calls, and comparing two snapshots would be impossible.
func modelSnapshots(byModel map[string]*ModelCounters) []ModelSnapshot {
	models := make([]string, 0, len(byModel))
	for name := range byModel {
		models = append(models, name)
	}
	sort.Strings(models)

	out := make([]ModelSnapshot, 0, len(models))
	for _, name := range models {
		m := byModel[name]
		out = append(out, ModelSnapshot{
			Model: name, OK: m.OK, Refused: m.Refused, Aborted: m.Aborted, LastSeen: m.LastSeen,
		})
	}
	return out
}

// dayKey returns the key of a day.
//
// The date is taken in UTC: providers reset their daily limits by UTC, and a
// local date would diverge from their day boundary.
func dayKey(t time.Time) string { return t.UTC().Format("2006-01-02") }

// addressFamily derives the family of an egress address.
//
// The family is read off the literal, not off net.IP.To4: a v4-mapped IPv6
// literal ("::ffff:1.2.3.4") is an IPv6 string, and the provider's limiter
// buckets by the string, so folding it in with plain v4 addresses would hide
// one of the buckets the operator is up against.
//
// An address that does not parse yields no family at all. An empty cell is
// honest; a guessed "v6" is not.
func addressFamily(address string) string {
	ip := net.ParseIP(address)
	if ip == nil {
		return ""
	}
	if strings.IndexByte(address, ':') >= 0 {
		return "v6"
	}
	if ip.To4() != nil {
		return "v4"
	}
	return ""
}
