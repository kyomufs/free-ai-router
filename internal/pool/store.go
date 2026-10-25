package pool

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// StateVersion is the shape of pool.json. A file with another version is ignored
// rather than half-read: the list is regenerable, and a wrong parse is not.
const StateVersion = 1

// State is what survives a restart.
type State struct {
	Version     int       `json:"version"`
	EvaluatedAt time.Time `json:"evaluated_at"`
	NextEvalAt  time.Time `json:"next_eval_at"`
	EvalState   string    `json:"eval_state"`
	// Active is the server that was serving when the state was written. It is a
	// fact about the last run, not a preference: a restarted daemon serves through
	// the same node until that node fails or the list is rebuilt.
	Active      string  `json:"active,omitempty"`
	Nodes       []Node  `json:"nodes,omitempty"`
	Quarantined []Entry `json:"quarantined,omitempty"`
	// Source and Last are what the last pass was, so a restarted daemon can still
	// say where its list came from and how many servers the pass left.
	Source string   `json:"source,omitempty"`
	Last   *LastRun `json:"last,omitempty"`
}

// Load reads the pool state.
//
// A missing, truncated or foreign file yields an empty state and no error: the
// daemon must start and re-evaluate rather than refuse to run because a cache it
// can rebuild is unreadable.
func Load(path string) (State, error) {
	empty := State{Version: StateVersion}
	if path == "" {
		return empty, nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return empty, nil
		}
		return empty, err
	}
	var s State
	if err := json.Unmarshal(body, &s); err != nil || s.Version != StateVersion {
		return empty, nil
	}
	s.Nodes = restoreNodes(s.Nodes)
	for i := range s.Quarantined {
		if r, ok := restoreOne(s.Quarantined[i].Node); ok {
			s.Quarantined[i].Node = r
		}
	}
	return s, nil
}

// Save writes the state atomically, so a killed process cannot leave a half
// written pool behind.
func Save(path string, s State) error {
	if path == "" {
		return errors.New("pool: no state path")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".pool-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// NextEvaluation is the next slot of the day's schedule.
//
// The schedule is a list of instants rather than a cron expression: three fixed
// times need no dependency, and the first one is tied to the provider's quota
// reset instead of to a machine's uptime.
func NextEvaluation(now, first time.Time, interval time.Duration) time.Time {
	if interval <= 0 {
		interval = 8 * time.Hour
	}
	now = now.UTC()
	slots := int((24 * time.Hour) / interval)
	if slots < 1 {
		slots = 1
	}
	anchor := first.UTC()
	if anchor.IsZero() {
		anchor = time.Date(now.Year(), now.Month(), now.Day(), 3, 0, 0, 0, time.UTC)
	}
	// The anchor's clock time on today's date, so that a daemon started weeks
	// after the anchor still lands on today's slots.
	base := time.Date(now.Year(), now.Month(), now.Day(), anchor.Hour(), anchor.Minute(), anchor.Second(), 0, time.UTC)
	for day := 0; day < 2; day++ {
		for i := 0; i < slots; i++ {
			candidate := base.Add(time.Duration(i)*interval + time.Duration(day)*24*time.Hour)
			if candidate.After(now) {
				return candidate
			}
		}
	}
	return base.Add(24*time.Hour + (24 * time.Hour) - interval)
}

// restoreNodes rebuilds what a node cannot carry in JSON: the renderer's view of
// its link. A node whose link no longer parses is dropped, because a node without
// a renderable config cannot serve.
func restoreNodes(nodes []Node) []Node {
	out := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		if r, ok := restoreOne(n); ok {
			out = append(out, r)
		}
	}
	return out
}

// restoreOne keeps the measurements, which the profile does not carry, and drops a
// node whose stored profile the core can no longer render.
func restoreOne(n Node) (Node, bool) {
	if _, ok := clashProxy(n.Raw); !ok {
		return Node{}, false
	}
	return n, true
}
