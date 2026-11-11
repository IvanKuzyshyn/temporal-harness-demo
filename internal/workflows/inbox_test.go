package workflows

import "testing"

func TestMessageWireModesAndReceipts(t *testing.T) {
	for _, tc := range []struct{ status, mode, want string }{
		{"idle", "", "started"}, {"working", "followUp", "queued"},
		{"working", "", "queued"}, {"working", "steer", "steered"},
		{"idle", "steer", "started"},
	} {
		t.Run(tc.status+"/"+tc.mode, func(t *testing.T) {
			r := &runtime{state: State{Status: tc.status}}
			msg := MessageInput{Text: "hello", WhenBusy: tc.mode}
			if err := r.validateMessage(msg); err != nil {
				t.Fatal(err)
			}
			receipt, err := r.message(nil, msg)
			if err != nil || receipt.Disposition != tc.want || len(r.state.Inbox) != 1 {
				t.Fatalf("receipt=%+v err=%v state=%+v", receipt, err, r.state)
			}
		})
	}
	r := &runtime{state: State{Status: "idle"}}
	if err := r.validateMessage(MessageInput{Text: "hello", WhenBusy: "followup"}); err == nil {
		t.Fatal("accepted wrong spelling")
	}
	_, _ = r.message(nil, MessageInput{Text: "first"})
	receipt, _ := r.message(nil, MessageInput{Text: "second", WhenBusy: "followUp"})
	if receipt.Disposition != "queued" {
		t.Fatalf("second pending input: %+v", receipt)
	}
}
