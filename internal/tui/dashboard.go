package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// ---------------------------------------------------------------------------
// tiles
// ---------------------------------------------------------------------------

// tileSpec is a tile before it has been placed: its heading, the note that goes
// in that heading, its share of the row, and the content it will draw.
//
// THE CONTENT IS A FUNCTION OF THE WIDTH, not a list of lines. A tile's content
// is laid out against the width it was given — the key table inside a provider
// tile drops its bar column when the tile is narrow — and that width is decided
// by the row the tile was placed in, which is decided after the tile is built.
// Measuring first and drawing afterwards would be the other way to do it, and it
// is the way that lets the two disagree.
type tileSpec struct {
	// title is the heading, drawn in the top border where a section's name goes.
	title string
	// meta is the note at the right of the top border. It is dropped when there
	// is no room for both it and the heading.
	meta string
	// weight is the tile's share of the free width of its row.
	weight int
	// body draws the content for the width available inside the tile's frame.
	body func(inner int) []string
}

// Tile geometry.
const (
	// tileGap is the space between two tiles of one row.
	tileGap = 2
	// minTileW is the narrowest a tile may be before its row gives up its
	// columns and stacks instead: at this width a key table's fingerprint and
	// counters still fit, and below it the tile would hide the numbers it exists
	// to show rather than merely show less of them.
	minTileW = 34
	// providersPerRow is how many provider tiles share a row on a wide terminal.
	// Two, because a provider's table needs half of a hundred-cell frame and
	// three of them would fit only by dropping a column from each.
	providersPerRow = 2
)

// dashboardSection renders the deployment as tiles.
//
// THE ORDER IS THE ORDER OF THE QUESTIONS. What the day came to, then what the
// daemon is, then who serves it and with which keys, then the addresses the
// traffic leaves from — each row of tiles narrower than the one above it, ending
// with the matrix, which is the only tile that wants all the width.
func (m Model) dashboardSection(inner int) []string {
	rows := [][]tileSpec{{m.todayTile(), m.daemonTile()}}
	rows = append(rows, m.providerRows(m.providerTiles())...)
	rows = append(rows, []tileSpec{m.addressesTile()})
	return m.tileRows(inner, rows...)
}

// providerRows groups the provider tiles into rows.
func (m Model) providerRows(tiles []tileSpec) [][]tileSpec {
	if len(tiles) == 0 {
		return [][]tileSpec{{m.noProvidersTile()}}
	}
	rows := make([][]tileSpec, 0, (len(tiles)+providersPerRow-1)/providersPerRow)
	for i := 0; i < len(tiles); i += providersPerRow {
		rows = append(rows, tiles[i:min(i+providersPerRow, len(tiles))])
	}
	return rows
}

// tileRows renders rows of tiles, each row exactly inner cells wide.
func (m Model) tileRows(inner int, rows ...[]tileSpec) []string {
	out := make([]string, 0, 32)
	for _, row := range rows {
		out = append(out, m.tileRow(inner, row)...)
	}
	return out
}

// tileRow renders one row of tiles.
//
// A ROW THAT CANNOT HOLD ITS TILES STACKS THEM. The alternative — sharing the
// width out anyway — would render a table too narrow for its own columns, and a
// tile that hides the counters is worse than a tile that takes a line of its
// own: the reader who asked for the dashboard gets all of it, in a different
// shape.
func (m Model) tileRow(inner int, row []tileSpec) []string {
	if len(row) == 0 {
		return nil
	}
	gaps := tileGap * (len(row) - 1)
	if inner < minTileW*len(row)+gaps {
		out := make([]string, 0, 24)
		for _, t := range row {
			body := t.body(contentWidth(inner))
			out = append(out, m.tile(t, inner, body, len(body)+2)...)
		}
		return out
	}

	widths := shareWidths(row, inner-gaps)

	// The bodies are built first because the tallest one decides the height of
	// the whole row, and a tile drawn short of it would leave its neighbours'
	// side borders hanging.
	bodies := make([][]string, len(row))
	height := 0
	for i, t := range row {
		bodies[i] = t.body(contentWidth(widths[i]))
		height = max(height, len(bodies[i])+2)
	}

	out := make([]string, 0, height)
	for y := 0; y < height; y++ {
		cells := make([]string, 0, len(row))
		for i, t := range row {
			cells = append(cells, m.tileLine(t, widths[i], bodies[i], height, y))
		}
		out = append(out, strings.Join(cells, repeat(" ", tileGap)))
	}
	return out
}

// shareWidths splits width cells between tiles in proportion to their weights.
//
// The remainder goes to the LAST tile rather than to the first: the rightmost
// border of the row is the one the reader compares with the row beneath it, and
// a leftover cell there would make the grid look ragged against the matrix tile
// underneath it.
func shareWidths(row []tileSpec, width int) []int {
	total := 0
	for _, t := range row {
		total += max(1, t.weight)
	}

	out := make([]int, len(row))
	left := width
	for i, t := range row {
		if i == len(row)-1 {
			out[i] = left
			break
		}
		w := width * max(1, t.weight) / total
		out[i] = w
		left -= w
	}
	return out
}

