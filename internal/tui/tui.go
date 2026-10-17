// Package tui — встроенный терминальный интерфейс.
//
// ПОЧЕМУ ВСТРОЕННЫЙ, А НЕ СКРИПТ. Внешняя обёртка не может быть частью
// контракта: она способна отсутствовать, требовать чужих программ,
// расходиться с бинарём в знании словарей и не проверяется как код проекта.
// Интерфейс внутри бинаря этих свойств не имеет по построению.
//
// ЧЕГО ИНТЕРФЕЙС НЕ ДЕЛАЕТ:
//
//   - НЕ переопределяет терминал. Полноэкранный режим не включается:
//     интерфейс сосуществует с терминалом, а вывод остаётся пригодным для
//     копирования.
//   - НЕ держит состояние. Каждый экран читает данные заново, а изменения
//     идут через тот же код, что и команды: у настройки одно место применения.
//   - НЕ требует ничего сверх бинаря.
package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// Экран — раздел интерфейса.
//
// Экраны разделены по вопросу, на который они отвечают: что происходит сейчас,
// чем это оплачено, и что можно изменить. Три вопроса — три экрана, и никаких
// вложенных меню.
type Screen int

const (
	// ScreenStatus — состояние демона, оси, счётчики.
	ScreenStatus Screen = iota
	// ScreenKeys — ключи провайдеров.
	ScreenKeys
	// ScreenControl — изменяемые настройки.
	ScreenControl
)

// String возвращает имя экрана.
func (s Screen) String() string {
	switch s {
	case ScreenStatus:
		return "состояние"
	case ScreenKeys:
		return "ключи"
	case ScreenControl:
		return "настройки"
	default:
		return "неизвестно"
	}
}

// DataSource отдаёт данные для экранов.
//
// Интерфейс, а не конкретный тип: интерфейс не должен знать, откуда берутся
// данные — с демона или из конфигурации. Это позволяет проверять его без
// запуска демона.
type DataSource interface {
	// Status возвращает строки для экрана состояния.
	Status() ([]Section, error)
	// Keys возвращает строки для экрана ключей.
	Keys() ([]Section, error)
	// Options возвращает строки для экрана настроек.
	Options() ([]Section, error)
}

// Section — раздел экрана с заголовком и строками.
type Section struct {
	// Title — заголовок раздела.
	Title string
	// Rows — строки в форме «имя — значение».
	Rows []Row
}

// Row — одна строка раздела.
type Row struct {
	// Name — имя настройки или поля.
	Name string
	// Value — значение.
	Value string
	// Note — пояснение. Показывается там, где значение требует объяснения —
	// например, когда настройка не действует.
	Note string
}

// Model — состояние интерфейса.
type Model struct {
	// Source отдаёт данные.
	Source DataSource
	// Screen — текущий экран.
	Screen Screen
	// Cursor — выбранная строка на экране настроек.
	Cursor int
	// sections — данные текущего экрана.
	sections []Section
	// err — ошибка чтения данных.
	err error
	// width и height — размер терминала.
	width, height int
	// quitting — признак запрошенного выхода.
	quitting bool
}

// New создаёт интерфейс.
func New(source DataSource) Model {
	return Model{Source: source}
}

// Init реализует tea.Model.
func (m Model) Init() tea.Cmd { return m.load() }

// load читает данные текущего экрана.
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

// loadedMsg — результат чтения данных.
type loadedMsg struct {
	sections []Section
	err      error
}
