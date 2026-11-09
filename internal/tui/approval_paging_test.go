package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func approvalScreen(t *testing.T, m *Model) string {
	t.Helper()
	s := m.View().Content
	if lipgloss.Width(s) > m.width || lipgloss.Height(s) > m.height {
		t.Fatalf("screen exceeds %dx%d: %q", m.width, m.height, s)
	}
	for _, control := range []string{"Esc stop", "Ctrl+C quit", "PgUp/PgDn"} {
		if !strings.Contains(s, control) {
			t.Fatalf("missing %q: %s", control, s)
		}
	}
	return s
}

func TestApprovalRenderedNavigation(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {40, 14}} {
		for _, payload := range []int{1024, maxText * 2} {
			t.Run(fmt.Sprintf("%dx%d/args%d", size[0], size[1], payload), func(t *testing.T) {
				m, f := approvalModel(t)
				m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				args, _ := json.Marshal(map[string]string{"content": strings.Repeat("x", payload), "path": "target.txt"})
				p := f.state.PendingApproval
				p.Call.Args = args
				p.Preview = "--- old\n+++ new\n-ORIGINAL_VALUE\n+NEW_VALUE\n" + strings.Repeat(" context\n", 60) + "-LAST_OLD\n+LAST_NEW"
				m.Update(snapshotMsg{epoch: m.epoch, query: m.queryID, state: f.state, at: time.Now()})
				first := approvalScreen(t, m)
				for _, text := range []string{"write_file", "target.txt", "a approve", "d deny"} {
					if !strings.Contains(first, text) {
						t.Fatalf("initial summary missing %q: %s", text, first)
					}
				}
				if !strings.Contains(first, "more below") {
					t.Fatal("overflow is not indicated")
				}
				seen := first
				for i := 0; i < 100; i++ {
					m.Update(key(tea.KeyPgDown, 0))
					s := approvalScreen(t, m)
					if !strings.Contains(s, "a approve") || !strings.Contains(s, "d deny") {
						t.Fatal("decision controls scrolled away")
					}
					seen += "\n" + s
				}
				for _, text := range []string{"-ORIGINAL_VALUE", "+NEW_VALUE", "-LAST_OLD", "+LAST_NEW", "Arguments:", "content"} {
					if !strings.Contains(seen, text) {
						t.Fatalf("unreachable %q in rendered pages", text)
					}
				}
				if payload > maxText && !strings.Contains(seen, "[arguments truncated]") {
					t.Fatal("argument truncation not indicated")
				}
				m.Update(key(tea.KeyHome, 0))
				if !strings.Contains(approvalScreen(t, m), "target.txt") {
					t.Fatal("Home did not restore summary")
				}
				m.Update(key(tea.KeyEnd, 0))
				end := approvalScreen(t, m)
				m.Update(key(tea.KeyPgUp, 0))
				if approvalScreen(t, m) == end {
					t.Fatal("Page Up did not navigate")
				}
			})
		}
	}
}

func TestApprovalPagingWhileDenyingRecordingAndSent(t *testing.T) {
	for _, mode := range []string{"denying", "pending", "sent"} {
		t.Run(mode, func(t *testing.T) {
			m, f := approvalModel(t)
			m.resize(40, 14)
			f.state.PendingApproval.Preview = strings.Repeat("context\n", 40) + "-END_OLD\n+END_NEW"
			m.Update(snapshotMsg{epoch: m.epoch, query: m.queryID, state: f.state})
			m.Update(key('d', 0))
			m.Update(tea.PasteMsg{Content: "review reason"})
			m.approval.pending = mode == "pending"
			m.approval.sent = mode == "sent"
			before := approvalScreen(t, m)
			m.Update(key(tea.KeyDown, 0))
			if approvalScreen(t, m) == before {
				t.Fatal("Down did not scroll")
			}
			m.Update(key(tea.KeyUp, 0))
			if approvalScreen(t, m) != before {
				t.Fatal("Up did not restore page")
			}
			m.Update(key(tea.KeyEnd, 0))
			if !strings.Contains(approvalScreen(t, m), "+END_NEW") {
				t.Fatal("End did not expose preview tail")
			}
			if m.approval.reason != "review reason" {
				t.Fatal("navigation changed reason")
			}
			_, cmd := m.Update(key(tea.KeyEscape, 0))
			if cmd == nil {
				t.Fatal("Esc intercepted")
			}
			cmd()
			if f.stops != 1 {
				t.Fatal("stop missing")
			}
			_, cmd = m.Update(key('c', tea.ModCtrl))
			if cmd == nil || m.ctx.Err() == nil {
				t.Fatal("Ctrl+C intercepted")
			}
		})
	}
}

