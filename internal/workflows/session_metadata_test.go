package workflows_test

import (
	"encoding/json"
	w "github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"go.temporal.io/sdk/testsuite"
	"testing"
	"time"
)

func TestSessionInfoSurvivesRestore(t *testing.T) {
	for _, carryJSON := range []string{`{}`, `{"session":{"model":"wrong","workdir":"/wrong"},"answer":"retained"}`} {
		var carry w.State
		if err := json.Unmarshal([]byte(carryJSON), &carry); err != nil {
			t.Fatal(err)
		}
		var suite testsuite.WorkflowTestSuite
		env := suite.NewTestWorkflowEnvironment()
		env.RegisterDelayedCallback(func() {
			state := query(t, env)
			data, _ := json.Marshal(state)
			var got struct {
				Session map[string]string `json:"session"`
			}
			_ = json.Unmarshal(data, &got)
			for k, v := range map[string]string{"model": "original-model", "workdir": "/original-root", "dataDir": "/original-data", "scenario": "demo"} {
				if got.Session[k] != v {
					t.Errorf("session %s=%q want %q", k, got.Session[k], v)
				}
			}
			env.CancelWorkflow()
		}, time.Second)
		env.ExecuteWorkflow(w.SessionWorkflow, w.Input{Model: "original-model", Workdir: "/original-root", DataDir: "/original-data", Scenario: "demo", Carry: &carry})
	}
}
