// Package presets — готовые пресеты оформления для Bubble Tea v2.
//
// Пакет даёт три вещи:
//
//   - семантическую тему [Theme]: одна акцентная краска плюс нейтральная база,
//     цвета разведены по ролям (accent, muted, border, surface, ...) и
//     разрешаются под светлый/тёмный терминал через lipgloss.LightDark;
//   - готовые элементы интерфейса: шапка экрана, хлебные крошки, табы,
//     статус-бар, пилюли, панели, состояния loading/empty/error, оверлей-модалка;
//   - предсобранные виджеты Bubbles (input, textarea, list, table, spinner,
//     progress, viewport, huh-форма), уже покрашенные этой темой.
//
// Пакет не тянет зависимостей сверх стека Charm и не создаёт глобального
// состояния: скопируй его в проект и подставь свой акцент в [Charm].
package presets

import (
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/exp/charmtone"
)

// Tone — семантический тон пилюль, уведомлений и состояний.
type Tone int

const (
	ToneInfo Tone = iota
	ToneSuccess
	ToneWarning
	ToneDanger
)

// Theme — семантическая тема: роли палитры и собранные из них стили.
// Создавай через [Charm]; нулевое значение не рассчитано на прямое использование.
type Theme struct {
	IsDark bool

	// Роли палитры. Text и OnAccent — тоже цвета, но Text по умолчанию
	// терминальный дефолт (lipgloss.NoColor), чтобы текст не спорил с фоном.
	Accent      color.Color
	AccentAlt   color.Color
	OnAccent    color.Color
	Success     color.Color
	Warning     color.Color
	Danger      color.Color
	Info        color.Color
	Text        color.Color
	Muted       color.Color
	Faint       color.Color
	Border      color.Color
	BorderFocus color.Color
	Surface     color.Color
	SurfaceAlt  color.Color

	// Тексты и подписи.
	Title        lipgloss.Style
	Subtitle     lipgloss.Style
	SectionTitle lipgloss.Style
	Label        lipgloss.Style
	Value        lipgloss.Style
	Hint         lipgloss.Style
	Dim          lipgloss.Style
	Key          lipgloss.Style
	AccentText   lipgloss.Style
	SuccessText  lipgloss.Style
	ErrorText    lipgloss.Style
	// WarningText — предупреждение внутри строки, например отменённый запрос.
	// Не NoticeWarning: уведомление занимает отдельную строку со значком, а тут
	// предупреждение это одно слово среди чужих колонок.
	WarningText lipgloss.Style
	Rule        lipgloss.Style

	// Строки, табы, карточки, панели, оверлеи.
	Selected      lipgloss.Style
	SelectedBlur  lipgloss.Style
	TabActive     lipgloss.Style
	TabInactive   lipgloss.Style
	Card          lipgloss.Style
	CardFocused   lipgloss.Style
	Panel         lipgloss.Style
	PanelFocused  lipgloss.Style
	Modal         lipgloss.Style
	StatusBar     lipgloss.Style
	TableHeader   lipgloss.Style
	TableRowOdd   lipgloss.Style
	TableRowEven  lipgloss.Style
	FilterMatch   lipgloss.Style
	NoticeInfo    lipgloss.Style
	NoticeSuccess lipgloss.Style
	NoticeWarning lipgloss.Style
	NoticeDanger  lipgloss.Style
}