// tileLine renders line y of a tile of width and height.
func (m Model) tileLine(t tileSpec, width int, body []string, height, y int) string {
	switch {
	case width < 2:
		return ""
	case y == 0:
		return m.tileTop(t, width)
	case y == height-1:
		return borderBottom(m.pal.TileFrame, width)
	}
	if i := y - 1; i < len(body) {
		return borderRow(m.pal.TileFrame, body[i], width)
	}
	return borderRow(m.pal.TileFrame, "", width)
}

// tile is a whole tile, drawn as height lines.
func (m Model) tile(t tileSpec, width int, body []string, height int) []string {
	out := make([]string, 0, height)
	for y := 0; y < height; y++ {
		out = append(out, m.tileLine(t, width, body, height, y))
	}
	return out
}

// tileTop is the top border of a tile: the heading in the frame, the note at the
// right of it, and the row of dashes between them.
func (m Model) tileTop(t tileSpec, width int) string {
	return m.borderTop(m.pal.TileFrame, m.pal.tileTitleStyle(), t.title, t.meta, width)
}

// ---------------------------------------------------------------------------
// the tiles themselves
// ---------------------------------------------------------------------------

// todayTile is the day's traffic: what came in, how much of it was answered,
// and the proportion as a bar.
//
// It is titled with the day it shows — "today" only while that is what it is.
// The tile is the biggest object on the screen, and a reader who moved to a past
// day and came back to the dashboard must not find the day's own date relegated
// to a small line under a title that says "today": the numbers and the title would
// then disagree about which day they describe.
func (m Model) todayTile() tileSpec {
	t := m.obs.Totals
	title := m.dayName()
	meta := t.Day
	switch {
	case t.Day == "":
		meta = "no counters"
	case title != "today":
		// Under the date itself the key only repeats the title. Under "today" it
		// stays: it is where the daemon's own boundary falls, and a reader
		// comparing two days needs it.
		meta = ""
	}

	return tileSpec{
		title:  title,
		meta:   meta,
		weight: 1,
		body: func(inner int) []string {
			total := t.OK + t.Refused + t.Aborted
			if total == 0 {
				return []string{
					m.pal.Dim.Render("nothing counted"),
					m.pal.Hint.Render("the counters fill in as the daemon serves"),
				}
			}

			out := []string{
				m.kvLine("requests", 10, m.pal.Value.Bold(true).Render(itoa(total))),
				m.kvLine("OK", 10, m.pal.SuccessText.Render(itoa(t.OK))),
				m.kvLine("refused", 10, m.pal.ErrorText.Render(itoa(t.Refused))),
			}
			if t.Aborted > 0 {
				out = append(out, m.kvLine("aborted", 10, m.pal.WarningText.Render(itoa(t.Aborted))))
			}

			// The bar is the share that was ANSWERED, not the share that failed:
			// a monitor's default reading of a full bar should be a healthy
			// deployment, and the failures are already the red numbers above it.
			frac := float64(t.OK) / float64(total)
			bar := m.barCell(frac, max(8, min(inner-12, 24)))
			out = append(out, "", bar+" "+m.pal.Dim.Render(fmt.Sprintf("%.0f%% served", frac*100)))
			return out
		},
	}
}

// kvLine is one label and value of a tile: the label padded to a column so that
// the values of a tile line up, the value already styled by its caller.
func (m Model) kvLine(label string, width int, value string) string {
	return m.pal.Label.Render(padTo(clip(label, width), width)) + " " + value
}

// daemonTile is the daemon's settings and where each of them came from.
//
// THE SOURCE IS SHOWN FOR EVERY VALUE. "Why is it listening there" is the
// question this tile answers, and the answer is the second half of each line:
// a flag on the command line, an environment variable, the configuration file,
// or the binary's own default. The values alone would be a screenshot; the
// values with their origins are a diagnosis.
func (m Model) daemonTile() tileSpec {
	meta := ""
	for _, r := range m.obs.Daemon {
		if r.Name == "mode" {
			meta = r.Value
		}
	}

	return tileSpec{
		title:  "daemon",
		meta:   meta,
		weight: 2,
		body: func(inner int) []string {
			if len(m.obs.Daemon) == 0 {
				return []string{
					m.pal.Dim.Render("the daemon did not report its settings"),
					m.pal.Hint.Render("press i for what this process was started with"),
				}
			}
			label := min(12, max(8, inner/3))
			out := make([]string, 0, len(m.obs.Daemon))
			for _, r := range m.obs.Daemon {
				line := m.pal.Label.Render(padTo(clip(r.Name, label), label)) + " " +
					m.pal.Value.Render(r.Value)
				if r.Source != "" {
					line += "  " + m.pal.Dim.Render("· "+r.Source)
				}
				out = append(out, line)
			}
			return out
		},
	}
}

// providerTiles is one tile per provider.
func (m Model) providerTiles() []tileSpec {
	out := make([]tileSpec, 0, len(m.obs.Providers))
	for _, p := range m.obs.Providers {
		out = append(out, m.providerTile(p))
	}
	return out
}

