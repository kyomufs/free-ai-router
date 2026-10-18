package presets

import (
	"charm.land/lipgloss/v2"
)

// ModalLayerID — id слоя модалки. По нему потом ищется попадание мыши:
//
//	frame, comp := m.theme.Overlay(m.base, m.modalView(), m.width, m.height)
//	// в Update:
//	case tea.MouseClickMsg:
//		if comp.Hit(msg.Mouse().X, msg.Mouse().Y).ID() == presets.ModalLayerID {
//			// клик пришёл внутрь окна, отдать модалке
//		}
const ModalLayerID = "modal"

// Overlay кладёт модалку по центру base и возвращает готовый кадр вместе с
// компоситором для hit-теста.
//
// base должен быть полным кадром размером width×height: компоситор не
// выравнивает подложку, он просто рисует слои друг поверх друга.
// Модалке нужен непрозрачный фон (t.Modal его ставит), иначе сквозь неё
// будет видно содержимое подложки.
//
// Компоситор создаётся здесь, то есть в View. Чтобы разбирать клики в Update,
// модель обязана его сохранить:
//
//	case tea.MouseClickMsg:
//		if m.comp != nil && m.comp.Hit(x, y).ID() == presets.ModalLayerID { ... }
func (t Theme) Overlay(base, modal string, width, height int) (string, *lipgloss.Compositor) {
	x, y := CenteredOrigin(width, height, modal)
	comp := lipgloss.NewCompositor(
		lipgloss.NewLayer(base),
		lipgloss.NewLayer(modal).ID(ModalLayerID).X(x).Y(y).Z(1),
	)
	// Compositor.Render обрезает хвостовые пробелы строк, поэтому кадр
	// возвращается к полному размеру экрана явно.
	return Fit(comp.Render(), width, height), comp
}

// CenteredOrigin — левый верхний угол блока, если его отцентрировать в кадре
// width×height. Нужен, чтобы посчитать прямоугольники кликов внутри модалки:
// [Theme.Overlay] центрирует по этой же формуле.
//
//	x0, y0 := presets.CenteredOrigin(w, h, modal)
//	// кнопка «reload» шириной bw на строке row модалки:
//	rect := hit{x: x0 + pad, y: y0 + row, w: bw, h: 1}
func CenteredOrigin(width, height int, block string) (x, y int) {
	x = (width - lipgloss.Width(block)) / 2
	y = (height - lipgloss.Height(block)) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	return x, y
}

// Split раскладывает две панели по горизонтали: слева фиксированной ширины,
// справа — на остаток. Если места меньше, чем нужно обеим, панели встают
// друг под друга, а между ними проходит линия. Такой схлопывающийся layout
// переживает узкий терминал без отдельных веток в коде экрана.
//
// Две колонки держатся, когда leftWidth + ширина правой панели + 3 (разделитель)
// влезают в width. Шире этой суммы панель справа не станет: правая колонка
// получает ровно width-leftWidth-3 и обрезает содержимое сама.
// Схлопнутая пара требует 2*(содержимое панели)+1 строк, поэтому при высоте
// меньше ~15 строк нижняя панель уедет за кадр — это ограничение вертикали,
// а не ширины.
func (t Theme) Split(left, right string, width, leftWidth int) string {
	divider := t.Rule.Render(" │ ")
	dividerWidth := lipgloss.Width(divider)
	if width < leftWidth+lipgloss.Width(right)+dividerWidth {
		return lipgloss.JoinVertical(lipgloss.Left, left, t.Separator(width), right)
	}
	rightWidth := width - leftWidth - dividerWidth
	left = lipgloss.NewStyle().Width(leftWidth).Render(left)
	right = lipgloss.NewStyle().Width(rightWidth).Render(right)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, divider, right)
}

// Fit приводит блок ровно к width×height ячеек: лишнее обрезает, недостающее
// добивает пробелами. Нужен перед выводом в tea.View: AltScreenMode не
// скроллится, лишняя строка уезжает за край, а Compositor.Render обрезает
// хвостовые пробелы строк, и кадр без добивки приходит короче терминала.
func Fit(block string, width, height int) string {
	if width < 1 || height < 1 {
		return ""
	}
	return lipgloss.NewStyle().
		MaxWidth(width).MaxHeight(height).
		Width(width).Height(height).
		Render(block)
}
