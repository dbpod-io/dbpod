package cmd

import (
	"io"
	"strings"
)

// table renders simple whitespace-aligned tables for list commands.
type table struct {
	w       io.Writer
	headers []string
	rows    [][]string
	widths  []int
}

func newTable(w io.Writer, headers ...string) *table {
	t := &table{w: w, headers: headers, widths: make([]int, len(headers))}
	for i, h := range headers {
		t.widths[i] = len(h)
	}
	return t
}

func (t *table) row(cells ...string) {
	for len(t.widths) < len(cells) {
		t.widths = append(t.widths, 0)
	}
	for i, c := range cells {
		if n := runewidth(c); n > t.widths[i] {
			t.widths[i] = n
		}
	}
	t.rows = append(t.rows, cells)
}

func (t *table) flush() error {
	var b strings.Builder
	writeCells(&b, t.headers, t.widths)
	b.WriteString(strings.Repeat("-", totalWidth(t.widths)) + "\n")
	for _, r := range t.rows {
		writeCells(&b, r, t.widths)
	}
	_, err := io.WriteString(t.w, b.String())
	return err
}

func writeCells(b *strings.Builder, cells []string, widths []int) {
	for i, c := range cells {
		pad := widths[i] - runewidth(c)
		b.WriteString(c + strings.Repeat(" ", pad+2))
	}
	b.WriteString("\n")
}

func totalWidth(widths []int) int {
	n := 0
	for _, w := range widths {
		n += w + 2
	}
	if n > 2 {
		return n - 2
	}
	return n
}

// runewidth approximates display width; CJK chars count as 2 columns.
func runewidth(s string) int {
	n := 0
	for _, r := range s {
		if r > 0x1100 && (r <= 0x115F || // Hangul Jamo
			r == 0x2329 || r == 0x232A ||
			(r >= 0x2E80 && r <= 0xA4CF && r != 0x303F) || // CJK ...
			(r >= 0xAC00 && r <= 0xD7A3) || // Hangul syllables
			(r >= 0xF900 && r <= 0xFAFF) || // CJK compat ideographs
			(r >= 0xFE30 && r <= 0xFE4F) ||
			(r >= 0xFF00 && r <= 0xFF60) || // fullwidth forms
			(r >= 0xFFE0 && r <= 0xFFE6)) {
			n += 2
		} else {
			n++
		}
	}
	return n
}