func TestApprovalPagerResizeAndSnapshotIdentity(t *testing.T) {
	m, f := approvalModel(t)
	f.state.PendingApproval.Preview = strings.Repeat("context\n", 60) + "+TAIL"
	m.Update(snapshotMsg{epoch: m.epoch, query: m.queryID, state: f.state})
	m.Update(key(tea.KeyEnd, 0))
	offset := m.approval.offset
	m.Update(snapshotMsg{epoch: m.epoch, query: m.queryID, state: f.state})
	if m.approval.offset != offset {
		t.Fatal("periodic snapshot reset review position")
	}
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 14})
	m.Update(key(tea.KeyEnd, 0))
	if !strings.Contains(approvalScreen(t, m), "+TAIL") {
		t.Fatal("resize lost access to preview")
	}
	m.Update(key('d', 0))
	m.Update(tea.PasteMsg{Content: "old reason"})
	f.state.PendingApproval.ActionHash = "changed"
	m.Update(snapshotMsg{epoch: m.epoch, query: m.queryID, state: f.state})
	if m.approval.offset != 0 || m.approval.reason != "" || m.approval.denying {
		t.Fatal("new action inherited review position or denial")
	}
	if !strings.Contains(approvalScreen(t, m), "Path: x") {
		t.Fatal("new action did not start at summary")
	}
}

func TestApprovalDenialFooter(t *testing.T) {
	m, _ := approvalModel(t)
	m.resize(40, 14)
	m.Update(key('d', 0))
	m.Update(key(tea.KeyEnter, 0))
	if !strings.Contains(approvalScreen(t, m), "reason required") {
		t.Fatal("empty-reason validation hidden")
	}
	m.Update(tea.PasteMsg{Content: strings.Repeat("界", 100) + "reason-tail"})
	if !strings.Contains(approvalScreen(t, m), "reason-tail") {
		t.Fatal("reason input tail hidden")
	}
	m.Update(approvalMsg{epoch: m.epoch, requestID: m.approval.requestID, hash: m.approval.hash, err: errors.New("backend unavailable")})
	if !strings.Contains(approvalScreen(t, m), "backend unavailable") {
		t.Fatal("denial submission error hidden")
	}
}

func TestApprovalRawSummaryAndBounds(t *testing.T) {
	m, f := approvalModel(t)
	p := f.state.PendingApproval
	p.RequestID = strings.Repeat("request", 50)
	p.ActionHash = "raw\x1bhash"
	p.Call.Args = []byte(`{"content":"` + strings.Repeat("x", maxText*2) + `","path":"safe\u001b\u202etarget.txt"}`)
	p.Preview = strings.Repeat("line\n", maxText) + "UNRETAINED"
	m.Update(snapshotMsg{epoch: m.epoch, query: m.queryID, state: f.state})
	if !strings.Contains(m.View().Content, "safetarget.txt") {
		t.Fatal("raw arguments not parsed/sanitized before truncation")
	}
	seen := ""
	for i := 0; i < 300; i++ {
		seen += m.View().Content
		m.Update(key(tea.KeyPgDown, 0))
	}
	if !strings.Contains(seen, "[preview truncated]") || strings.Contains(seen, "UNRETAINED") {
		t.Fatal("preview bound not indicated/enforced")
	}
	_, cmd := m.Update(key('a', 0))
	if cmd == nil {
		t.Fatal("no command")
	}
	m.Update(cmd())
	if len(f.decisions) != 1 || f.decisions[0].RequestID != p.RequestID || f.decisions[0].ActionHash != p.ActionHash {
		t.Fatal("display changed raw identity")
	}
}
