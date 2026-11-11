package chat

import (
	"errors"
	"testing"
)

func TestCommandCanonicalDigest(t *testing.T) {
	in := CommandInput{RequestID: "11111111-1111-4111-8111-111111111111", ConversationID: "22222222-2222-4222-8222-222222222222", Kind: CommandMessage, Payload: CommandPayload{Text: "hello"}}
	digest, err := in.Digest()
	if err != nil {
		t.Fatal(err)
	}
	in.Payload.AttachmentIDs = []AttachmentID{}
	same, err := in.Digest()
	if err != nil || same != digest {
		t.Fatalf("empty attachments must normalize: %s %v", same, err)
	}
	in.Payload.Text = "changed"
	changed, err := in.Digest()
	if err != nil || changed == digest {
		t.Fatalf("text missing from digest: %s %v", changed, err)
	}
	in.Kind = CommandStop
	if _, err := in.Digest(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid union payload accepted: %v", err)
	}
	in.Kind = CommandMessage
	in.RequestID = "not-a-uuid"
	if _, err := in.Digest(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad identity accepted: %v", err)
	}
}
func TestCommandPayloadValidation(t *testing.T) {
	base := CommandInput{RequestID: "11111111-1111-4111-8111-111111111111", ConversationID: "22222222-2222-4222-8222-222222222222"}
	cases := []struct {
		kind  CommandKind
		p     CommandPayload
		valid bool
	}{
		{CommandMessage, CommandPayload{}, false},
		{CommandMessage, CommandPayload{Text: "ok"}, true},
		{CommandReplace, CommandPayload{Text: "ok"}, false},
		{CommandStop, CommandPayload{ExpectedActiveTurnID: "33333333-3333-4333-8333-333333333333"}, true},
		{CommandRevoke, CommandPayload{}, true},
		{CommandDecide, CommandPayload{ApprovalID: "33333333-3333-4333-8333-333333333333", ActionHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Decision: ApprovalOnce}, true},
		{CommandDecide, CommandPayload{ApprovalID: "33333333-3333-4333-8333-333333333333", Decision: "yes"}, false},
	}
	for _, tc := range cases {
		base.Kind = tc.kind
		base.Payload = tc.p
		_, err := base.Digest()
		if (err == nil) != tc.valid {
			t.Errorf("%+v: %v", tc, err)
		}
	}
}
