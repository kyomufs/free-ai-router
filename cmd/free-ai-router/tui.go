package main

import (
	"os"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"free-ai-router/internal/config"
	"free-ai-router/internal/state"
	"free-ai-router/internal/tui"
)

// cmdTUI starts the terminal monitor.
//
// The monitor reads; it never writes. Every value it displays is declared once,
// in the binary's axes, and set through flags or through the host's
// configuration — so there is no path from this command to a setting, and the
// help text says as much.
//
// ON THE SCREEN. The monitor runs full-screen by default: it takes the alternate
// screen, so the blocks keep their shape as readings arrive and the terminal is
// restored on exit. --scrollback is the other behaviour, kept because there is
// one thing full screen cannot do — leave the frame in the terminal afterwards,
// where it can be selected, copied and quoted. Neither mode is a fallback for
// the other; they answer different questions about the same run.
func cmdTUI(args []string) error {
	scrollback := false
	rest := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "--scrollback" {
			scrollback = true
			continue
		}
		rest = append(rest, arg)
	}

	cfg, err := loadConfigSimple(rest)
	if err != nil || cfg == nil {
		return err
	}

	model := tui.New(newTUISource(cfg))
	model.Scrollback = scrollback
	program := tea.NewProgram(model)
	_, err = program.Run()
	return err
}

// tuiSource serves readings to the monitor.
type tuiSource struct {
	cfg *config.Config
}

// newTUISource assembles the data source.
//
// It never fails. The monitor has to open even when the configuration does not
// build — an operator looking at a broken deployment needs to see WHICH value is
// wrong, and a command that refused to start would tell them nothing.
func newTUISource(cfg *config.Config) *tuiSource {
	return &tuiSource{cfg: cfg}
}

// Observe implements tui.DataSource.
//
// The day is resolved HERE and reported back, rather than left for the model to
// compare against a list: what the state holds is this side's knowledge, and a
// model that had to decide for itself whether a day exists would be a second copy
// of the retention rule.
func (s *tuiSource) Observe(day string) (tui.Observation, error) {
	obs := tui.Observation{At: time.Now()}
	cfg := s.cfg

	obs.Daemon = []tui.Row{
		{Name: "mode", Value: cfg.Mode.Val, Source: string(cfg.Mode.From)},
		{Name: "listen", Value: cfg.Listen.Val, Source: string(cfg.Listen.From)},
		{Name: "transport", Value: cfg.Transport.Val, Source: string(cfg.Transport.From)},
		{Name: "upstream", Value: cfg.Upstream.Val, Source: string(cfg.Upstream.From)},
		{Name: "credential", Value: cfg.Credential.Val, Source: string(cfg.Credential.From)},
		{Name: "key_rotation", Value: cfg.KeyRotation.Val, Source: string(cfg.KeyRotation.From)},
		{Name: "family", Value: cfg.Family.Val, Source: string(cfg.Family.From)},
	}

	obs.LogPath = cfg.Paths.LogFile
	obs.Uptime = daemonUptime(cfg.Paths.PIDFile)

	// The counters. A state file that cannot be opened is a note rather than a
	// failure: the monitor still has the configuration and the journal to show,
	// and refusing to start would hide them.
	snap, err := readSnapshot(cfg.Paths.StateFile)
	if err != nil {
		obs.Notes = append(obs.Notes, "state file: "+err.Error())
	}

	// The shown day. A day the state no longer holds — the retention window has
	// passed, or the file was rotated away — falls back to the daemon's own day,
	// with a note: silently answering a question about the 3rd of last month with
	// today's numbers would make the frame wrong about the one thing it is for.
	shown := daySnapshot(snap, day)
	if snap != nil {
		obs.Today = snap.Today
		obs.Days = dayKeys(snap)
		obs.Day = snap.Today
		if shown != nil {
			obs.Day = shown.Day
		} else if day != "" {
			obs.Notes = append(obs.Notes, "no counters for "+day+
				" (the state keeps "+strconv.Itoa(state.RetentionDays())+" days); showing "+obs.Day)
		}
	}

	obs.Providers = s.providers(snap, obs.Day)
	obs.Addresses, obs.Totals = matrix(snap, obs.Day)

	lines, err := state.Tail(cfg.Paths.LogFile, tailLines)
	if err != nil && !isMissing(err) {
		obs.Notes = append(obs.Notes, "journal: "+err.Error())
	}
	obs.Logs = lines

	if len(lines) == 0 && err == nil {
		obs.Notes = append(obs.Notes, "the journal is empty; the daemon writes to "+cfg.Paths.LogFile)
	}

	return obs, nil
}

// daySnapshot is the day of counters to show, nil when the state holds none.
//
// An empty want means the daemon's own day, which is what makes the monitor
// follow the daemon across midnight instead of pinning a key of its own.
func daySnapshot(snap *state.Snapshot, want string) *state.DaySnapshot {
	if snap == nil {
		return nil
	}
	if want == "" {
		want = snap.Today
	}
	for i := range snap.Days {
		if snap.Days[i].Day == want {
			return &snap.Days[i]
		}
	}
	return nil
}

