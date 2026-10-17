// Package tui is the built-in terminal monitor.
//
// WHAT IT IS. A monitor, and deliberately nothing more. It reads; it never
// writes. The reason is not caution, it is the project's rule about where
// settings live: every value is declared once, in the binary's own axes, and is
// set through the flags on any distribution or through the configuration on a
// managed host. An interface that also edited settings would be a second place
// where they can be changed, and the two would drift — a value changed in the
// monitor would look permanent and be gone at the next activation, because the
// configuration file belongs to the host's manager.
//
// So the monitor answers four questions, in four blocks:
//
//   - WHICH KEY IS IN USE, and whether key rotation is on.
//   - HOW MUCH EACH ADDRESS HAS SPENT, per model — the matrix. This is the block
//     that exists: the provider's anonymous tier is limited per address rather
//     than per key, so a single total cannot tell a spent address from a spent
//     key, and the address is the only row key that can.
//   - WHAT HAPPENED — the journal, scrollable, filterable, wrap togglable.
//   - WHAT THE DAEMON IS — the axes in effect and where each value came from.
//
// WHY IT IS BUILT IN rather than a script: an external wrapper may be absent,
// may need other people's programs, and may disagree with the binary about the
// catalogs. An interface inside the binary has none of those properties.
//
// WHY FULL SCREEN BY DEFAULT, AND WHY THAT IS NOT A REGRESSION. The monitor
// takes the alternate screen, so it behaves like an application rather than like
// a command that prints: the blocks keep their shape, the layout is stable, and
// the terminal is restored exactly as it was on exit. The earlier behaviour —
// printing into the scrollback so that the output could be quoted — is still
// available under --scrollback, because it is the right choice when the monitor
// is run to capture what it shows rather than to be watched. The two are one
// flag apart, and the flag is named for the property it controls.
package tui

import (
	"time"

	"charm.land/bubbles/v2/help"
	tea "charm.land/bubbletea/v2"
)

// Block is one of the monitor's panels.
type Block int

const (
	// BlockKeys is the key block: the pool, the active key, the rotation state.
	BlockKeys Block = iota
	// BlockMatrix is the per-address, per-model matrix.
	BlockMatrix
	// BlockLogs is the journal.
	BlockLogs
)

// String returns the block name.
func (b Block) String() string {
	switch b {
	case BlockKeys:
		return "keys"
	case BlockMatrix:
		return "addresses"
	case BlockLogs:
		return "log"
	default:
		return "unknown"
	}
}

// blockCount is how many blocks the monitor has.
const blockCount = 3

// DataSource supplies a reading of the daemon's state.
//
// One method rather than one per block: the blocks describe one moment, and
// three separate calls could return three moments — a key counted in the key
// block and not in the matrix, with no way for the reader to tell which is
// current.
type DataSource interface {
	// Observe returns one reading.
	Observe() (Observation, error)
}

// Observation is a single reading of everything the monitor shows.
type Observation struct {
	// At is the moment of the reading.
	At time.Time
	// Daemon describes the deployment: the axes in effect, with their sources.
	Daemon []Row
	// Providers are the providers, their credentials and their key pools.
	Providers []ProviderState
	// Addresses are the matrix rows: one per egress address.
	Addresses []AddressRow
	// Totals are the counters for the current day.
	Totals Totals
	// Logs are the tail of the journal, oldest line first.
	Logs []string
	// LogPath is where the journal lives, so that a reader can find it.
	LogPath string
	// Notes are facts the reader should not have to infer: an unreachable
	// daemon, a missing journal, a provider whose catalog was not asked for.
	Notes []string
}

// Row is a name and a value, with the origin of the value.
type Row struct {
	// Name is the axis name.
	Name string
	// Value is the value in effect.
	Value string
	// Source is where the value came from: a flag, the environment, the file, or
	// a built-in default.
	Source string
}

// ProviderState is one provider as the monitor shows it.
type ProviderState struct {
	// ID is the provider's identifier.
	ID string
	// Type is the constructor used for it.
	Type string
	// Credential is the axis value: "anonymous" or "account".
	Credential string
	// RotationOn is the key-rotation axis: whether the key may be swapped on a
	// refusal. It is shown even when there is nothing to swap, because "off" and
	// "nothing to rotate" are different facts and only one of them is a decision.
	RotationOn bool
	// Keys is the pool, in rotation order.
	Keys []KeyState
	// Active is the fingerprint of the key in use, empty when unknown.
	//
	// It is inferred from the counters rather than read from the daemon: the
	// monitor is a separate process, and the rotating pointer lives in the
	// daemon's memory. The inferring is sound for a key that was used at least
	// once, and the block says so instead of pretending otherwise.
	Active string
}

