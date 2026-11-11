package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestApprovalActualFrameAttentionHeading(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {40, 12}, {39, 12}, {16, 5}, {8, 2}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			m, f := approvalModel(t)
			// Fixed countdown, with untrusted preview resembling a trusted label.
			f.state.PendingApproval.ExpiresAt = time.Now().Add(-time.Minute)
			f.state.PendingApproval.Preview = "Approval blocked\nUNTRUSTED-PREVIEW\x1b[31m"
			m.Update(snapshotMsg{epoch: m.epoch, query: m.queryID, state: f.state})
			m.resize(size[0], size[1])
			m.Update(key('d', 0))
			m.Update(tea.PasteMsg{Content: "NEUTRAL-REASON"})
			frame := m.View().Content
			label := "Approval blocked · expired"
			if !m.approvalFits() {
				label = "RESIZE: approval paused"
			}
			// Compare the actual first row to styling applied AFTER cell truncation,
			// including the ellipsis at narrow sizes, not an unused helper result.
			label = ansi.Truncate(label, m.width, "…")
			first, rest, _ := strings.Cut(frame, "\n")
			if strings.TrimRight(first, " ") != semanticHeading(label, "attention") || !strings.Contains(first, "38;5;214") {
				t.Fatalf("actual approval heading lacks bounded amber span: %q", first)
			}
			if strings.Contains(rest, "\x1b") {
				t.Fatalf("preview/arguments/reason inherited semantic color: %q", rest)
			}
			if lipgloss.Width(frame) > m.width || lipgloss.Height(frame) > m.height {
				t.Fatal("styled approval exceeded terminal cells")
			}
			if m.approvalFits() && (!strings.Contains(rest, "UNTRUSTED-PREVIEW") || !strings.Contains(rest, "NEUTRAL-REASON")) {
				t.Fatalf("fixture must expose neutral preview and denial input: %s", rest)
			}
			// This checks model-level textual meaning, not terminal negotiation;
			// root's PTY qualification owns actual NO_COLOR emission.
			t.Setenv("NO_COLOR", "1")
			if plain := ansi.Strip(m.View().Content); plain != ansi.Strip(frame) || !strings.HasPrefix(plain, label) {
				t.Fatalf("no-color meaning changed: %q", plain)
			}
		})
	}
}
