package tui

import (
	"encoding/json"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"strings"
	"testing"
)

func TestDemoSubmitConfigurationMismatch(t *testing.T) {
	for _, scenario := range []string{"", "demo", "parallel"} {
		t.Run(scenario, func(t *testing.T) {
			f := &fakeBackend{}
			m := New(f, Options{SessionID: "s", Model: "mock", Input: workflows.Input{Model: "mock", Workdir: "/root", DataDir: "/data", Scenario: scenario}})
			defer m.Close()
			m.resize(80, 24)
			runCommand(t, m, "/demo", 0)
			if len(f.messages) != 1 || f.messages[0].Text != "/demo" {
				t.Errorf("demo must submit explicit token: %+v", f.messages)
			}
			s := workflows.State{Session: &workflows.SessionInfo{Model: "mock", Workdir: "/root", DataDir: "/data", Scenario: "demo"}}
			m.Update(snapshotMsg{epoch: m.epoch, query: m.queryID, state: s})
			mismatch := scenario == "parallel"
			if m.configMismatch != mismatch || strings.Contains(m.View().Content, "mismatch") != mismatch {
				t.Errorf("scenario=%q mismatch=%v view=%s", scenario, m.configMismatch, m.View().Content)
			}
		})
	}
}

func TestReconnectMetadataProjection(t *testing.T) {
	m := New(nil, Options{SessionID: "existing", Model: "client-model", Input: workflows.Input{Workdir: "/client-root"}})
	defer m.Close()
	m.resize(80, 24)
	var s workflows.State
	_ = json.Unmarshal([]byte(`{"session":{"model":"original-model","workdir":"/original-root","dataDir":"/original-data","scenario":"demo"}}`), &s)
	m.Update(snapshotMsg{epoch: m.epoch, query: m.queryID, state: s})
	for i := 0; i < 2; i++ {
		view := m.View().Content
		for _, want := range []string{"original-model", "/original-root", "mismatch"} {
			if !strings.Contains(view, want) {
				t.Errorf("missing %q: %s", want, view)
			}
		}
		m.Update(snapshotMsg{epoch: m.epoch, query: m.queryID, state: s})
	}
	if m.opts.Model != "client-model" || m.opts.Input.Workdir != "/client-root" {
		t.Fatal("client selection mutated")
	}
	if m.state.Session == nil || m.state.Session.DataDir != "/original-data" || m.state.Session.Scenario != "demo" {
		t.Fatal("projection lost session metadata")
	}
	m.state = displayState(workflows.State{})
	if !strings.Contains(m.View().Content, "unknown (legacy") {
		t.Error("legacy configuration not explicitly unknown")
	}
}
