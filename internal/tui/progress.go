package tui

import (
	"fmt"
	"time"
)

func elapsed(start, end time.Time) string {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return "unknown"
	}
	return end.Sub(start).Truncate(time.Second).String()
}
func (m *Model) progressView() string {
	phase, mode := m.state.Phase, m.state.Mode
	if phase == "" {
		phase = "unknown"
	}
	if mode == "" {
		mode = "unknown"
	}
	if mode == "demo" {
		mode = "demo (harness-enforced)"
	}
	end := m.state.RunFinishedAt
	active := m.state.Status == "working" || m.state.Status == "blocked"
	if active && end.IsZero() {
		end = m.now
	}
	clock := "run " + elapsed(m.state.RunStartedAt, end)
	if active {
		clock = "phase " + elapsed(m.state.PhaseStartedAt, end)
	}
	heartbeat := "heartbeat unknown"
	if !m.heartbeat.IsZero() {
		heartbeat = "heartbeat " + elapsed(m.heartbeat, m.now) + " ago (observed)"
	}
	activity := ""
	if active {
		activity = m.spinner.View() + " "
	}
	children := 0
	for _, child := range m.state.Children {
		if child.Status == "working" || child.Status == "blocked" {
			children++
		}
	}
	return fmt.Sprintf("%smode:%s · %s · %s\ntools:%d children:%d · %s", activity, mode, phase, clock, len(m.state.ActiveTools), children, heartbeat)
}
