package pdcmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/rivo/uniseg"
)

// A rune is not a terminal cell: Hangul occupies two, combining marks none,
// and joined emoji can be several runes in one glyph. Use the grapheme rules
// already linked by the CLI instead of keeping another Unicode range table.
func displayWidth(s string) int { return uniseg.StringWidth(s) }

func padCells(s string, width int) string {
	if n := width - displayWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

func printCells(w io.Writer, rows [][]string) error {
	var widths []int
	for _, row := range rows {
		for i, cell := range row {
			if i == len(widths) {
				widths = append(widths, 0)
			}
			if n := displayWidth(cell); n > widths[i] {
				widths[i] = n
			}
		}
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(row)-1 {
				cell = padCells(cell, widths[i]+3)
			}
			if _, err := io.WriteString(w, cell); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}
	return nil
}
