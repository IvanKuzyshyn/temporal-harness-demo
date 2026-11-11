package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestReflowBudgetOmitsOnlyMappedTargets(t *testing.T) {
	m, _ := testModel(t)
	r := durableRecords(64)
	for i := range r {
		// Per-record input fits 4 KiB, but canonical body gutters make this
		// allowed loaded page exceed the aggregate window bound.
		r[i].Message.Content = fmt.Sprintf("DURABLE-%03d\n", i) + strings.Repeat("\n\nx", 1350)
	}
	m.history.page.Entries = r
	m.selectedID = r[63].ID
	m.rebuildJournal()
	assertReflowRows(t, m)
	last := ""
	for _, b := range m.journalBlocks {
		if b.id == r[63].ID {
			t.Fatal("fixture did not exceed canonical budget")
		}
		if b.id != "" {
			last = b.id
		}
	}
	if last == "" || m.selectedID != "" {
		t.Fatal("omitted durable target remained selected, or fixture has no targets")
	}
	m.selectRecord(0)
	if m.selectedID != last || !strings.Contains(ansi.Strip(m.View().Content), "> › You") {
		t.Fatal("navigation selected an omitted heading rather than actual retained content")
	}
	assertReflowRows(t, m)
	m.prompt.SetValue("/detail 64")
	m.submitPrompt(false)
	if !strings.Contains(ansi.Strip(m.View().Content), "DURABLE-063") || m.history.detailID != r[63].ID {
		t.Fatal("aggregate window limit lost the explicit durable source route")
	}
	assertReflowRows(t, m)
}
