package chat

import (
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestEnabledModelAdmission(t *testing.T) {
	require.True(t, ValidModel("qwen3.5:9b"))
	for _, model := range []string{"gemma4:e4b", "qwen3.5:latest", ""} {
		require.False(t, ValidModel(model), model)
	}
}
func TestSingleAttachmentCommandBoundary(t *testing.T) {
	for _, kind := range []CommandKind{CommandMessage, CommandReplace} {
		in := CommandInput{RequestID: uuid.NewString(), ConversationID: ConversationID(uuid.NewString()), Kind: kind, Payload: CommandPayload{Text: "hello"}}
		if kind == CommandReplace {
			in.Payload.ExpectedActiveTurnID = TurnID(uuid.NewString())
		}
		_, err := in.Digest()
		require.NoError(t, err)
		in.Payload.AttachmentIDs = []AttachmentID{AttachmentID(uuid.NewString())}
		_, err = in.Digest()
		require.NoError(t, err)
		in.Payload.AttachmentIDs = append(in.Payload.AttachmentIDs, AttachmentID(uuid.NewString()))
		_, err = in.Digest()
		require.ErrorIs(t, err, ErrInvalid, string(kind))
	}
}
