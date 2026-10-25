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
// So the monitor answers its questions on two screens:
//
//   - THE DASHBOARD — the whole deployment as tiles. What the day's traffic came
//     to; what the daemon is and where each of its settings came from; which
//     providers exist, under which credential, with which keys and whether a
//     refusal rotates them; and how much every egress address has spent, per
//     model. The address matrix is the part that exists for a reason: the
//     provider's anonymous tier is limited per address rather than per key, so a
//     single total cannot tell a spent address from a spent key, and the address
//     is the only row key that can.
//   - THE JOURNAL — what happened, scrollable, filterable, wrap togglable.
//
// The keys used to have a screen of their own. They do not any more: a key is a
// property of a provider, the reader who asks about a key is asking about the
// provider it belongs to, and a separate screen made the reader hold the two
// halves of one answer in their head.
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

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
)

// Block is one of the monitor's panels.
type Block int

const (
	// BlockDashboard is the deployment as tiles: the day's counters, the daemon's
	// axes, the providers with their keys, and the address matrix.
	BlockDashboard Block = iota
	// BlockLogs is the journal.
	BlockLogs
)

// String returns the block name.
//
// The name is what the tab strip draws and what the frame's own title repeats,
// so it is also the name the reader will use when describing what they were
// looking at.
func (b Block) String() string {
	switch b {
	case BlockDashboard:
		return "dashboard"
	case BlockLogs:
		return "log"
	default:
		return "unknown"
	}
}

// blockCount is how many blocks the monitor has.
const blockCount = 2

// DataSource supplies a reading of the daemon's state.
//
// One method rather than one per block: the blocks describe one moment, and
// three separate calls could return three moments — a key counted in the key
// block and not in the matrix, with no way for the reader to tell which is
// current.
type DataSource interface {
	// Observe returns one reading of the counters for the day named by day, or
	// of the current day when day is empty.
	//
	// THE DAY IS AN ARGUMENT because it is a question the reader asks, not a
	// property of the source: "what did the daemon do on the 12th" is one reading
	// from one state file. A source that always returned today and a model that
	// picked the day out of it afterwards would have to keep two copies of every
	// list to answer the same question.
	Observe(day string) (Observation, error)
}