// noProvidersTile stands in for the provider tiles when there are none.
func (m Model) noProvidersTile() tileSpec {
	return tileSpec{
		title:  "providers",
		weight: 1,
		body: func(int) []string {
			return []string{
				m.pal.Dim.Render("no providers are configured"),
				m.pal.Hint.Render("the daemon serves nothing until one is declared"),
			}
		},
	}
}

// providerTile is one provider: how it authenticates, whether its keys rotate,
// and the pool itself.
//
// A KEY BELONGS TO A PROVIDER, and this tile is where that shows: the same
// fingerprint can appear under two providers and mean two different things, and
// a fingerprint without its provider is not an identity.
func (m Model) providerTile(p ProviderState) tileSpec {
	return tileSpec{
		title:  p.ID,
		meta:   p.Type,
		weight: 1,
		body: func(inner int) []string {
			out := []string{
				m.kvLine("credential", 12, m.pal.Value.Render(p.Credential)),
				m.kvLine("keys", 12, m.pal.Value.Render(itoa(len(p.Keys)))),
				"  " + m.rotationLine(p),
			}

			if len(p.Keys) == 0 {
				// An anonymous provider has no pool by design, and an account
				// provider without keys could not have started. Both are stated
				// rather than left as a gap: a gap reads as a rendering fault.
				if p.Credential == "anonymous" {
					out = append(out, "", m.pal.Dim.Render("no keys — an anonymous credential presents none"))
				} else {
					out = append(out, "", m.pal.WarningText.Render("no keys in the pool"))
				}
				return out
			}

			cols := keyColumns(inner)
			out = append(out, "", m.keyHead(cols))
			for _, k := range p.Keys {
				out = append(out, m.keyRow(k, k.Fingerprint == p.Active, cols))
			}
			return out
		},
	}
}

// keyCols are the widths of a key table that fits in inner cells.
//
// THE TABLE LOSES ITS RIGHT SIDE FIRST: the bar and the share it stands for go
// together, because one without the other is a decoration, and the fingerprint
// and the two counters are what the reader came for. The fingerprint keeps a
// floor of eight cells — enough to tell two keys apart, which is all a
// fingerprint is for — and the table is never asked for less than that.
type keyCols struct {
	fp, ok, err, bar, share int
}

func keyColumns(inner int) keyCols {
	c := keyCols{fp: 14, ok: 7, err: 7, bar: 14, share: 5}
	const marker = 2

	if inner < marker+c.fp+c.ok+c.err+c.bar+c.share {
		c.bar, c.share = 0, 0
	}
	if inner < marker+c.fp+c.ok+c.err {
		c.fp = max(8, inner-marker-c.ok-c.err)
	}
	return c
}

// keyHead is the column titles of a key table.
func (m Model) keyHead(c keyCols) string {
	line := "  " + m.pal.Label.Render(padTo("key", c.fp))
	line += m.pal.Label.Render(padTo("OK", c.ok))
	line += m.pal.Label.Render(padTo("REFUSED", c.err))
	if c.bar > 0 {
		line += m.pal.Label.Render(padTo("", c.bar))
		line += m.pal.Label.Render(padTo("share", c.share))
	}
	return line
}

// addressesTile is the matrix: every egress address, and under it the models it
// carried.
//
// THE TILE IS THE WIDEST ONE, because this is the only part of the dashboard
// whose content grows with the deployment, and the only one where a column can
// be dropped by a narrow frame — which is why it takes the full width instead of
// sharing a row.
func (m Model) addressesTile() tileSpec {
	meta := "no addresses"
	if n := len(m.obs.Addresses); n > 0 {
		meta = plural(n, "address", "addresses") + " · sort " + m.sort.String()
	}

	return tileSpec{
		title:  "addresses",
		meta:   meta,
		weight: 1,
		body: func(inner int) []string {
			rows := m.sortedAddresses()
			if len(rows) == 0 {
				return []string{
					m.pal.Dim.Render("no address has served a request yet"),
					m.pal.Hint.Render("the matrix fills in as the daemon forwards traffic"),
				}
			}

			cols := addressCols(rows, inner)
			out := make([]string, 0, len(rows)*3+2)
			out = append(out,
				renderCells(colTitles(cols), cols),
				m.pal.Rule.Render(repeat("─", min(inner, colsWidth(cols)))),
			)

			for i, r := range rows {
				if i > 0 {
					// A rule between address groups rather than a zebra stripe:
					// the rows nest one level, and a nested zebra reads as a
					// rendering fault.
					out = append(out, m.pal.Rule.Render(repeat("·", min(inner, colsWidth(cols)))))
				}
				out = append(out, m.addressRow(r, cols))
				for _, mr := range r.Models {
					out = append(out, m.modelRow(r, mr, cols))
				}
			}
			return out
		},
	}
}

// tileTitleStyle is the heading of a tile, one step quieter than a section's.
func (p palette) tileTitleStyle() lipgloss.Style {
	return p.TileTitle.Bold(true)
}
