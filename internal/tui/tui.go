// Package tui is the built-in terminal interface.
//
// WHY BUILT IN, AND NOT A SCRIPT. An external wrapper cannot be part of the
// contract: it may be absent, require other people's programs, diverge from the
// binary in its knowledge of the catalogs, and is not tested as project code.
// An interface inside the binary has none of these properties by construction.
//
// WHAT THE INTERFACE DOES NOT DO:
//
//   - It does NOT take over the terminal. Full-screen mode is not entered: the
//     interface coexists with the terminal, and the output stays fit for
//     copying.
//   - It does NOT hold state. Every screen reads the data afresh, and changes
//     go through the same code as the commands: settings have one place where
//     they are applied.
//   - It requires nothing beyond the binary.
package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// Screen is a section of the interface.
//
// The screens are split by the question they answer: what is happening now,
// what it costs, and what can be changed. Three questions — three screens, and
// no nested menus.
type Screen int

const (
	// ScreenStatus is the daemon state, the axes, the counters.
	ScreenStatus Screen = iota
	// ScreenKeys are the provider keys.
	ScreenKeys
	// ScreenControl are the mutable settings.
	ScreenControl
)

// String returns the screen name.
func (s Screen) String() string {
	switch s {
	case ScreenStatus:
		return "status"
	case ScreenKeys:
		return "keys"
	case ScreenControl:
		return "settings"
	default:
		return "unknown"
	}
}

// DataSource supplies the data for the screens.
//
// An interface rather than a concrete type: the interface must not know where
// the data comes from — from the daemon or from the configuration. That makes
// it testable without starting the daemon.
type DataSource interface {
	// Status returns the rows for the status screen.
	Status() ([]Section, error)
	// Keys returns the rows for the keys screen.
	Keys() ([]Section, error)
	// Options returns the rows for the settings screen.
	Options() ([]Section, error)
}

// Section is a screen section with a heading and rows.
type Section struct {
	// Title is the section heading.
	Title string
	// Rows are the rows in the form "name — value".
	Rows []Row
}

// Row is a single row of a section.
type Row struct {
	// Name is the name of the setting or field.
	Name string
	// Value is the value.
	Value string
	// Note is an explanation. It is shown where the value needs explaining —
	// for instance when the setting is not in effect.
	Note string
}

// Model is the interface state.
type Model struct {
	// Source supplies the data.
	Source DataSource
	// Screen is the current screen.
	Screen Screen
	// Cursor is the selected row on the settings screen.
	Cursor int
	// sections is the data of the current screen.
	sections []Section
	// err is the data-reading error.
	err error
	// width and height are the terminal size.
	width, height int
	// quitting marks that an exit has been requested.
	quitting bool
}

// New creates the interface.
func New(source DataSource) Model {
	return Model{Source: source}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd { return m.load() }

// load reads the data of the current screen.
func (m Model) load() tea.Cmd {
	source, screen := m.Source, m.Screen
	return func() tea.Msg {
		if source == nil {
			return loadedMsg{}
		}
		var (
			sections []Section
			err      error
		)
		switch screen {
		case ScreenKeys:
			sections, err = source.Keys()
		case ScreenControl:
			sections, err = source.Options()
		default:
			sections, err = source.Status()
		}
		return loadedMsg{sections: sections, err: err}
	}
}

// loadedMsg is the result of reading the data.
type loadedMsg struct {
	sections []Section
	err      error
}
