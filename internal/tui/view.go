package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Update реализует tea.Model.
//
// Клавиши означают одно и то же на всех экранах: переключение экранов, движение
// по строкам, перечитывание и выход. Неназначенная клавиша не делает НИЧЕГО —
// ни перерисовки, ни мерцания: лишняя перерисовка читается как сбой.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case loadedMsg:
		m.sections = msg.sections
		m.err = msg.err
		m.clampCursor()
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			m.quitting = true
			return m, tea.Quit

		case "tab":
			m.Screen = (m.Screen + 1) % 3
			m.Cursor = 0
			return m, m.load()

		case "shift+tab":
			m.Screen = (m.Screen + 2) % 3
			m.Cursor = 0
			return m, m.load()

		case "r":
			return m, m.load()

		case "up", "k":
			if m.Cursor > 0 {
				m.Cursor--
			}
			return m, nil

		case "down", "j":
			if m.Cursor < m.rowCount()-1 {
				m.Cursor++
			}
			return m, nil
		}
	}
	return m, nil
}

// clampCursor удерживает курсор в границах списка строк.
//
// Без этого переключение на экран с меньшим числом строк оставило бы курсор
// указывающим в пустоту, и выделение исчезло бы без объяснения.
func (m *Model) clampCursor() {
	if n := m.rowCount(); m.Cursor >= n {
		m.Cursor = n - 1
	}
	if m.Cursor < 0 {
		m.Cursor = 0
	}
}

// rowCount возвращает число строк на текущем экране.
func (m Model) rowCount() int {
	n := 0
	for _, s := range m.sections {
		n += len(s.Rows)
	}
	return n
}

// Стили. Цвета подобраны так, чтобы интерфейс читался и на светлом, и на
// тёмном фоне, и оставался спокойным: это инструмент, а не витрина.
var (
	styleTitle    = lipgloss.NewStyle().Bold(true)
	styleSection  = lipgloss.NewStyle().Bold(true).Faint(false)
	styleName     = lipgloss.NewStyle().Faint(true)
	styleSelected = lipgloss.NewStyle().Bold(true)
	styleNote     = lipgloss.NewStyle().Faint(true)
	styleError    = lipgloss.NewStyle().Bold(true)
	styleTabs     = lipgloss.NewStyle().Faint(true)
)

// View реализует tea.Model.
func (m Model) View() string {
	if m.quitting {
		return ""
	}

	var b strings.Builder

	b.WriteString(styleTitle.Render("free-ai-router"))
	b.WriteString("  ")
	b.WriteString(m.renderTabs())
	b.WriteString("\n\n")

	if m.err != nil {
		b.WriteString(styleError.Render("ошибка чтения данных: " + m.err.Error()))
		b.WriteString("\n\n")
	}

	if len(m.sections) == 0 {
		b.WriteString(styleNote.Render("данных нет"))
		b.WriteString("\n")
	}

	index := 0
	for _, section := range m.sections {
		if section.Title != "" {
			b.WriteString(styleSection.Render(section.Title))
			b.WriteString("\n")
		}
		width := 0
		for _, row := range section.Rows {
			if len(row.Name) > width {
				width = len(row.Name)
			}
		}
		for _, row := range section.Rows {
			line := fmt.Sprintf("%-*s  %s", width, row.Name, row.Value)
			if m.Screen == ScreenControl && index == m.Cursor {
				b.WriteString("▸ " + styleSelected.Render(line))
			} else if m.Screen == ScreenControl {
				b.WriteString("  " + line)
			} else {
				b.WriteString("  " + styleName.Render(fmt.Sprintf("%-*s", width, row.Name)) + "  " + row.Value)
			}
			if row.Note != "" {
				b.WriteString("  " + styleNote.Render(row.Note))
			}
			b.WriteString("\n")
			index++
		}
		b.WriteString("\n")
	}

	b.WriteString(styleTabs.Render("Tab экраны · ↑↓ строки · r обновить · q выход"))
	b.WriteString("\n")
	return b.String()
}

// renderTabs рисует полосу экранов.
func (m Model) renderTabs() string {
	names := []Screen{ScreenStatus, ScreenKeys, ScreenControl}
	parts := make([]string, 0, len(names))
	for _, s := range names {
		label := s.String()
		if s == m.Screen {
			parts = append(parts, styleTitle.Render("["+label+"]"))
			continue
		}
		parts = append(parts, styleTabs.Render(" "+label+" "))
	}
	return strings.Join(parts, "")
}
