package events

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestEveryEventRoundTrips(t *testing.T) {
	tests := []struct {
		typ     Type
		name    string
		pattern Pattern
		payload any
		wire    string
	}{
		{AgentStart, "agent_start", None, AgentData{InputID: "input-1"}, `{"inputId":"input-1"}`},
		{TurnStart, "turn_start", None, TurnData{Turn: 0}, `{"turn":0}`},
		{TurnEnd, "turn_end", None, TurnData{Turn: 2, ToolResults: []string{"result-1", "result-2"}}, `{"turn":2,"toolResults":["result-1","result-2"]}`},
		{MessageStart, "message_start", None, MessageData{Role: "assistant", MessageRef: "message-1"}, `{"role":"assistant","messageRef":"message-1"}`},
		{MessageEnd, "message_end", None, MessageData{MessageRef: "message-1", Tokens: 12}, `{"messageRef":"message-1","tokens":12}`},
		{MessageUpdate, "message_update", None, MessageData{Delta: "hello"}, `{"delta":"hello"}`},
		{ToolStart, "tool_execution_start", None, ToolData{ToolCallID: "call-1", Name: "read_file", Args: json.RawMessage(`{"path":"a.go"}`), ExecutionMode: "parallel", Details: map[string]string{"path": "a.go"}}, `{"toolCallId":"call-1","name":"read_file","args":{"path":"a.go"},"executionMode":"parallel","details":{"path":"a.go"}}`},
		{ToolUpdate, "tool_execution_update", LongRunning, ToolData{ToolCallID: "call-1", Chunk: "output", Offset: 7}, `{"toolCallId":"call-1","chunk":"output","offset":7}`},
		{ToolEnd, "tool_execution_end", None, ToolData{ToolCallID: "call-1", IsError: true, Interrupted: true, Summary: "cancelled"}, `{"toolCallId":"call-1","isError":true,"interrupted":true,"summary":"cancelled"}`},
		{ApprovalRequested, "approval_requested", Approval, ApprovalData{RequestID: "request-1", ActionHash: "hash-1", Command: "go test ./..."}, `{"requestId":"request-1","actionHash":"hash-1","command":"go test ./..."}`},
		{ApprovalResolved, "approval_resolved", Approval, ApprovalData{RequestID: "request-1", ActionHash: "hash-1", Decision: "deny", User: "alice", Reason: "unsafe"}, `{"requestId":"request-1","actionHash":"hash-1","decision":"deny","user":"alice","reason":"unsafe"}`},
		{ChildStarted, "child_started", Child, ChildData{ChildWorkflowID: "child-1", Task: "inspect"}, `{"childWorkflowId":"child-1","task":"inspect"}`},
		{ChildCompleted, "child_completed", Child, ChildData{ChildWorkflowID: "child-1", SummaryRef: "summary-1"}, `{"childWorkflowId":"child-1","summaryRef":"summary-1"}`},
		{ParallelStart, "parallel_batch_start", Parallel, ParallelData{ToolCallIDs: []string{"call-1", "call-2"}, Concurrency: 2}, `{"toolCallIds":["call-1","call-2"],"concurrency":2}`},
		{ParallelEnd, "parallel_batch_end", Parallel, ParallelData{ToolCallIDs: []string{"call-1", "call-2"}, Concurrency: 0}, `{"toolCallIds":["call-1","call-2"],"concurrency":0}`},
		{Heartbeat, "heartbeat", LongRunning, HeartbeatData{Activity: "command", Progress: 0}, `{"activity":"command","progress":0}`},
		{AgentEnd, "agent_end", None, AgentData{Status: "completed"}, `{"status":"completed"}`},
		{AgentSettled, "agent_settled", None, AgentData{Reason: "idle"}, `{"reason":"idle"}`},
		{RunFailed, "run_failed", None, AgentData{Status: "failed", RunFailure: "model unavailable"}, `{"status":"failed","runFailure":"model unavailable"}`},
	}
	inventory := make(map[Type]bool)
	for _, typ := range Types {
		if inventory[typ] {
			t.Errorf("duplicate inventory event %q", typ)
		}
		inventory[typ] = true
	}
	covered := make(map[Type]bool)
	for _, tt := range tests {
		if covered[tt.typ] {
			t.Errorf("duplicate table event %q", tt.typ)
		}
		covered[tt.typ] = true
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.typ) != tt.name {
				t.Fatalf("event name = %q, want %q", tt.typ, tt.name)
			}
			data := assertPayloadRoundTrip(t, tt.payload, tt.wire)
			want := Event{Seq: 42, TS: "2026-11-09T00:00:00Z", WorkflowID: "session-test", RunID: "run", Type: tt.typ, Pattern: tt.pattern, Data: data}
			if tt.pattern == Child {
				want.ParentWorkflowID = "parent-session"
			}
			b, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			var got Event
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("got %+v want %+v", got, want)
			}
		})
	}
	if len(tests) != 19 || !reflect.DeepEqual(inventory, covered) {
		t.Fatalf("event inventory %v does not match 19-case coverage %v", inventory, covered)
	}
}

func TestPayloadOptionalAndZeroFields(t *testing.T) {
	tests := []struct {
		name    string
		payload any
		wire    string
	}{
		{"agent", AgentData{}, `{}`},
		{"turn", TurnData{}, `{"turn":0}`},
		{"message", MessageData{}, `{}`},
		{"tool", ToolData{ToolCallID: "call-1", Offset: 0, IsError: false, Interrupted: false}, `{"toolCallId":"call-1"}`},
		{"approval", ApprovalData{}, `{"requestId":"","actionHash":""}`},
		{"child", ChildData{}, `{"childWorkflowId":""}`},
		{"parallel-nil", ParallelData{}, `{"toolCallIds":null,"concurrency":0}`},
		{"parallel-empty", ParallelData{ToolCallIDs: []string{}}, `{"toolCallIds":[],"concurrency":0}`},
		{"heartbeat", HeartbeatData{}, `{"activity":"","progress":0}`},
		{"heartbeat-progress", HeartbeatData{Activity: "command", Progress: 42}, `{"activity":"command","progress":42}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertPayloadRoundTrip(t, tt.payload, tt.wire)
		})
	}
}

// Compare JSON structurally so field names, values, and omissions are contractual,
// but object key ordering is not. Decode both emitted and fixture JSON into the
// concrete payload type to check both directions of the wire contract.
func assertPayloadRoundTrip(t *testing.T, payload any, wire string) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var gotJSON, wantJSON any
	if err := json.Unmarshal(data, &gotJSON); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(wire), &wantJSON); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotJSON, wantJSON) {
		t.Fatalf("payload JSON = %s, want %s", data, wire)
	}
	for _, encoded := range [][]byte{data, []byte(wire)} {
		decoded := reflect.New(reflect.TypeOf(payload))
		if err := json.Unmarshal(encoded, decoded.Interface()); err != nil {
			t.Fatal(err)
		}
		if got := decoded.Elem().Interface(); !reflect.DeepEqual(got, payload) {
			t.Fatalf("decoded payload = %#v, want %#v", got, payload)
		}
	}
	return data
}
