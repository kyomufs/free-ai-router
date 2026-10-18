package main

import (
	"os"
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
func (s *tuiSource) Observe() (tui.Observation, error) {
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

	obs.Providers = s.providers(snap)
	obs.Addresses, obs.Totals = matrix(snap)

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
func (s *tuiSource) providers(snap *state.Snapshot) []tui.ProviderState {
	// The counters are read from the day the state file calls "today" rather
	// than from today's date: the two agree except across midnight, and the file
	// is the one that knows which bucket the daemon was writing into.
	counters := map[string]state.KeySnapshot{}
	if snap != nil {
		for _, day := range snap.Days {
			if day.Day != snap.Today {
				continue
			}
			for _, k := range day.Keys {
				counters[k.Fingerprint] = k
			}
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
func matrix(snap *state.Snapshot) ([]tui.AddressRow, tui.Totals) {
	if snap == nil {
		return nil, tui.Totals{}
	}

	var (
		rows []tui.AddressRow
		tot  tui.Totals
	)
	for _, day := range snap.Days {
		if day.Day != snap.Today {
			continue
		}
		tot = tui.Totals{Day: day.Day, OK: day.OK, Refused: day.Refused, Aborted: day.Aborted}

		for _, a := range day.ByAddress {
			row := tui.AddressRow{
				Address:  a.Address,
				Family:   a.Family,
				OK:       a.OK,
				Error:    a.Refused,
				Aborted:  a.Aborted,
				LastSeen: lastSeen(a.ByModel),
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
	}
	return rows, tot
}

// lastSeen is the newest model mark under an address.
//
// The address row has no mark of its own, and the newest of its models is the
// honest substitute: it answers "when was this address last used" without
// inventing a fourth counter for it.
func lastSeen(models []state.ModelSnapshot) time.Time {
	var newest time.Time
	for _, m := range models {
		if t := millis(m.LastSeen); t.After(newest) {
			newest = t
		}
	}
	return newest
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
