//go:build integration

package storage_test

import (
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestChatAdmissionCheckpointIdentityAndClosedReceipt(t *testing.T) {
	for _, proof := range []string{"none", "enqueue", "running"} {
		t.Run(proof, func(t *testing.T) {
			r, p := database(t)
			c := conversation(t, r)
			in := input(c.ID)
			rc, err := r.AcceptCommand(ctx, in)
			require.NoError(t, err)
			var seq int64
			require.NoError(t, p.QueryRow(ctx, "SELECT sequence FROM commands WHERE id=$1", rc.CommandID).Scan(&seq))
			cmd := chat.SessionCommand{CommandID: rc.CommandID, RequestID: in.RequestID, ConversationID: c.ID, TurnID: rc.TurnID, WorkflowID: c.WorkflowID, Model: c.Model, PayloadDigest: rc.PayloadDigest, Kind: in.Kind, Payload: in.Payload, Sequence: seq}
			cp := chat.AdmissionCheckpoint{Command: cmd.Identity(), Phase: "check", Eligible: true}
			got, err := r.ResumeChatAdmission(ctx, cp)
			require.NoError(t, err)
			require.Empty(t, got.State)
			cp.Command.Sequence++
			_, err = r.ResumeChatAdmission(ctx, cp)
			require.ErrorIs(t, err, storage.ErrConflict)
			cp.Command = cmd.Identity()
			if proof == "running" {
				require.NoError(t, r.StartChatTurn(ctx, chat.TurnRef{ConversationID: c.ID, TurnID: rc.TurnID}))
			}
			got, err = r.ResolveClosedChatCommand(ctx, cmd, proof == "enqueue")
			require.NoError(t, err)
			want := chat.AdmissionResult{State: "accepted"}
			if proof == "none" {
				want = chat.AdmissionResult{State: "rejected", Code: "conversation_closed"}
			}
			require.Equal(t, want, got)
			cp.Phase = "receipt"
			cp.Result = chat.AdmissionResult{State: "accepted"}
			_, err = r.ResumeChatAdmission(ctx, cp)
			if proof == "none" {
				require.ErrorIs(t, err, storage.ErrConflict)
			} else {
				require.NoError(t, err)
			}
			saved, err := r.CheckChatCommand(ctx, cmd)
			require.NoError(t, err)
			require.Equal(t, want, saved)
		})
	}
}
