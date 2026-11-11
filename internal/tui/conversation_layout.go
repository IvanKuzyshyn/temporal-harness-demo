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
		if block.inset == 0 || m.journalCompact {
			// Literal panels are soft-wrapped by the viewport. Match its cell
			// slicing exactly, including wide glyphs crossing a row boundary.
			width := max(1, m.width)
			count := max(1, (ansi.StringWidth(text)+width-1)/width)
			for row := 0; row < count; row++ {
				cell := row * width
				if !heading && block.inset > 0 {
					// Prose anchors exclude the canonical body gutter in both
					// presentation modes; literal panels keep native cells.
					cell = max(0, cell-block.inset)
				}
				out = append(out, conversationLine{line: line, cell: cell, heading: heading})
			}
			continue
		}
		inset := 0
		if !heading {
			inset = min(block.inset, max(0, m.width-2))
		}
		prefix := strings.Repeat(" ", inset)
		if !heading {
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
			consumed := len(chunk)
			if consumed == 0 || n == 0 {
				// A cluster wider than the terminal cannot be shown. Consume
				// the WHOLE source cluster, not a cell slice that may return
				// that same glyph forever. Keep its source-cell position on
				// an empty row; canonical text survives a normal-width restore.
				cluster, cells := ansi.FirstGraphemeCluster(text, ansi.GraphemeWidth)
				consumed, n = len(cluster), cells
				chunk = ansi.Truncate(cluster, width, "")
			}
			out = append(out, conversationLine{prefix + chunk, line, cell, heading})
			text = text[consumed:]
			cell += n
		}
	}
	return out
}
