package webintegration_test

// Test-only contract for the four text-only durability scenarios. This is NOT
// an event filter: every row must belong to a declared turn and one of exactly
// two domains. Preview rows cannot satisfy or hide canonical settlement.
import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"unicode/utf8"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/stretchr/testify/require"
)

type textOutcome struct {
	completed  bool
	maxAttempt int
}

const mockAnswer = "Mock assistant: ready."

func validateTextOutcome(conversation chat.ConversationID, turns map[chat.TurnID]textOutcome, events []chat.Event, messages []chat.MessageRecord) error {
	completions, answers, prompts := map[chat.TurnID]int{}, map[chat.TurnID]int{}, map[chat.TurnID]int{}
	ids := map[string]bool{}
	var sequence int64
	for _, e := range events {
		want, ok := turns[e.TurnID]
		if !ok || e.ConversationID != conversation || !chat.ValidID(e.ID) || ids[e.ID] || e.Sequence <= sequence || len(e.Payload) > 65536 || !utf8.Valid(e.Payload) {
			return fmt.Errorf("invalid event identity/scope/order/payload: %+v", e)
		}
		ids[e.ID], sequence = true, e.Sequence
		switch e.Type {
		case "message.delta":
			if e.LogicalStep != 0 || e.Attempt < 1 || e.Attempt > want.maxAttempt {
				return fmt.Errorf("unrecognized preview attempt: %+v", e)
			}
			var p struct {
				Text    *string `json:"text"`
				Bounded *bool   `json:"bounded"`
			}
			d := json.NewDecoder(bytes.NewReader(e.Payload))
			d.DisallowUnknownFields()
			if err := d.Decode(&p); err != nil {
				return fmt.Errorf("malformed preview: %w", err)
			}
			if d.Decode(new(any)) != io.EOF || p.Text == nil || p.Bounded == nil || !utf8.ValidString(*p.Text) || len(*p.Text) > 8000 {
				return fmt.Errorf("invalid preview schema/bounds: %s", e.Payload)
			}
			// These fixtures have a short deterministic response. Even a correctly
			// shaped preview must be a cumulative prefix of that response, never a
			// different attempt's answer or a falsely truncated fixture.
			if *p.Bounded || len(*p.Text) > len(mockAnswer) || mockAnswer[:len(*p.Text)] != *p.Text {
				return fmt.Errorf("unexpected fixture preview: %s", e.Payload)
			}
		case "turn.completed":
			var p struct {
				Status *string `json:"status"`
				Reason *string `json:"reason"`
			}
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return err
			}
			if !want.completed || e.ID != chat.LogicalID(e.TurnID, "settlement") || e.Attempt != 0 || e.LogicalStep != 0 || p.Status == nil || *p.Status != "completed" || p.Reason == nil || *p.Reason != "" {
				return fmt.Errorf("premature or incorrect canonical completion: %+v", e)
			}
			completions[e.TurnID]++
		default:
			return fmt.Errorf("unexpected event kind %q", e.Type)
		}
	}
	ids = map[string]bool{}
	for _, m := range messages {
		want, ok := turns[m.TurnID]
		if !ok || m.ConversationID != conversation || !chat.ValidID(m.EntryID) || ids[m.EntryID] {
			return fmt.Errorf("invalid message identity/scope: %+v", m)
		}
		ids[m.EntryID] = true
		switch m.Role {
		case "user":
			prompts[m.TurnID]++
		case "assistant":
			if !want.completed || m.EntryID != chat.LogicalID(m.TurnID, "final") || m.LogicalStep != 0 || m.Text != mockAnswer || m.ResultKind != "final" || m.CompletionState != "completed" {
				return fmt.Errorf("premature or incorrect final answer: %+v", m)
			}
			answers[m.TurnID]++
		default:
			return fmt.Errorf("unexpected message role %q", m.Role)
		}
	}
	for turn, want := range turns {
		n := 0
		if want.completed {
			n = 1
		}
		if completions[turn] != n || answers[turn] != n || prompts[turn] != 1 {
			return fmt.Errorf("turn %s: completion=%d answer=%d prompt=%d; want %d/%d/1", turn, completions[turn], answers[turn], prompts[turn], n, n)
		}
	}
	return nil
}

