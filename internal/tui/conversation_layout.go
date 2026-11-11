package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// One transient projection supplies rendering AND navigation geometry. Retain
// canonical logical lines, not width-specific copies, while browsing. Each row
// points to its original cell so repeated word-wrapped reflow cannot drift.
type conversationLine struct {
	text       string
	line, cell int
	heading    bool
}

func (m *Model) partDisplayLines(index int) []conversationLine {
	part, block := m.journalParts[index], m.journalBlocks[index]
	headingLines := strings.Count(part[:block.headingBytes], "\n") + 1
	var out []conversationLine
	for line, text := range strings.Split(part, "\n") {
		heading := line < headingLines
		if block.inset == 0 {
			// Literal panels are soft-wrapped by the viewport. Match its cell
			// slicing exactly, including wide glyphs crossing a row boundary.
			width := max(1, m.width)
			count := max(1, (ansi.StringWidth(text)+width-1)/width)
			for row := 0; row < count; row++ {
				out = append(out, conversationLine{line: line, cell: row * width, heading: heading})
			}
			continue
		}
		inset := 0
		if !heading {
			inset = min(block.inset, max(0, m.width-2))
		}
		prefix := strings.Repeat(" ", inset)
		if inset > 0 {
			text = strings.TrimPrefix(text, strings.Repeat(" ", block.inset))
		}
		width := max(1, m.width-inset)
		cell := 0
		for {
			size := ansi.StringWidth(text)
			if size <= width {
				out = append(out, conversationLine{prefix + text, line, cell, heading})
				break
			}
			chunk := ansi.Truncate(text, width, "")
			n := ansi.StringWidth(chunk)
			if inset > 0 {
				// Prefer a word boundary, but hard-wrap oversized identifiers/evidence.
				if space := strings.LastIndexByte(chunk, ' '); space > 0 {
					chunk = chunk[:space+1]
					n = ansi.StringWidth(chunk)
				}
			}
			if n == 0 { // A wide glyph in a one-cell terminal: retain cell-sliced fallback.
				n = width
				chunk = ansi.Cut(text, 0, n)
			}
			out = append(out, conversationLine{prefix + chunk, line, cell, heading})
			text = ansi.Cut(text, n, size)
			cell += n
		}
	}
	return out
}