// KeyState is one key of the pool.
type KeyState struct {
	// Fingerprint identifies the key without printing it.
	Fingerprint string
	// OK, Refused and Aborted are its counters.
	OK, Refused, Aborted int64
	// LastSeen is when it was last used. Zero means never.
	LastSeen time.Time
	// LastRefusal is when it was last refused. Zero means never.
	LastRefusal time.Time
}

// AddressRow is one row of the matrix: one egress address.
type AddressRow struct {
	// Address is the egress address as the provider sees it.
	Address string
	// Family is "v4" or "v6".
	Family string
	// OK is the number of requests the provider answered.
	OK int64
	// Error is the number of requests the provider refused.
	//
	// "Refused" in the counters and "ERROR" in the column: the column name is the
	// one the operator reads, the field name is the one the counter has.
	Error int64
	// Aborted is the number of requests that ended inside the gateway or were
	// cancelled by the client. It is kept apart from Error on purpose: an abort
	// is not a signal from the provider, and adding it to the refusals would make
	// a spent address and a broken client look alike.
	Aborted int64
	// Models are the per-model counters of this address.
	Models []ModelRow
	// LastSeen is when the address was last used.
	LastSeen time.Time
}

// ModelRow is one model's counters under one address.
type ModelRow struct {
	// Model is the model identifier.
	Model string
	// OK is the number of answers.
	OK int64
	// Error is the number of refusals.
	Error int64
	// Aborted is the number of aborts.
	Aborted int64
}

// Totals are the counters of the day.
type Totals struct {
	// Day is the date the counters belong to.
	Day string
	// OK, Refused and Aborted are the totals.
	OK, Refused, Aborted int64
}

// readMsg carries a completed reading.
type readMsg struct {
	obs Observation
	err error
}

// Model is the monitor's state.
type Model struct {
	// Source supplies the readings.
	Source DataSource

	// obs is the current reading.
	obs Observation
	// err is the last reading failure, if any.
	err error
	// loaded is whether a reading has arrived.
	loaded bool

	// Focus is the block that receives the movement keys.
	Focus Block
	// Scroll is the line offset of each block.
	Scroll [blockCount]int
	// Follow pins the log block to its end while new lines arrive.
	Follow bool
	// Wrap is whether the log block wraps long lines.
	Wrap bool
	// filter is the current log filter.
	filter string
	// editing is whether the filter is being typed.
	editing bool
	// filterDraft is the filter being typed.
	filterDraft string
	// filterBefore is the filter that was in force when the editor opened, kept
	// so that a cancelled edit can be undone.
	filterBefore string
	// filtered holds the log lines after the filter is applied.
	filtered []logLine

	// pal is the style set, rebuilt when the terminal reports its background.
	pal palette

	// width and height are the terminal size.
	width, height int
	// quitting marks that an exit has been requested.
	quitting bool

	// Scrollback keeps the output in the terminal's scrollback instead of taking
	// the alternate screen.
	//
	// It is set once, from the command line, and it is a property of the RUN
	// rather than of the reading: an operator watching the monitor wants it
	// framed in place, while one capturing its output wants the text to survive
	// the exit. Neither is more correct, so the choice is explicit.
	Scrollback bool

	// help renders the key hints at the bottom of the frame.
	help help.Model
	// keys declares what the monitor binds.
	keys keyMap
}

// logLine is a filtered log line with its position in the source list.
type logLine struct {
	// text is the line.
	text string
	// index is its position in the unfiltered log.
	index int
}

// New creates the monitor.
func New(source DataSource) Model {
	h := help.New()
	// More keys than there is room for on one line: pressing ? is what shows the
	// rest, and the bar says so rather than hiding them silently.
	h.ShowAll = false

	return Model{
		Source: source,
		Follow: true,
		Wrap:   true,
		// The dark palette until the terminal says otherwise: that is what a
		// terminal normally is, and the answer arrives in the first exchange.
		pal:  newPalette(true),
		help: h,
		keys: newKeyMap(),
	}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.read(), tea.RequestBackgroundColor)
}

// read returns the command that performs a reading.
func (m Model) read() tea.Cmd {
	source := m.Source
	return func() tea.Msg {
		if source == nil {
			return readMsg{}
		}
		obs, err := source.Observe()
		return readMsg{obs: obs, err: err}
	}
}

// tailLimit is how many journal lines are read.
//
// A few hundred rather than the whole file: the journal is rotated by size, and
// the reader is looking at what just happened. The filter runs over what was
// read, and it is named in the block so that the bound is never a silent one.
const tailLimit = 500