// Negative controls are ordinary Go tests (no database/build tag). They prove
// malformed previews and premature/duplicate canonical rows cannot be hidden
// by the compatibility correction, including an assistant-only false success.
func TestTextOutcomeRejectsInvalidDomains(t *testing.T) {
	c := chat.ConversationID("11111111-1111-4111-8111-111111111111")
	turn := chat.TurnID("22222222-2222-4222-8222-222222222222")
	preview := chat.Event{ID: "33333333-3333-4333-8333-333333333333", ConversationID: c, TurnID: turn, Attempt: 1, Type: "message.delta", Payload: json.RawMessage(`{"text":"Mock assistant: ready.","bounded":false}`), Sequence: 1}
	completed := chat.Event{ID: chat.LogicalID(turn, "settlement"), ConversationID: c, TurnID: turn, Type: "turn.completed", Payload: json.RawMessage(`{"status":"completed","reason":""}`), Sequence: 2}
	prompt := chat.MessageRecord{EntryID: "44444444-4444-4444-8444-444444444444", ConversationID: c, TurnID: turn, Role: "user"}
	answer := chat.MessageRecord{EntryID: chat.LogicalID(turn, "final"), ConversationID: c, TurnID: turn, Role: "assistant", Text: mockAnswer, ResultKind: "final", CompletionState: "completed"}
	for _, done := range []bool{false, true} {
		events := []chat.Event{preview}
		messages := []chat.MessageRecord{prompt}
		if done {
			events = append(events, completed)
			messages = append(messages, answer)
		}
		require.NoError(t, validateTextOutcome(c, map[chat.TurnID]textOutcome{turn: {done, 1}}, events, messages))
	}
	cases := []struct {
		name    string
		mutate  func(*[]chat.Event, *[]chat.MessageRecord)
		pending bool
	}{
		{"premature canonical event", func(e *[]chat.Event, m *[]chat.MessageRecord) { *m = (*m)[:1] }, true},
		{"premature assistant without event", func(e *[]chat.Event, m *[]chat.MessageRecord) { *e = (*e)[:1] }, true},
		{"duplicate canonical event", func(e *[]chat.Event, m *[]chat.MessageRecord) { v := completed; v.Sequence = 3; *e = append(*e, v) }, false},
		{"duplicate canonical event distinct ID", func(e *[]chat.Event, m *[]chat.MessageRecord) {
			v := completed
			v.Sequence = 3
			v.ID = "55555555-5555-4555-8555-555555555555"
			*e = append(*e, v)
		}, false},
		{"duplicate final assistant", func(e *[]chat.Event, m *[]chat.MessageRecord) { *m = append(*m, answer) }, false},
		{"missing canonical event", func(e *[]chat.Event, m *[]chat.MessageRecord) { *e = (*e)[:1] }, false},
		{"missing final assistant", func(e *[]chat.Event, m *[]chat.MessageRecord) { *m = (*m)[:1] }, false},
		{"unexpected event", func(e *[]chat.Event, m *[]chat.MessageRecord) { (*e)[0].Type = "turn.failed" }, false},
		{"cross conversation preview", func(e *[]chat.Event, m *[]chat.MessageRecord) { (*e)[0].ConversationID = "other" }, false},
		{"cross turn preview", func(e *[]chat.Event, m *[]chat.MessageRecord) { (*e)[0].TurnID = "other" }, false},
		{"missing attempt", func(e *[]chat.Event, m *[]chat.MessageRecord) { (*e)[0].Attempt = 0 }, false},
		{"unknown attempt", func(e *[]chat.Event, m *[]chat.MessageRecord) { (*e)[0].Attempt = 2 }, false},
		{"wrong step", func(e *[]chat.Event, m *[]chat.MessageRecord) { (*e)[0].LogicalStep = 1 }, false},
		{"malformed preview", func(e *[]chat.Event, m *[]chat.MessageRecord) {
			(*e)[0].Payload = json.RawMessage(`{"text":7,"bounded":false}`)
		}, false},
		{"missing preview bound", func(e *[]chat.Event, m *[]chat.MessageRecord) { (*e)[0].Payload = json.RawMessage(`{"text":"Mock"}`) }, false},
		{"extra preview field", func(e *[]chat.Event, m *[]chat.MessageRecord) {
			(*e)[0].Payload = json.RawMessage(`{"text":"Mock","bounded":false,"final":true}`)
		}, false},
		{"oversized preview", func(e *[]chat.Event, m *[]chat.MessageRecord) {
			(*e)[0].Payload, _ = json.Marshal(map[string]any{"text": string(bytes.Repeat([]byte("x"), 8001)), "bounded": false})
		}, false},
		{"incorrect canonical payload", func(e *[]chat.Event, m *[]chat.MessageRecord) {
			(*e)[1].Payload = json.RawMessage(`{"status":"failed","reason":""}`)
		}, false},
		{"attempt-scoped canonical", func(e *[]chat.Event, m *[]chat.MessageRecord) { (*e)[1].Attempt = 1 }, false},
		{"wrong final answer", func(e *[]chat.Event, m *[]chat.MessageRecord) { (*m)[1].Text = "wrong winner" }, false},
		{"wrong final identity", func(e *[]chat.Event, m *[]chat.MessageRecord) { (*m)[1].EntryID = prompt.EntryID }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events := []chat.Event{preview, completed}
			messages := []chat.MessageRecord{prompt, answer}
			tc.mutate(&events, &messages)
			require.Error(t, validateTextOutcome(c, map[chat.TurnID]textOutcome{turn: {!tc.pending, 1}}, events, messages))
		})
	}
}
