package workflows

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
)

func TestQueryPreservesApprovalControlBindings(t *testing.T) {
	for _, approved := range []bool{false, true} {
		t.Run(fmt.Sprint(approved), func(t *testing.T) {
			// Exercise both bindings beyond the old display cap, even though actual
			// producer hashes are only 64 bytes.
			id := strings.Repeat("workflow", 100) + ":approval:123456789"
			hash := strings.Repeat("hash", 100)
			r := runtime{state: State{PendingApproval: &PendingApproval{
				RequestID: id, ActionHash: hash,
				Decision: &DecisionInput{RequestID: id, ActionHash: hash, Approved: approved, User: strings.Repeat("u", 500), Reason: strings.Repeat("r", 10000)},
			}}}
			before, _ := json.Marshal(r.state)
			q := r.queryState()
			assertQueryApproval(t, q, id, hash, approved)
			if len(q.PendingApproval.Decision.User) != 256 || len(q.PendingApproval.Decision.Reason) != 8192 {
				t.Fatal("decision display fields were not bounded")
			}
			after, _ := json.Marshal(r.state)
			if string(before) != string(after) {
				t.Fatal("query mutated source state")
			}
		})
	}
}

func TestQueryBudgetFallbackPreservesRecordedDecision(t *testing.T) {
	for _, approved := range []bool{false, true} {
		t.Run(fmt.Sprint(approved), func(t *testing.T) {
			escaped := func(n int) string { return strings.Repeat("\x01", n) }
			id := strings.Repeat("w", 1000) + ":approval:123456789"
			hash := strings.Repeat("a", 64)
			args := json.RawMessage(`"` + strings.Repeat("x", 63000) + `"`)
			r := runtime{state: State{
				Answer: escaped(maxAnswerBytes), TranscriptRef: escaped(2048), OriginInputID: escaped(512),
				LastRunFailure: &Failure{Reason: escaped(64), Detail: escaped(maxAnswerBytes)},
				ActiveTools:    []llm.ToolCall{{ID: "active", Name: "tool", Args: args}},
				PendingApproval: &PendingApproval{RequestID: id, ActionHash: hash, Preview: escaped(8192),
					Call:     llm.ToolCall{ID: "pending", Name: "tool", Args: args},
					Decision: &DecisionInput{RequestID: id, ActionHash: hash, Approved: approved, User: escaped(256), Reason: escaped(8192)},
				},
			}}
			for i := 0; i < 16; i++ {
				msg := MessageInput{Text: strings.Repeat("x", 65400), RequestID: fmt.Sprint(i), WhenBusy: "queue"}
				if err := r.inboxFits(msg); err != nil {
					t.Fatal(err)
				}
				r.state.Inbox = append(r.state.Inbox, msg)
			}
			for i := 0; i < 2*maxRetainedChildren; i++ {
				r.state.Children = append(r.state.Children, ChildState{WorkflowID: escaped(256), SummaryRef: escaped(512), Status: escaped(32)})
			}
			before, err := json.Marshal(r.state)
			if err != nil || len(before) <= maxQueryJSONBytes {
				t.Fatalf("fixture must exceed budget: %d, %v", len(before), err)
			}
			q := r.queryState()
			if q.Children != nil || q.ActiveTools != nil || q.PendingApproval.Call.Args != nil || q.PendingApproval.Preview != "" {
				t.Fatal("budget fallback did not discard optional projections")
			}
			b, err := json.Marshal(q)
			if err != nil || len(b) > maxQueryJSONBytes {
				t.Fatalf("query bytes=%d error=%v", len(b), err)
			}
			// Check the serialized public contract too, including Approved=false.
			var wire State
			if err := json.Unmarshal(b, &wire); err != nil {
				t.Fatal(err)
			}
			assertQueryApproval(t, wire, id, hash, approved)
			inboxBefore, _ := json.Marshal(r.state.Inbox)
			inboxAfter, _ := json.Marshal(wire.Inbox)
			if string(inboxBefore) != string(inboxAfter) {
				t.Fatal("query lost admitted inbox")
			}
			after, _ := json.Marshal(r.state)
			if string(before) != string(after) {
				t.Fatal("query mutated source state")
			}
		})
	}
}

func assertQueryApproval(t *testing.T, q State, id, hash string, approved bool) {
	t.Helper()
	p := q.PendingApproval
	if p == nil || p.RequestID != id || p.ActionHash != hash {
		t.Fatal("query changed pending approval binding")
	}
	d := p.Decision
	if d == nil || d.RequestID != id || d.ActionHash != hash || d.Approved != approved {
		t.Fatal("query lost or changed recorded decision")
	}
}
