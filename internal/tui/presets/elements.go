package presets

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

// Banner — шапка экрана: заголовок слева, подзаголовок справа, линия под ними.
// Когда места мало, подзаголовок уезжает на вторую строку.
func (t Theme) Banner(title, subtitle string, width int) string {
	if width < 1 {
		width = 1
	}
	head := t.Title.Render(title)
	if subtitle == "" {
		return clamp(lipgloss.JoinVertical(lipgloss.Left, head, t.Separator(width)), width)
	}
	tail := t.Subtitle.Render(subtitle)
	gap := width - lipgloss.Width(head) - lipgloss.Width(tail)
	if gap < 1 {
		return clamp(lipgloss.JoinVertical(lipgloss.Left, head, tail, t.Separator(width)), width)
	}
	return clamp(lipgloss.JoinVertical(lipgloss.Left,
		head+strings.Repeat(" ", gap)+tail,
		t.Separator(width),
	), width)
}

// clamp обрезает каждую строку блока по ширине: lipgloss не режет сам,
// поэтому без этого длинный заголовок ломает кадр AltScreen.
func clamp(block string, width int) string {
	if width < 1 {
		return ""
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(block)
}

// Separator — горизонтальная линия цветом Border ровно width ячеек.
func (t Theme) Separator(width int) string {
	if width < 1 {
		width = 1
	}
	return t.Rule.Render(strings.Repeat("─", width))
}

// Crumbs — хлебные крошки: последняя часть — акцентная, остальные приглушены.
func (t Theme) Crumbs(parts ...string) string {
	if len(parts) == 0 {
		return ""
	}
	out := make([]string, 0, len(parts))
	for i, p := range parts {
		if i == len(parts)-1 {
			out = append(out, t.AccentText.Render(p))
			continue
		}
		out = append(out, t.Dim.Render(p))
	}
	return strings.Join(out, t.Dim.Render(" › "))
}

// Tabs — полоса табов. Активный таб — инверсная пилюля акцентом, остальные приглушены.
// Ширина обрезается по width, лишнее молча уходит за край.
func (t Theme) Tabs(labels []string, active, width int) string {
	parts := make([]string, 0, len(labels))
	for i, l := range labels {
		if i == active {
			parts = append(parts, t.TabActive.Render(l))
			continue
		}
		parts = append(parts, t.TabInactive.Render(l))
	}
	row := lipgloss.JoinHorizontal(lipgloss.Top, parts...)
	if width > 0 {
		row = lipgloss.NewStyle().MaxWidth(width).Render(row)
	}
	return row
}

// Hint — пара «клавиша — действие» для [Theme.HelpBar].
type Hint struct {
	Key  string
	Desc string
}

// HelpBar — статус-бар снизу: клавиши акцентным цветом, описания — приглушённо,
// фон Surface на всю ширину кадра. Пустые подсказки пропускаются.
//
// Возвращает ровно width ячеек. В lipgloss v2 Style.Width считает паддинги
// внутри бокса, поэтому StatusBar задаётся прямо по width: так фон закрашивает
// всю полосу, а длинные подсказки обрезаются по её краю.
func (t Theme) HelpBar(width int, hints ...Hint) string {
	bar := strings.Join(t.HintCells(hints...), t.Dim.Render(" · "))
	if width < 1 {
		return t.StatusBar.Render(bar)
	}
	return t.StatusBar.MaxWidth(width).Width(width).Render(bar)
}

// HintCells — те же подсказки, но по отдельности: клавиша акцентом, описание
// приглушённо, пустые пропущены.
//
// Нужен там, где полосу собирают не в одну строку: разделитель между
// подсказками задаёт вызывающий, и он же решает, чем развести куски — « · » или
// свободным местом.
func (t Theme) HintCells(hints ...Hint) []string {
	parts := make([]string, 0, len(hints))
	for _, h := range hints {
		if h.Key == "" || h.Desc == "" {
			continue
		}
		parts = append(parts, t.Key.Render(h.Key)+" "+t.Dim.Render(h.Desc))
	}
	return parts
}

// Pill — пилюля (бейдж) на фоне Surface: цветной жирный текст в отступах.
// Контраст обеспечен подбором ролей, поэтому одинаково читается на светлом
// и тёмном терминале.
func (t Theme) Pill(label string, tone Tone) string {
	c, _ := t.tone(tone)
	return lipgloss.NewStyle().
		Foreground(c).Background(t.Surface).
		Padding(0, 1).Bold(true).
		Render(label)
}

// Notice — строка состояния с иконкой тона: «✓ собрано», «! 3 предупреждения».
// Без подложки, чтобы вставлять в любую строку.
func (t Theme) Notice(tone Tone, text string) string {
	_, style := t.tone(tone)
	return style.Render(icon(tone) + " " + text)
}

func icon(tone Tone) string {
	switch tone {
	case ToneSuccess:
		return "✓"
	case ToneWarning:
		return "!"
	case ToneDanger:
		return "✗"
	default:
		return "●"
	}
}

func (t Theme) tone(tone Tone) (c color.Color, style lipgloss.Style) {
	switch tone {
	case ToneSuccess:
		return t.Success, t.NoticeSuccess
	case ToneWarning:
		return t.Warning, t.NoticeWarning
	case ToneDanger:
		return t.Danger, t.NoticeDanger
	default:
		return t.Info, t.NoticeInfo
	}
}

// KV — пара «подпись — значение» для [Theme.KeyValues].
type KV struct {
	Label string
	Value string
}

// KeyValues — ровная колонка подпись/значение: подписи выравниваются по ширине labelWidth.
func (t Theme) KeyValues(labelWidth int, pairs ...KV) string {
	rows := make([]string, 0, len(pairs))
	for _, p := range pairs {
		rows = append(rows, t.KVLine(labelWidth, p.Label, p.Value))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

// KVLine собирает одну строку подпись/значение.
//
// Отдельная функция, потому что строки нужны и поодиночке: когда пары
// раскладываются по нескольким колонкам, собирать их в вертикальный блок и потом
// разрезать обратно — лишняя работа и лишний повод ошибиться.
func (t Theme) KVLine(labelWidth int, label, value string) string {
	// Паддинг считается по НЕстилизованной подписи и добавляется ПОСЛЕ
	// стилизации. lipgloss.Width на отрендеренной строке вернул бы её
	// собственную ширину, разница вышла бы нулевой, и подписи разной длины
	// слиплись бы со значениями. Стиль подписи обрамления не добавляет —
	// только цвет, — поэтому ширина текста и есть ширина подписи.
	rendered := t.Label.Render(label)
	// Разделитель добавляется ВСЕГДА, даже когда подпись уже нужной ширины:
	// самая длинная подпись паддинга не получает, и без этого её значение
	// слиплось бы с ней — «familyauto» вместо «family  auto».
	rendered += " "
	if gap := labelWidth - lipgloss.Width(label); gap > 0 {
		rendered += strings.Repeat(" ", gap)
	}
	return rendered + t.Value.Render(value)
}

// RenderCard рисует карточку с рамкой и заголовком. focused переключает рамку и
// заголовок на акцент — этого достаточно для визуального фокуса без смены
// разметки.
func (t Theme) RenderCard(title, body string, width int, focused bool) string {
	st := t.Card
	titleStyle := t.SectionTitle
	if focused {
		st = t.CardFocused
		titleStyle = t.Title
	}
	inner := width - st.GetHorizontalFrameSize()
	if inner < 1 {
		inner = 1
	}
	content := lipgloss.NewStyle().Width(inner).Render(body)
	if title != "" {
		content = lipgloss.JoinVertical(lipgloss.Left, titleStyle.Render(title), content)
	}
	return st.Width(width).Render(content)
}

// RenderPanel — панель с заголовком в рамке, базовая единица разметки экрана.
func (t Theme) RenderPanel(title, body string, width int, focused bool) string {
	st := t.Panel
	titleStyle := t.SectionTitle
	if focused {
		st = t.PanelFocused
		titleStyle = t.Title
	}
	inner := width - st.GetHorizontalFrameSize()
	if inner < 1 {
		inner = 1
	}
	content := lipgloss.NewStyle().Width(inner).Render(body)
	if title != "" {
		content = lipgloss.JoinVertical(lipgloss.Left, titleStyle.Render(title), content)
	}
	return st.Width(width).Render(content)
}

// GradientTitle — заголовок с градиентом от Accent к AccentAlt.
// Красится по рунам, поэтому работает и с кириллицей, и с CJK.
func (t Theme) GradientTitle(s string) string {
	runes := []rune(s)
	if len(runes) == 0 {
		return ""
	}
	colors := lipgloss.Blend1D(len(runes), t.Accent, t.AccentAlt)
	var b strings.Builder
	for i, r := range runes {
		b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colors[i]).Render(string(r)))
	}
	return b.String()
}

// StateLoading — строка загрузки. Готовый кадр берётся у spinner'а:
// Theme.StateLoading(m.spinner.View(), "Загружаю проекты...").
func (t Theme) StateLoading(frame, text string) string {
	return frame + " " + t.Subtitle.Render(text)
}

// StateEmpty — пустое состояние: заголовок плюс подсказка, что делать дальше.
func (t Theme) StateEmpty(title, hint string) string {
	block := lipgloss.JoinVertical(lipgloss.Left,
		t.AccentText.Render("◌ "+title),
		t.Hint.Render(hint),
	)
	return block
}

// StateError — состояние ошибки с подсказкой о выходе.
func (t Theme) StateError(title, hint string) string {
	block := lipgloss.JoinVertical(lipgloss.Left,
		t.ErrorText.Render("✗ "+title),
		t.Hint.Render(hint),
	)
	return block
}

// StateSuccess — транзиентное подтверждение: показывается после успешного
// действия и снимается по таймеру, чтобы не занимать экран постоянно.
func (t Theme) StateSuccess(title, hint string) string {
	block := lipgloss.JoinVertical(lipgloss.Left,
		t.SuccessText.Render("✓ "+title),
		t.Hint.Render(hint),
	)
	return block
}

// Centered центрует блок в области width×height. Нужен для экранов
// загрузки/пустых состояний и модалок, чтобы не считать отступы руками.
func Centered(width, height int, block string) string {
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, block)
}
