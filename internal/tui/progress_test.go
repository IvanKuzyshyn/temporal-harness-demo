package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/charmbracelet/x/ansi"
)

func TestProgressDoesNotClassifySanitizedEnums(t *testing.T) {
	m, _ := testModel(t)
	m.state = displayState(workflows.State{Status: "wor\x1bking", Mode: "de\x1bmo", Children: []workflows.ChildState{{Status: "wor\x1bking"}}})
	v := m.progressView()
	if strings.Contains(v, "harness-enforced") || strings.Contains(v, "children:1") || strings.Contains(v, m.spinner.View()+" ") {
		t.Fatalf("sanitized display invented activity/mode: %s", v)
	}
}

func TestNoColorMeaningAndPollingIsNotActivity(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	for _, status := range []string{"succeeded", "failed", "failed/denied (see captured result)", "interrupted", "outcome unavailable"} {
		if ansi.Strip(outcomeLabel(status)) != "["+status+"]" {
			t.Fatal("color-only outcome")
		}
	}
	m, _ := testModel(t)
	m.state.Status = "idle"
	m.queryPending = true
	if strings.Contains(m.progressView(), m.spinner.View()+" ") {
		t.Fatal("query polling presented as agent activity")
	}
	m.state.Status = "working"
	m.queryPending = false
	if !strings.Contains(m.progressView(), m.spinner.View()+" ") || strings.Contains(m.progressView(), "%") {
		t.Fatal("missing indeterminate activity")
	}
}

func TestProgressTruthAndFinishedElapsed(t *testing.T) {
	m, _ := testModel(t)
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	m.now = now
	m.state = displayState(workflows.State{Status: "idle", Phase: "settled", Mode: "normal", PhaseStartedAt: now.Add(-20 * time.Second), RunStartedAt: now.Add(-30 * time.Second), RunFinishedAt: now.Add(-10 * time.Second)})
	v := m.progressView()
	if !strings.Contains(v, "20s") || !strings.Contains(v, "heartbeat unknown") || !strings.Contains(v, "normal") {
		t.Fatalf("dishonest progress: %s", v)
	}
	m.now = now.Add(time.Hour)
	if v != m.progressView() {
		t.Fatal("finished elapsed advances")
	}
	m.state.Status = "working"
	m.state.Phase = "generating"
	m.state.RunFinishedAt = time.Time{}
	m.queryPending = false
	if !strings.Contains(m.progressView(), "generating") {
		t.Fatal("missing actual phase")
	}
	raw := events.Event{Seq: 1, Type: events.Heartbeat, WorkflowID: "one", TS: now.Format(time.RFC3339Nano)}
	m.applyEvent(compactEvent(raw))
	if m.heartbeat != now {
		t.Fatal("timestamp lost during compaction")
	}
	m.state.Mode = "demo"
	if !strings.Contains(m.progressView(), "harness-enforced") {
		t.Fatal("demo not distinguished")
	}
}