// Charm собирает тему на палитре CharmTone.
//
// Правила подбора, чтобы тема осталась читаемой при смене схемы терминала:
// бренд берётся из палитры (в тёмной схеме — осветлённый вариант Charple,
// в светлой — приглушённый), а семантические цвета выбраны так, чтобы
// контраст с фоном держался в обеих схемах. Основной текст не красится вообще:
// так он наследует настройки терминала пользователя.
func Charm(isDark bool) Theme {
	t := Theme{IsDark: isDark}
	ld := lipgloss.LightDark(isDark)

	t.Accent = ld(charmtone.Charple, charmtone.Hazy)
	t.AccentAlt = ld(charmtone.Damson, charmtone.Malibu)
	t.OnAccent = ld(charmtone.Salt, charmtone.Pepper)
	t.Success = ld(charmtone.Pickle, charmtone.Guac)
	t.Warning = ld(charmtone.Cumin, charmtone.Mustard)
	t.Danger = ld(charmtone.Pom, charmtone.Bengal)
	t.Info = t.AccentAlt
	t.Text = lipgloss.NoColor{}
	t.Muted = ld(charmtone.Oyster, charmtone.Steam)
	t.Faint = ld(charmtone.Steam, charmtone.Oyster)
	t.Border = ld(charmtone.Steep, charmtone.Char)
	t.BorderFocus = t.Accent
	t.Surface = ld(charmtone.Salt, charmtone.BBQ)
	t.SurfaceAlt = ld(charmtone.Sash, charmtone.Pepper)

	plain := lipgloss.NewStyle()

	t.Title = plain.Bold(true).Foreground(t.Accent)
	t.Subtitle = plain.Foreground(t.Muted)
	t.SectionTitle = plain.Bold(true).Foreground(t.AccentAlt)
	t.Label = plain.Foreground(t.Muted)
	t.Value = plain.Foreground(t.Text)
	t.Hint = plain.Foreground(t.Faint).Italic(true)
	t.Dim = plain.Foreground(t.Faint)
	t.Key = plain.Bold(true).Foreground(t.AccentAlt)
	t.AccentText = plain.Bold(true).Foreground(t.Accent)
	t.SuccessText = plain.Bold(true).Foreground(t.Success)
	t.ErrorText = plain.Bold(true).Foreground(t.Danger)
	t.WarningText = plain.Bold(true).Foreground(t.Warning)
	t.Rule = plain.Foreground(t.Border)

	t.Selected = plain.Bold(true).Foreground(t.OnAccent).Background(t.Accent)
	t.SelectedBlur = plain.Foreground(t.Text).Background(t.SurfaceAlt)
	t.TabActive = plain.Padding(0, 1).Bold(true).Foreground(t.OnAccent).Background(t.Accent)
	t.TabInactive = plain.Padding(0, 1).Foreground(t.Muted)
	t.Card = plain.Border(lipgloss.RoundedBorder()).BorderForeground(t.Border).Padding(0, 2)
	t.CardFocused = t.Card.BorderForeground(t.BorderFocus)
	t.Panel = plain.Border(lipgloss.RoundedBorder()).BorderForeground(t.Border).Padding(1, 2)
	t.PanelFocused = t.Panel.BorderForeground(t.BorderFocus)
	t.Modal = plain.Border(lipgloss.RoundedBorder()).BorderForeground(t.BorderFocus).
		Background(t.SurfaceAlt).Padding(1, 2)
	t.StatusBar = plain.Padding(0, 1).Background(t.Surface)
	t.TableHeader = plain.Bold(true).Foreground(t.AccentAlt).Padding(0, 1)
	t.TableRowOdd = plain.Padding(0, 1).Foreground(t.Text)
	t.TableRowEven = plain.Padding(0, 1).Foreground(t.Text).Background(t.Surface)
	t.FilterMatch = plain.Bold(true).Foreground(t.Accent)
	t.NoticeInfo = plain.Foreground(t.Info)
	t.NoticeSuccess = plain.Foreground(t.Success)
	t.NoticeWarning = plain.Foreground(t.Warning)
	t.NoticeDanger = plain.Foreground(t.Danger)

	return t
}

// WithDark пересобирает тему под другую схему терминала и возвращает её.
// Удобно в Update основного экрана:
//
//	case tea.BackgroundColorMsg:
//		if msg.IsDark() != m.theme.IsDark {
//			m.theme = m.theme.WithDark(msg.IsDark())
//			m.rebuild() // перерисовать всё, что кэшировало готовые строки
//		}
func (t Theme) WithDark(isDark bool) Theme {
	if t.IsDark == isDark {
		return t
	}
	return Charm(isDark)
}