// dayKeys lists the recorded days, newest first.
//
// The order is the state's own and is passed through unchanged: it is the order
// the selector walks, and re-sorting here would leave the model's position and
// the source's list able to disagree.
func dayKeys(snap *state.Snapshot) []string {
	if snap == nil {
		return nil
	}
	days := make([]string, 0, len(snap.Days))
	for _, day := range snap.Days {
		days = append(days, day.Day)
	}
	return days
}

// tailLines is how many journal lines a reading keeps.
//
// The monitor's own bound, deliberately smaller than what a command would read:
// a reading happens every couple of seconds, and a longer tail would be redrawn
// over and over for no one's benefit.
const tailLines = 500

// daemonUptime is how long the daemon has been running.
//
// THE PID FILE'S AGE IS THE UPTIME. The daemon writes it once at startup and
// removes it on the way out, so the file's modification time is the moment it
// took over the port — a stamp no other artifact in the deployment carries.
//
// The age is only reported when the process the file names is still alive, and
// only when it is in the past: an unclean shutdown leaves the file behind, and
// the age of that leftover is a number about a period during which nothing was
// running. Zero means "not known", which the monitor draws as a placeholder
// rather than as a daemon that started this instant.
func daemonUptime(path string) time.Duration {
	pid, err := findDaemonPID(path)
	if err != nil || pid == 0 {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	if d := time.Since(info.ModTime()); d > 0 {
		return d
	}
	return 0
}

// readSnapshot reads the counters.
func readSnapshot(path string) (*state.Snapshot, error) {
	st, err := state.Open(path)
	if err != nil && st == nil {
		return nil, err
	}
	snap := st.Snapshot()
	return &snap, nil
}

// providers turns the configuration and the counters into the key block.
//
// The counters are read from the day being shown, which is the daemon's own day
// until the reader moves the selector: the key pool is a property of the
// deployment, but its counters are per day like everything else, and a past day
// has to show the keys as they were used then.
func (s *tuiSource) providers(snap *state.Snapshot, day string) []tui.ProviderState {
	counters := map[string]state.KeySnapshot{}
	if shown := daySnapshot(snap, day); shown != nil {
		for _, k := range shown.Keys {
			counters[k.Fingerprint] = k
		}
	}

	rotate := s.cfg.KeyRotation.Val == "on"

	var out []tui.ProviderState
	for _, id := range providerNames(s.cfg) {
		pc := s.cfg.Providers[id]
		p := tui.ProviderState{
			ID:         id,
			Type:       pc.Type,
			Credential: s.cfg.Credential.Val,
			RotationOn: rotate,
		}

		var newest time.Time
		for _, raw := range keysFromConfig(pc) {
			str, ok := raw.(string)
			if !ok {
				continue
			}
			fp := fingerprint(str)
			if fp == "" {
				continue
			}
			k := tui.KeyState{Fingerprint: fp}
			if c, ok := counters[fp]; ok {
				k.OK, k.Refused, k.Aborted = c.OK, c.Refused, c.Aborted
				k.LastSeen = millis(c.LastSeen)
				k.LastRefusal = millis(c.LastRefusalAt)
			}
			// The key in use is the one used most recently. The daemon holds the
			// rotating pointer in memory and the monitor is a separate process,
			// so the pointer is inferred, not read — and the block says so.
			if !k.LastSeen.IsZero() && k.LastSeen.After(newest) {
				newest, p.Active = k.LastSeen, fp
			}
			p.Keys = append(p.Keys, k)
		}
		out = append(out, p)
	}

	if len(out) == 0 {
		out = append(out, tui.ProviderState{})
	}
	return out
}

// matrix turns the counters into the per-address rows.
//
// The row order is the state's own — first used to last — and it is passed
// through: the order of the addresses is the order the uplink was switched in, and
// the monitor's own sort modes are applied to a copy of this list.
func matrix(snap *state.Snapshot, day string) ([]tui.AddressRow, tui.Totals) {
	shown := daySnapshot(snap, day)
	if shown == nil {
		return nil, tui.Totals{}
	}

	tot := tui.Totals{Day: shown.Day, OK: shown.OK, Refused: shown.Refused, Aborted: shown.Aborted}

	var rows []tui.AddressRow
	for _, a := range shown.ByAddress {
		row := tui.AddressRow{
			Address:   a.Address,
			Family:    a.Family,
			OK:        a.OK,
			Error:     a.Refused,
			Aborted:   a.Aborted,
			FirstSeen: millis(a.FirstSeen),
			LastSeen:  millis(a.LastSeen),
		}
		for _, m := range a.ByModel {
			row.Models = append(row.Models, tui.ModelRow{
				Model:   m.Model,
				OK:      m.OK,
				Error:   m.Refused,
				Aborted: m.Aborted,
			})
		}
		rows = append(rows, row)
	}
	return rows, tot
}

// millis converts a stored millisecond stamp into a time.
func millis(v int64) time.Time {
	if v <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(v)
}

// isMissing reports whether an error is a file that is not there.
func isMissing(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "no such file") || strings.Contains(msg, "not exist")
}
