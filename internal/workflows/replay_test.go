package workflows

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type recordedFixture struct {
	File, WorkflowID, RunID, WorkflowType string
	Events                                int
	SHA256, RawSHA256                     string
}
type recordedManifest struct {
	Command, CapturedAt, SourceCommit, GoVersion, SDKVersion, TemporalVersion, Redaction string
	FirstReceipt, DuplicateReceipt                                                       MessageReceipt
	BeforeDuplicate, AfterDuplicate, FinalState                                          State
	Approvals                                                                            []PendingApproval
	Histories                                                                            []recordedFixture
}

func loadRecordedHistories(t *testing.T) (recordedManifest, map[string]*historypb.History) {
	t.Helper()
	var m recordedManifest
	b, err := os.ReadFile("testdata/replay/manifest.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, &m))
	require.Len(t, m.Histories, 4, "do not silently skip missing recorded coverage")
	require.Equal(t, "v1.50.0", m.SDKVersion)
	for _, v := range []string{m.Command, m.CapturedAt, m.SourceCommit, m.GoVersion, m.TemporalVersion, m.Redaction} {
		require.NotEmpty(t, v)
	}
	histories := map[string]*historypb.History{}
	for _, f := range m.Histories {
		require.Equal(t, filepath.Base(f.File), f.File)
		require.NotEmpty(t, f.WorkflowID)
		require.NotEmpty(t, f.RunID)
		require.Len(t, f.RawSHA256, 64)
		b, err = os.ReadFile(filepath.Join("testdata/replay", f.File))
		require.NoError(t, err)
		require.Equal(t, f.SHA256, fmtHash(b), f.File)
		h := &historypb.History{}
		require.NoError(t, protojson.Unmarshal(b, h))
		require.GreaterOrEqual(t, len(h.Events), 4)
		require.Len(t, h.Events, f.Events)
		for i, e := range h.Events {
			require.EqualValues(t, i+1, e.EventId, "full, contiguous history")
		}
		start := h.Events[0].GetWorkflowExecutionStartedEventAttributes()
		require.NotNil(t, start)
		require.Equal(t, f.WorkflowType, start.WorkflowType.Name)
		require.Equal(t, f.RunID, start.OriginalExecutionRunId)
		require.NotContains(t, histories, f.File)
		histories[f.File] = h
	}
	for _, name := range []string{"hello-can.json", "demo-can.json", "successor.json", "explorer.json"} {
		require.Contains(t, histories, name)
	}
	return m, histories
}
func fmtHash(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }
func replayRecorded(h *historypb.History, f recordedFixture) error {
	r := worker.NewWorkflowReplayer()
	r.RegisterWorkflow(SessionWorkflow)
	r.RegisterWorkflow(ExplorerWorkflow)
	// The SDK otherwise substitutes ReplayId. Preserve the captured parent ID
	// because delegate constructs child IDs from workflow.GetInfo(ctx).
	return r.ReplayWorkflowHistoryWithOptions(nil, h, worker.ReplayWorkflowHistoryOptions{OriginalExecution: workflow.Execution{ID: f.WorkflowID, RunID: f.RunID}})
}

// No server, client, activity registration or model provider is used here.
func TestRecordedReplay(t *testing.T) {
	m, histories := loadRecordedHistories(t)
	for _, f := range m.Histories {
		t.Run(f.File, func(t *testing.T) { require.NoError(t, replayRecorded(histories[f.File], f)) })
	}
}

func TestRecordedReplayRejectsChangedActivity(t *testing.T) {
	m, histories := loadRecordedHistories(t)
	for _, f := range m.Histories {
		if f.File != "hello-can.json" {
			continue
		}
		h := proto.Clone(histories[f.File]).(*historypb.History)
		changed := false
		for _, e := range h.Events {
			if a := e.GetActivityTaskScheduledEventAttributes(); a != nil {
				a.ActivityType.Name = "NegativeControlNotEmitEvent"
				changed = true
				break
			}
		}
		require.True(t, changed)
		err := replayRecorded(h, f)
		require.Error(t, err, "a changed scheduled activity must fail deterministic replay")
		require.Contains(t, err.Error(), "NegativeControlNotEmitEvent")
		return
	}
	t.Fatal("negative control fixture missing")
}

func TestRecordedReplayCoverage(t *testing.T) {
	m, h := loadRecordedHistories(t)
	dc := converter.GetDefaultDataConverter()
	first, demo, next, child := h["hello-can.json"], h["demo-can.json"], h["successor.json"], h["explorer.json"]
	for _, pair := range [][2]*historypb.History{{first, demo}, {demo, next}} {
		a := pair[0].Events[len(pair[0].Events)-1].GetWorkflowExecutionContinuedAsNewEventAttributes()
		require.NotNil(t, a)
		b := pair[1].Events[0].GetWorkflowExecutionStartedEventAttributes()
		require.Equal(t, b.OriginalExecutionRunId, a.NewExecutionRunId)
		require.Equal(t, pair[0].Events[0].GetWorkflowExecutionStartedEventAttributes().OriginalExecutionRunId, b.ContinuedExecutionRunId)
		require.True(t, proto.Equal(a.Input, b.Input), "CAN input must be actual successor input")
	}
	var carried, final Input
	require.NoError(t, dc.FromPayloads(demo.Events[0].GetWorkflowExecutionStartedEventAttributes().Input, &carried))
	require.NoError(t, dc.FromPayloads(next.Events[0].GetWorkflowExecutionStartedEventAttributes().Input, &final))
	require.NotNil(t, carried.Carry)
	require.NotNil(t, final.Carry)
	require.Equal(t, 1, carried.ContinueAfterTurns)
	require.Equal(t, 1, carried.Carry.RunCounter)
	require.Equal(t, 1, carried.Carry.TotalTurns)
	require.Len(t, carried.Carry.Receipts, 1)
	require.Equal(t, "recorded-hello", carried.Carry.Receipts[0].RequestID)
	require.Equal(t, m.FirstReceipt, carried.Carry.Receipts[0].Receipt)
	require.Equal(t, m.FirstReceipt, m.DuplicateReceipt)
	require.Equal(t, m.BeforeDuplicate, m.AfterDuplicate)
	require.Equal(t, 1, m.AfterDuplicate.RunCounter)
	require.Empty(t, m.AfterDuplicate.Inbox)
	require.Len(t, final.Carry.Receipts, 2)
	require.Equal(t, carried.Carry.Receipts[0], final.Carry.Receipts[0])
	require.Equal(t, 2, final.Carry.RunCounter)
	require.Equal(t, 6, final.Carry.TotalTurns)
	require.Equal(t, 3, final.Carry.ApprovalCounter)
	require.Equal(t, 1, final.Carry.ChildCounter)
	require.False(t, final.Carry.VerificationPending)
	require.Nil(t, final.Carry.LastRunFailure)
	require.Contains(t, final.Carry.Answer, "Verification: go test ./... passed (exitCode=0;")
	// Public query intentionally omits the private retained-receipt ledger.
	observed := *final.Carry
	observed.Receipts = nil
	require.Equal(t, m.FinalState, observed)

	updates, completed := map[string]int{}, 0
	approvals := map[string]DecisionInput{}
	commands := map[int64]ToolInput{}
	commandResults := 0
	childStarts, childCompletions, childSchedules := 0, 0, 0
	for _, history := range []*historypb.History{first, demo} {
		for _, e := range history.Events {
			if a := e.GetWorkflowExecutionUpdateAcceptedEventAttributes(); a != nil {
				name := a.AcceptedRequest.Input.Name
				updates[name]++
				if name == DecideUpdate {
					var d DecisionInput
					require.NoError(t, dc.FromPayloads(a.AcceptedRequest.Input.Args, &d))
					require.True(t, d.Approved)
					approvals[d.RequestID] = d
				}
			}
			if a := e.GetWorkflowExecutionUpdateCompletedEventAttributes(); a != nil {
				completed++
				require.NotNil(t, a.Outcome.GetSuccess())
			}
			if a := e.GetStartChildWorkflowExecutionInitiatedEventAttributes(); a != nil {
				childSchedules++
				require.Equal(t, ExplorerName, a.WorkflowType.Name)
				var in ExplorerInput
				require.NoError(t, dc.FromPayloads(a.Input, &in))
				require.Nil(t, in.Config.Carry)
				require.Equal(t, demo.Events[0].GetWorkflowExecutionStartedEventAttributes().OriginalExecutionRunId, in.ParentScope.RunID)
				require.Equal(t, in.ParentScope.WorkflowID+"/explore-1", a.WorkflowId)
				require.True(t, proto.Equal(a.Input, child.Events[0].GetWorkflowExecutionStartedEventAttributes().Input))
			}
			if a := e.GetChildWorkflowExecutionStartedEventAttributes(); a != nil {
				childStarts++
				start := child.Events[0].GetWorkflowExecutionStartedEventAttributes()
				require.Equal(t, start.OriginalExecutionRunId, a.WorkflowExecution.RunId)
				require.Equal(t, final.Carry.Children[0].WorkflowID, a.WorkflowExecution.WorkflowId)
				require.Equal(t, demo.Events[0].GetWorkflowExecutionStartedEventAttributes().OriginalExecutionRunId, start.ParentWorkflowExecution.RunId)
				require.Equal(t, strings.TrimSuffix(a.WorkflowExecution.WorkflowId, "/explore-1"), start.ParentWorkflowExecution.WorkflowId)
			}
			if a := e.GetChildWorkflowExecutionCompletedEventAttributes(); a != nil {
				childCompletions++
				var result ExplorerResult
				require.NoError(t, dc.FromPayloads(a.Result, &result))
				require.NotEmpty(t, result.SummaryRef)
			}
			if a := e.GetActivityTaskScheduledEventAttributes(); a != nil && a.ActivityType.Name == CommandActivity {
				var in ToolInput
				require.NoError(t, dc.FromPayloads(a.Input, &in))
				commands[e.EventId] = in
				require.Equal(t, "run_command", in.Call.Name)
			}
			if a := e.GetActivityTaskCompletedEventAttributes(); a != nil {
				if _, ok := commands[a.ScheduledEventId]; ok {
					var result CommandResult
					require.NoError(t, dc.FromPayloads(a.Result, &result))
					require.Zero(t, result.ExitCode)
					require.False(t, result.Interrupted)
					require.NotEmpty(t, result.LogRef)
					commandResults++
				}
			}
		}
	}
	require.Equal(t, 3, updates[MessageUpdate], "hello, retained duplicate on new run, and demo")
	require.Equal(t, 3, updates[DecideUpdate])
	require.Equal(t, 6, completed)
	require.Equal(t, 1, childSchedules)
	require.Equal(t, 1, childStarts)
	require.Equal(t, 1, childCompletions)
	require.Equal(t, enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_COMPLETED, child.Events[len(child.Events)-1].EventType)
	require.Len(t, commands, 2)
	require.Equal(t, 2, commandResults)
	require.Len(t, m.Approvals, 3)
	require.Len(t, approvals, 3)
	for _, gate := range m.Approvals {
		decision, ok := approvals[gate.RequestID]
		require.True(t, ok)
		require.Equal(t, gate.ActionHash, decision.ActionHash)
	}
	require.Equal(t, "write_file", m.Approvals[0].Call.Name)
	require.NotEqual(t, m.Approvals[1].RequestID, m.Approvals[2].RequestID)
	require.True(t, strings.HasSuffix(m.Approvals[2].Call.ID, "/afterrun/tests"))
	matched := 0
	for _, in := range commands {
		for _, gate := range m.Approvals[1:] {
			if in.Call.ID == gate.Call.ID {
				matched++
			}
		}
	}
	require.Equal(t, 2, matched, "both commands have their own observed approval")
}