// Observation is a single reading of everything the monitor shows.
type Observation struct {
	// At is the moment of the reading.
	At time.Time
	// Uptime is how long the daemon has been up, and zero when that is not
	// known — no daemon is running, or its start could not be established.
	//
	// It is a reading of the deployment rather than of the frame, so it is
	// reported here and not measured in the view: the monitor is a separate
	// process, and how long IT has been running says nothing about the daemon.
	Uptime time.Duration
	// Daemon describes the deployment: the axes in effect, with their sources.
	Daemon []Row
	// Providers are the providers, their credentials and their key pools.
	Providers []ProviderState
	// Addresses are the matrix rows: one per egress address.
	Addresses []AddressRow
	// Totals are the counters for the day.
	Totals Totals
	// Day is the day the counters describe, as the state keys it. It is the day
	// that was READ, which is the day that was asked for whenever the state
	// still holds it.
	Day string
	// Today is the day the daemon is writing into.
	//
	// Read from the reading rather than derived in the view: the day key is the
	// state's own, and a view that computed it from its own clock would disagree
	// with the counters for the minutes after midnight — which is exactly when a
	// reader is comparing two days.
	Today string
	// Days are the days the state holds counters for, newest first. It is what
	// bounds the day selector: a reader can only be moved to a day that exists.
	Days []string
	// Logs are the tail of the journal, oldest line first.
	Logs []string
	// LogPath is where the journal lives, so that a reader can find it.
	LogPath string
	// Pool is the pool transport's list, nil when that transport is not in use.
	Pool *PoolState
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
	// FirstSeen is when the address first carried a request, and zero when the
	// mark predates it.
	//
	// It is what the default order uses: the address changes when the operator
	// switches the uplink, so the order the addresses appeared in is the order of
	// the switches.
	FirstSeen time.Time
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
	// manual marks that the reading in flight was asked for by the reader, so
	// that it is confirmed when it lands.
	//
	// A reading the reader asked for and an automatic one are the same message by
	// the time they arrive, and only the first deserves a toast: a reading that
	// was asked for has to say it happened, because the frame may be identical to
	// the one before it.
	manual bool

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

	// spinner animates the loading state. It is the one widget the monitor owns
	// that is not interactive: it exists so that a wait is visible as a wait.
	spinner spinner.Model

	// width and height are the terminal size.
	width, height int
	// quitting marks that an exit has been requested.
	quitting bool

	// geom is where every band of the frame sits at the current size.
	//
	// Computed once per resize and read by both the view and the hit tests. That
	// sharing is the point: the click handler used to recompute the layout by
	// adding up the heights of the elements above the blocks, so every new
	// element silently moved the thing a click landed on.
	geom Geometry

	// overlay is the modal in front of the frame, nil when there is none.
	//
	// A pointer rather than a boolean plus a field: the modal carries its own
	// content — which line was opened — and two fields that must agree are two
	// fields that can disagree.
	overlay *overlay

	// sort is the order the address matrix is shown in.
	sort sortMode

	// day is the day the reader asked for, empty to follow the current one.
	//
	// EMPTY IS NOT "TODAY", and the difference is the whole reason this is not a
	// day key initialised at construction: a monitor left open across midnight
	// must follow the daemon into the new day, and a pinned key would leave it
	// showing a day that ended. What the READING returned is obs.Day, so a day
	// the reader never touched is re-resolved on every tick.
	day string

	// toast is the transient confirmation shown in place of the metrics, nil
	// when there is nothing to confirm.
	toast *toast
	// toastSeq numbers the confirmations raised, so that an expiring one can be
	// told from the one that replaced it.
	toastSeq int

	// selected is the index into the filtered journal of the line the details
	// key would open. -1 means nothing is selected.
	selected int

	// dayLines is how many journal lines the selected day left, before the text
	// filter ran. It is what tells an empty block that the tail does not reach
	// the day from one whose lines were all filtered out.
	dayLines int

	// Scrollback keeps the output in the terminal's scrollback instead of taking
	// the alternate screen.
	//
	// It is set once, from the command line, and it is a property of the RUN
	// rather than of the reading: an operator watching the monitor wants it
	// framed in place, while one capturing its output wants the text to survive
	// the exit. Neither is more correct, so the choice is explicit.
	Scrollback bool

	// now is the clock the frame is drawn with.
	//
	// Held in the model rather than read from the clock inside View: a view that
	// calls time.Now is not a function of the state, and the banner's clock is
	// exactly the kind of thing a test would otherwise be unable to assert on.
	// It is advanced by the refresh tick, which is why the clock is never more
	// than one tick behind.
	now time.Time

	// keys declares what the monitor binds.
	keys keyMap
}

// overlayKind is which modal is open.
type overlayKind int

const (
	// overlayDetails is the journal line the reader opened.
	overlayDetails overlayKind = iota
	// overlayAxes is the full list of the deployment's axes.
	overlayAxes
	// overlayHelp is the full list of the monitor's bindings.
	overlayHelp
)

// overlay is the open modal.
//
// It carries what it needs to render rather than pointing back at the model: the
// details modal is opened on a line whose position in the filtered journal is not
// the position it has in the log, and the index is resolved once, when the modal
// opens, instead of being re-derived on every frame from two lists that may have
// moved apart since.
type overlay struct {
	// kind is which modal this is.
	kind overlayKind
	// index is the opened line's position in the unfiltered journal, for
	// overlayDetails.
	index int
}

// sortMode is the order of the address matrix.
type sortMode int

const (
	// sortUsed is the default: the order the addresses were first used in. The
	// egress address changes when the operator switches the uplink — a VPN, a
	// tethered modem — so the order is the sequence of switches, and the address
	// in use now is the last row.
	//
	// It replaced "most requests first" as the default because that order answers
	// the wrong question at the moment the matrix is looked at: the address that
	// was just switched away from has the most traffic of the day, so it stays on
	// top and the new one is pushed down the table, which reads as the switch not
	// having been noticed.
	sortUsed sortMode = iota
	// sortTraffic is most requests first: which address carried the day.
	sortTraffic
	// sortErrors puts the addresses that refuse first — the question "is
	// anything broken" is the other one worth asking of a matrix.
	sortErrors
	// sortRecent puts the most recently used first, which is how a spent
	// credential or a rotated address is spotted.
	sortRecent
	// sortAddress is alphabetical, for finding a known address among many.
	sortAddress
	// sortModeCount is how many modes there are.
	sortModeCount
)

