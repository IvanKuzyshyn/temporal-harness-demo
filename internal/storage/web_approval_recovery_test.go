//go:build integration

package storage_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestWebApprovalCancellationEstablishesImmutableTombstone(t *testing.T) {
	r, db := database(t)
	ref, a := webAction(t, r)
	p := chat.WebApproval{ID: uuid.NewString(), Scope: a.Scope, Deadline: time.Now().UTC().Truncate(time.Microsecond).Add(24 * time.Hour)}
	require.NoError(t, r.MarkChatCancelling(ctx, ref))
	cancelled := chat.WebApprovalRecord{Approval: p, Outcome: "cancelled"}
	require.NoError(t, r.RecordWebApproval(ctx, cancelled), "cancellation must establish an uncommitted request, not ignore an absent row")
	require.NoError(t, r.RecordWebApproval(ctx, cancelled))
	var original string
	require.NoError(t, db.QueryRow(ctx, `SELECT to_jsonb(a)::text FROM approvals a WHERE id=$1`, p.ID).Scan(&original))
	require.NoError(t, r.RecordWebApproval(ctx, chat.WebApprovalRecord{Approval: p}), "late same-scope creation cannot resurrect the tombstone")
	var after string
	require.NoError(t, db.QueryRow(ctx, `SELECT to_jsonb(a)::text FROM approvals a WHERE id=$1`, p.ID).Scan(&after))
	require.Equal(t, original, after)
	for _, outcome := range []string{"authorized", "denied", "expired", "revoked"} {
		require.ErrorIs(t, r.RecordWebApproval(ctx, chat.WebApprovalRecord{Approval: p, Outcome: outcome}), storage.ErrConflict)
	}
	var raw []byte
	require.NoError(t, db.QueryRow(ctx, `SELECT scope FROM approvals WHERE id=$1 AND outcome='cancelled'`, p.ID).Scan(&raw))
	var scope chat.ApprovalScope
	require.NoError(t, json.Unmarshal(raw, &scope))
	require.Equal(t, p.Scope, scope)
}
func TestWebApprovalRecoveryRejectsChangedIdentity(t *testing.T) {
	r, db := database(t)
	ref, a := webAction(t, r)
	p := chat.WebApproval{ID: uuid.NewString(), Scope: a.Scope, Deadline: time.Now().UTC().Truncate(time.Microsecond).Add(24 * time.Hour)}
	require.NoError(t, r.RecordWebApproval(ctx, chat.WebApprovalRecord{Approval: p}))
	require.NoError(t, r.MarkChatCancelling(ctx, ref))
	variants := map[string]func(*chat.WebApproval){
		"deadline": func(p *chat.WebApproval) { p.Deadline = p.Deadline.Add(time.Hour) },
		"revision": func(p *chat.WebApproval) { p.Scope.PolicyRevision++ },
		"hash":     func(p *chat.WebApproval) { p.Scope.ActionHash = strings.Repeat("c", 64) },
		"content":  func(p *chat.WebApproval) { p.Scope.ContentRef = strings.Repeat("d", 64) },
		"call":     func(p *chat.WebApproval) { p.Scope.ToolCallID = uuid.NewString() },
		"turn":     func(p *chat.WebApproval) { p.Scope.TurnID = chat.TurnID(uuid.NewString()) },
	}
	var original string
	require.NoError(t, db.QueryRow(ctx, `SELECT to_jsonb(a)::text FROM approvals a WHERE id=$1`, p.ID).Scan(&original))
	for name, mutate := range variants {
		t.Run(name, func(t *testing.T) {
			changed := p
			mutate(&changed)
			for _, outcome := range []string{"", "cancelled"} {
				require.ErrorIs(t, r.RecordWebApproval(ctx, chat.WebApprovalRecord{Approval: changed, Outcome: outcome}), storage.ErrConflict)
			}
		})
	}
	var after string
	require.NoError(t, db.QueryRow(ctx, `SELECT to_jsonb(a)::text FROM approvals a WHERE id=$1`, p.ID).Scan(&after))
	require.Equal(t, original, after)
}
func TestWebApprovalLateCreationCannotRewriteDecisionOrReopenTurn(t *testing.T) {
	r, db := database(t)
	ref, a := webAction(t, r)
	p := chat.WebApproval{ID: uuid.NewString(), Scope: a.Scope, Deadline: time.Now().UTC().Truncate(time.Microsecond).Add(24 * time.Hour)}
	require.NoError(t, r.RecordWebApproval(ctx, chat.WebApprovalRecord{Approval: p}))
	require.NoError(t, r.RecordApprovalDecision(ctx, ref.ConversationID, p.ID, chat.ApprovalOnce, 0, time.Now()))
	require.NoError(t, r.RecordWebApproval(ctx, chat.WebApprovalRecord{Approval: p, Outcome: "authorized"}))
	a.Outcome = "authorized"
	require.NoError(t, r.RecordWebAuthorization(ctx, a))
	var original string
	require.NoError(t, db.QueryRow(ctx, `SELECT to_jsonb(a)::text FROM approvals a WHERE id=$1`, p.ID).Scan(&original))
	p.Decision = chat.ApprovalDeny // Creation is not the decision-audit writer.
	require.NoError(t, r.RecordWebApproval(ctx, chat.WebApprovalRecord{Approval: p}))
	var after string
	require.NoError(t, db.QueryRow(ctx, `SELECT to_jsonb(a)::text FROM approvals a WHERE id=$1`, p.ID).Scan(&after))
	require.Equal(t, original, after)
	turn, err := r.GetTurn(ctx, ref)
	require.NoError(t, err)
	require.Equal(t, "running", turn.Status, "stale creation cannot reopen a resolved approval")
}
