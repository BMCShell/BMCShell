package render

import (
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	extast "github.com/yuin/goldmark/extension/ast"
)

// TextTable writes one wrapped row at a time. An empty table produces no output.
type TextTable struct {
	out      io.Writer
	headings []string
	widths   []int
	started  bool
}

// NewTextTable fits the headings and preferred cell widths to the terminal width.
func NewTextTable(out io.Writer, width int, headings []string, preferredWidths []int) *TextTable {
	widths := make([]int, len(headings))
	for i, heading := range headings {
		for line := range strings.SplitSeq(heading, "\n") {
			widths[i] = max(widths[i], textWidth(line))
		}
		if i < len(preferredWidths) {
			widths[i] = max(widths[i], preferredWidths[i])
		}
	}
	fitColumns(widths, width-3*len(widths)-1)
	return &TextTable{out: out, headings: append([]string(nil), headings...), widths: widths}
}

// Row writes the heading on the first call and separates subsequent entries.
func (table *TextTable) Row(cells []string) {
	if table.out == nil || len(table.widths) == 0 {
		return
	}
	var row strings.Builder
	if !table.started {
		writeBorder(&row, table.widths, "┌", "┬", "┐")
		writeTableRow(&row, table.headings, table.widths, nil)
		table.started = true
	}
	writeBorder(&row, table.widths, "├", "┼", "┤")
	writeTableRow(&row, cells, table.widths, nil)
	io.WriteString(table.out, row.String())
}

// Close writes the bottom border after the last row.
func (table *TextTable) Close() {
	if table.out != nil && table.started {
		var border strings.Builder
		writeBorder(&border, table.widths, "└", "┴", "┘")
		io.WriteString(table.out, border.String())
	}
	table.out = nil
}

// WrapText breaks text at terminal cell boundaries and preserves explicit newlines.
func WrapText(value string, width int) []string {
	return wrapCell(value, max(2, width))
}

func (body markdownBody) table(table *extast.Table, width int) string {
	widths := make([]int, len(table.Alignments))
	var rows [][]string
	for row := table.FirstChild(); row != nil; row = row.NextSibling() {
		cells := make([]string, len(widths))
		column := 0
		for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
			cells[column] = strings.ReplaceAll(body.inline(cell), "\t", "    ")
			for line := range strings.SplitSeq(cells[column], "\n") {
				widths[column] = max(widths[column], textWidth(line))
			}
			column++
		}
		rows = append(rows, cells)
	}

	fitColumns(widths, width-3*len(widths)-1)
	var out strings.Builder
	writeBorder(&out, widths, "┌", "┬", "┐")
	for i, cells := range rows {
		writeTableRow(&out, cells, widths, table.Alignments)
		if i == 0 && len(rows) > 1 {
			writeBorder(&out, widths, "├", "┼", "┤")
		}
	}
	writeBorder(&out, widths, "└", "┴", "┘")
	return strings.TrimSuffix(out.String(), "\n")
}

// fitColumns keeps narrow columns at their natural width and shares the
// remaining space between wide columns. Every column can hold a wide rune.
func fitColumns(widths []int, available int) {
	available = max(available, 2*len(widths))
	total := 0
	for i, width := range widths {
		widths[i] = max(2, width)
		total += widths[i]
	}
	if total <= available {
		return
	}

	low, high := 2, available
	for low < high {
		capWidth := low + (high-low+1)/2
		if limitedWidth(widths, capWidth) <= available {
			low = capWidth
		} else {
			high = capWidth - 1
		}
	}
	remaining := available - limitedWidth(widths, low)
	for i, width := range widths {
		widths[i] = min(width, low)
		if width > low && remaining > 0 {
			widths[i]++
			remaining--
		}
	}
}

func limitedWidth(widths []int, capWidth int) int {
	total := 0
	for _, width := range widths {
		total += min(width, capWidth)
	}
	return total
}

func writeBorder(out *strings.Builder, widths []int, left, middle, right string) {
	out.WriteString(left)
	for i, width := range widths {
		if i > 0 {
			out.WriteString(middle)
		}
		out.WriteString(strings.Repeat("─", width+2))
	}
	out.WriteString(right)
	out.WriteByte('\n')
}

func writeTableRow(out *strings.Builder, cells []string, widths []int, alignments []extast.Alignment) {
	wrapped := make([][]string, len(widths))
	height := 1
	for i, cell := range cells[:min(len(cells), len(widths))] {
		wrapped[i] = wrapCell(cell, widths[i])
		height = max(height, len(wrapped[i]))
	}
	for line := range height {
		out.WriteString("│")
		for column, chunks := range wrapped {
			value := ""
			if line < len(chunks) {
				value = chunks[line]
			}
			gap := widths[column] - textWidth(value)
			left := 0
			if column < len(alignments) {
				switch alignments[column] {
				case extast.AlignCenter:
					left = gap / 2
				case extast.AlignRight:
					left = gap
				}
			}
			out.WriteString(strings.Repeat(" ", 1+left))
			out.WriteString(value)
			out.WriteString(strings.Repeat(" ", 1+gap-left))
			out.WriteString("│")
		}
		out.WriteByte('\n')
	}
}

func wrapCell(value string, width int) []string {
	var lines []string
	for line := range strings.SplitSeq(value, "\n") {
		for end := cellLineEnd(line, width); end < len(line); end = cellLineEnd(line, width) {
			lines = append(lines, line[:end])
			line = line[end:]
		}
		lines = append(lines, line)
	}
	return lines
}

func cellLineEnd(line string, width int) int {
	used, lastBreak := 0, 0
	for i, r := range line {
		used += RuneWidth(r)
		if used > width {
			if lastBreak > 0 {
				return lastBreak
			}
			return i
		}
		if unicode.IsSpace(r) {
			lastBreak = i + utf8.RuneLen(r)
		}
	}
	return len(line)
}

func textWidth(value string) int {
	width := 0
	for _, r := range value {
		width += RuneWidth(r)
	}
	return width
}

// RuneWidth estimates the number of terminal cells occupied by a rune.
func RuneWidth(r rune) int {
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
		return 0
	}
	if unicode.In(r, unicode.Han, unicode.Hangul, unicode.Hiragana, unicode.Katakana) ||
		(r >= 0x3000 && r <= 0x303f) || (r >= 0xff01 && r <= 0xff60) {
		return 2
	}
	return 1
}