// String is the mode's name, as the frame shows it.
func (s sortMode) String() string {
	switch s {
	case sortTraffic:
		return "traffic"
	case sortErrors:
		return "errors"
	case sortRecent:
		return "recent"
	case sortAddress:
		return "address"
	default:
		return "used"
	}
}

// next is the following mode, wrapping around.
func (s sortMode) next() sortMode { return (s + 1) % sortModeCount }

// toast is a transient confirmation.
//
// It is shown in place of the metrics line rather than beside them, and it is
// what the refresh key answers with: a key that re-reads the daemon must say
// that it did, or the reader cannot tell a refresh from a key that did nothing.
// The timestamp is captured with the message so that the text does not change
// while it is on screen.
type toast struct {
	// text is the message.
	text string
	// err marks a failure rather than a confirmation.
	err bool
	// at is the moment the message was raised.
	at time.Time
	// seq identifies this toast.
	//
	// A counter rather than the timestamp, which was what this used: the clock
	// here is the READING's clock, and two confirmations raised inside one
	// reading — a refresh pressed twice, an automatic read landing in between —
	// would share a stamp, so the first one's timer would dismiss the second one
	// almost immediately.
	seq int
}

// toastMsg dismisses the toast whose sequence number is seq.
//
// The number is compared rather than the toast being dropped unconditionally: a
// second toast raised before the first one's timer fires must not be cut short
// by the first one's expiry.
type toastMsg struct {
	seq int
}

// toastFor is how long a confirmation stays on screen.
const toastFor = 3 * time.Second

// raiseToast returns the model with a toast raised and the command that will
// clear it.
func (m Model) raiseToast(text string, err bool) (Model, tea.Cmd) {
	m.toastSeq++
	t := &toast{text: text, err: err, at: m.now, seq: m.toastSeq}
	m.toast = t
	seq := t.seq
	return m, tea.Tick(toastFor, func(time.Time) tea.Msg { return toastMsg{seq: seq} })
}

// logLine is a filtered log line with its position in the source list.
type logLine struct {
	// text is the line.
	text string
	// index is its position in the unfiltered log.
	index int
}

// defaultWidth and defaultHeight are the size assumed until the terminal
// reports its own.
//
// A guess is needed because the first frame may be drawn before the size
// message arrives, and a geometry of zero is a blank screen. 80×24 is the
// terminal that always fits.
const (
	defaultWidth  = 80
	defaultHeight = 24
)

// New creates the monitor.
func New(source DataSource) Model {
	// The dark palette until the terminal says otherwise: that is what a terminal
	// normally is, and the answer arrives in the first exchange.
	pal := newPalette(true)

	m := Model{
		Source: source,
		Follow: true,
		Wrap:   true,
		pal:    pal,
		// The spinner is themed here rather than left at its default: an
		// unstyled spinner is the one element on the loading screen that would
		// not belong to the palette.
		spinner: pal.NewSpinner(),
		// Nothing is selected until the reader selects it: the details key must
		// not open something the reader never pointed at.
		selected: -1,
		keys:     newKeyMap(),
		now:      time.Now(),
	}

	// The geometry is built here rather than left for the first resize: View may
	// run before the size message arrives, and it reads the geometry to draw.
	m.width, m.height = defaultWidth, defaultHeight
	m.geom = geometry(m.width, m.height, false)

	return m
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.read(), m.spinner.Tick, tea.RequestBackgroundColor)
}

// read returns the command that performs a reading.
func (m Model) read() tea.Cmd {
	source := m.Source
	// The day is captured with the source: the command runs after the model
	// moved on, and a reading that resolved the day when it arrived would answer
	// the previous question.
	day := m.day
	return func() tea.Msg {
		if source == nil {
			return readMsg{}
		}
		obs, err := source.Observe(day)
		return readMsg{obs: obs, err: err}
	}
}

// tailLimit is how many journal lines are read.
//
// A few hundred rather than the whole file: the journal is rotated by size, and
// the reader is looking at what just happened. The filter runs over what was
// read, and it is named in the block so that the bound is never a silent one.
const tailLimit = 500
