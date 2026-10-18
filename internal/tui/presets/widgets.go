package presets

import (
	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
)

// InputStyles — стили textinput под тему: акцентный промпт у сфокусированного,
// приглушённый у потерявшего фокус.
//
// Стили передаются виджету целиком при создании или через SetStyles.
func (t Theme) InputStyles() textinput.Styles {
	s := textinput.DefaultStyles(t.IsDark)
	s.Focused.Text = t.Value
	s.Focused.Placeholder = t.Dim
	s.Focused.Prompt = t.AccentText
	s.Focused.Suggestion = t.Dim
	s.Blurred = s.Focused
	s.Blurred.Text = t.Dim
	s.Blurred.Prompt = t.Dim
	s.Cursor.Color = t.Accent
	return s
}

// TextAreaStyles — то же самое для textarea.
func (t Theme) TextAreaStyles() textarea.Styles {
	s := textarea.DefaultStyles(t.IsDark)
	s.Focused.Text = t.Value
	s.Focused.Placeholder = t.Dim
	s.Focused.Prompt = t.AccentText
	s.Focused.LineNumber = t.Dim
	s.Blurred = s.Focused
	s.Blurred.Text = t.Dim
	s.Blurred.Prompt = t.Dim
	s.Cursor.Color = t.Accent
	return s
}

// HelpStyles — стили встроенного help из Bubbles: клавиши акцентом,
// описания приглушённо.
func (t Theme) HelpStyles() help.Styles {
	s := help.DefaultStyles(t.IsDark)
	s.ShortKey = t.Key
	s.ShortDesc = t.Dim
	s.ShortSeparator = t.Dim
	s.FullKey = t.Key
	s.FullDesc = t.Dim
	s.FullSeparator = t.Dim
	s.Ellipsis = t.Dim
	return s
}

// ListStyles — стили списка Bubbles, включая поле фильтра.
func (t Theme) ListStyles() list.Styles {
	s := list.DefaultStyles(t.IsDark)
	s.TitleBar = lipgloss.NewStyle().Padding(0, 0, 1, 0)
	s.Title = t.Title
	s.Spinner = t.AccentText
	s.Filter = t.InputStyles()
	s.DefaultFilterCharacterMatch = t.FilterMatch
	s.StatusBar = t.Dim
	s.StatusEmpty = t.Dim
	s.StatusBarActiveFilter = t.AccentText
	s.StatusBarFilterCount = t.Dim
	s.NoItems = t.Dim
	s.PaginationStyle = t.Dim
	s.HelpStyle = t.HelpStyles().ShortKey
	s.ActivePaginationDot = t.AccentText
	s.InactivePaginationDot = t.Dim
	s.DividerDot = t.Dim
	return s
}

// ListDelegate — делегат элемента списка: выбранный получает акцентную
// инверсию, остальные — обычный и приглушённый текст.
//
// Описание и SetSpacing(1) дают 3 строки на пункт (Height() 2 + Spacing() 1),
// а list делит высоту на эту сумму: в панели на 16 строк видно 4 пункта из 6.
// Нужно плотнее — ужать делегата, SetDelegate сам пересчитает пагинацию:
//
//	d := t.ListDelegate()
//	d.SetSpacing(0) // 2 строки на пункт
//	l.SetDelegate(d)
func (t Theme) ListDelegate() list.DefaultDelegate {
	d := list.NewDefaultDelegate()
	d.ShowDescription = true
	d.SetSpacing(1)
	d.Styles.NormalTitle = t.Value
	d.Styles.NormalDesc = t.Dim
	d.Styles.SelectedTitle = t.Selected
	d.Styles.SelectedDesc = t.Selected
	d.Styles.DimmedTitle = t.Dim
	d.Styles.DimmedDesc = t.Dim
	d.Styles.FilterMatch = t.FilterMatch
	return d
}

// TableStyles — стили виджета table: шапка акцентом, курсор — акцентной
// инверсией, чётные строки на подложке для читаемости.
func (t Theme) TableStyles() table.Styles {
	s := table.Styles{}
	s.Header = t.TableHeader
	s.Cell = t.TableRowOdd
	s.Selected = t.Selected
	return s
}

// NewInput — готовое поле ввода: акцентный промпт, плейсхолдер, заданная ширина.
func (t Theme) NewInput(placeholder string, width int) textinput.Model {
	in := textinput.New()
	in.Placeholder = placeholder
	in.Prompt = "› "
	st := t.InputStyles()
	in.SetStyles(st)
	in.SetWidth(width)
	return in
}

// NewTextArea — многострочный ввод фиксированного размера.
func (t Theme) NewTextArea(placeholder string, width, height int) textarea.Model {
	ta := textarea.New()
	ta.Placeholder = placeholder
	ta.Prompt = "│ "
	st := t.TextAreaStyles()
	ta.SetStyles(st)
	ta.SetWidth(width)
	ta.SetHeight(height)
	return ta
}

// NewList — список с фильтром, статус-баром и пагинацией.
func (t Theme) NewList(title string, items []list.Item, width, height int) list.Model {
	l := list.New(items, t.ListDelegate(), width, height)
	l.Title = title
	l.Styles = t.ListStyles()
	l.SetShowStatusBar(true)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false) // подсказки рисует Theme.HelpBar, чтобы не было двух строк с клавишами
	l.Help.Styles = t.HelpStyles()
	return l
}

// NewTable — таблица с акцентной шапкой и курсором.
func (t Theme) NewTable(cols []table.Column, rows []table.Row, width, height int) table.Model {
	styles := t.TableStyles()
	return table.New(
		table.WithColumns(cols),
		table.WithRows(rows),
		table.WithStyles(styles),
		table.WithWidth(width),
		table.WithHeight(height),
		table.WithFocused(true),
	)
}

// NewSpinner — спиннер точками в акцентном цвете. Кадр обновляется командой
// m.spinner.Tick, кадр берётся из m.spinner.View().
func (t Theme) NewSpinner() spinner.Model {
	st := lipgloss.NewStyle().Foreground(t.Accent)
	return spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(st))
}

// NewProgress — прогресс-бар с градиентом от основного акцента ко второму.
func (t Theme) NewProgress(width int) progress.Model {
	return progress.New(
		progress.WithColors(t.Accent, t.AccentAlt),
		progress.WithWidth(width),
	)
}

// NewViewport — область прокрутки под заданный размер.
func (t Theme) NewViewport(width, height int) viewport.Model {
	return viewport.New(viewport.WithWidth(width), viewport.WithHeight(height))
}

// NewHuhForm — форма huh в теме Charm.
//
// Важно: в huh v2 *huh.Form больше не tea.Model (его View возвращает string).
// Держи форму полем и в своём Update делай:
//
//	fm, cmd := m.form.Update(msg)
//	if f, ok := fm.(*huh.Form); ok {
//		m.form = f
//	}
//	return m, cmd
//
// а во View — tea.NewView(m.form.View()).
func (t Theme) NewHuhForm(groups ...*huh.Group) *huh.Form {
	return huh.NewForm(groups...).
		WithTheme(huh.ThemeFunc(huh.ThemeCharm)).
		WithShowHelp(true)
}
