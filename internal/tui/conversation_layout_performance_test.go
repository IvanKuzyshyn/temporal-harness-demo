package tui

import (
	"reflect"
	"strings"
	"testing"
)

// Repeated suffix truncation allocated once per row (and scanned the whole
// remainder twice). A bounded 4 KiB ASCII body should need only the row slice,
// not thousands of temporary truncation buffers at tiny widths.
func TestReflowASCIIProjectionAllocations(t *testing.T) {
	const bodyBytes = 4096
	m := &Model{
		width:         1,
		journalParts:  []string{"H\n  " + strings.Repeat("x", bodyBytes)},
		journalBlocks: []conversationBlock{{headingBytes: 1, inset: 2}},
	}
	var rows []conversationLine
	allocs := testing.AllocsPerRun(1, func() { rows = m.partDisplayLines(0) })
	if len(rows) != bodyBytes+1 {
		t.Fatalf("got %d rows, want heading plus %d body rows", len(rows), bodyBytes)
	}
	for i, row := range rows[1:] {
		if row.text != "x" || row.line != 1 || row.cell != i || row.heading {
			t.Fatalf("body row %d lost text/source geometry: %+v", i, row)
		}
	}
	t.Logf("4 KiB width-one ASCII projection: %.0f allocations", allocs)
	// Allow ample slice-growth/compiler variation, but not per-row buffers.
	if allocs > 64 {
		t.Fatalf("ASCII projection allocated %.0f times; want <=64 without per-row truncation buffers", allocs)
	}
}

func TestReflowASCIIProjectionGeometry(t *testing.T) {
	for _, tc := range []struct {
		name, part string
		width      int
		want       []conversationLine
	}{
		{"words", "H\n  one two three", 9, []conversationLine{
			{"H", 0, 0, true}, {"  one ", 1, 0, false}, {"  two ", 1, 4, false}, {"  three", 1, 8, false},
		}},
		{"exact", "H\n  abcdefg", 9, []conversationLine{
			{"H", 0, 0, true}, {"  abcdefg", 1, 0, false},
		}},
		{"identifier", "H\n  abcdefgh", 9, []conversationLine{
			{"H", 0, 0, true}, {"  abcdefg", 1, 0, false}, {"  h", 1, 7, false},
		}},
		{"spaces", "H\n    abc  ", 7, []conversationLine{
			{"H", 0, 0, true}, {"    ", 1, 0, false}, {"  abc  ", 1, 2, false},
		}},
		{"empty", "H\n  ", 1, []conversationLine{
			{"H", 0, 0, true}, {"", 1, 0, false},
		}},
		{"zero", "H\n  ab", 0, []conversationLine{
			{"H", 0, 0, true}, {"a", 1, 0, false}, {"b", 1, 1, false},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &Model{width: tc.width, journalParts: []string{tc.part}, journalBlocks: []conversationBlock{{headingBytes: 1, inset: 2}}}
			if got := m.partDisplayLines(0); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("projection = %#v, want %#v", got, tc.want)
			}
		})
	}
}
