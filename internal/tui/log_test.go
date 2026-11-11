package tui

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
)

func TestCapturedLogRefCanonicalOnly(t *testing.T) {
	valid := `{"toolCallId":"c","isError":false,"output":"tail","details":{"exitCode":"0","logRef":"REF"}}`
	if capturedLogRef("run_command", "c", valid) != "REF" {
		t.Fatal("valid ref refused")
	}
	for _, content := range []string{
		`{"Details":{"logRef":"EVIL"},` + valid[1:],
		strings.Replace(valid, `"logRef":"REF"`, `"logRef":"REF","logRef":"EVIL"`, 1),
		strings.Replace(valid, `"logRef":"REF"`, `"LogRef":"EVIL"`, 1),
		strings.Replace(valid, `"exitCode":"0"`, `"exitCode":null`, 1),
		strings.Replace(valid, `"toolCallId":"c"`, `"toolCallId":"other"`, 1),
		`{"logRef":"EVIL",` + valid[1:],
	} {
		if got := capturedLogRef("run_command", "c", content); got != "" {
			t.Fatalf("ambiguous ref %q from %s", got, content)
		}
	}
	if capturedLogRef("read_file", "c", valid) != "" {
		t.Fatal("read bytes parsed as log")
	}
}
func TestLogPagingBeyondModelTailAndStaleResponses(t *testing.T) {
	m, _ := testModel(t)
	dir := filepath.Join(t.TempDir(), "raw\nroot")
	m.history.dir = dir
	ref := fmt.Sprintf("artifacts/%x/cmd-%s.log", sha256.Sum256([]byte(m.opts.SessionID)), strings.Repeat("a", 64))
	path := filepath.Join(dir, ref)
	os.MkdirAll(filepath.Dir(path), 0700)
	os.WriteFile(path, []byte(strings.Repeat("first\n", 4000)+"MIDDLE-REAL-LOG\n"+strings.Repeat("last\n", 5000)), 0600)
	m.history.page.Entries = durableRecords(1)
	m.selectedID = m.history.page.Entries[0].ID
	m.history.page.Entries[0].Message = llm.Message{Role: "tool", ToolName: "run_command", ToolCallID: "c", Content: fmt.Sprintf(`{"toolCallId":"c","isError":false,"output":"last","details":{"exitCode":"0","logRef":%q}}`, ref)}
	m.prompt.SetValue("keep draft")
	_, cmd := m.Update(key('l', tea.ModCtrl))
	if cmd == nil {
		t.Fatal("no deferred log read")
	}
	old := cmd()
	m.Update(old)
	_, cmd = m.Update(key(tea.KeyRight, tea.ModCtrl))
	if cmd == nil {
		t.Fatal("no next page")
	}
	m.Update(cmd())
	if !strings.Contains(m.viewport.GetContent(), "MIDDLE-REAL-LOG") || m.prompt.Value() != "keep draft" {
		t.Fatal("middle output unavailable or draft lost")
	}
	visible := false
	for i := 0; i < 300; i++ {
		view := m.View().Content
		if !strings.Contains(view, "keep draft") {
			t.Fatal("log navigation hid composer")
		}
		if strings.Contains(view, "MIDDLE-REAL-LOG") {
			visible = true
			break
		}
		m.Update(key(tea.KeyPgDown, 0))
	}
	if !visible {
		t.Fatal("middle log line could not be brought into visible terminal viewport")
	}
	m.Update(old)
	if !strings.Contains(m.viewport.GetContent(), "MIDDLE-REAL-LOG") {
		t.Fatal("stale page replaced current")
	}
	// Reopening the same record cannot reuse a generation from a previous visit.
	m.toggleDetail()
	cmd = m.openSelectedLog()
	if cmd == nil {
		t.Fatal("cannot reopen")
	}
	m.Update(old)
	if !m.log.pending {
		t.Fatal("stale previous visit accepted")
	}
	m.Update(cmd())
	m.switchSession("other")
	m.Update(old)
	if m.log.ref != "" {
		t.Fatal("cross-session response accepted")
	}
}
