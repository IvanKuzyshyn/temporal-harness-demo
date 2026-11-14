package storage

import (
	"context"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/jackc/pgx/v5"
)

// CompleteToolCall records the workflow's settled result. This cannot authorize
// or execute the action; it only advances a pending/running projection once.
// The original action digest stays unchanged, so late creation retries are safe.
func (r *Repository) CompleteToolCall(ctx context.Context, c chat.ConversationID, id, status string, result, sources []byte) error {
	if !chat.ValidID(id) || (status != "completed" && status != "failed" && status != "denied" && status != "cancelled") {
		return chat.ErrInvalid
	}
	var err error
	result, err = canonicalJSON(result)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		sources = []byte(`[]`)
	}
	sources, err = canonicalJSON(sources)
	if err != nil {
		return err
	}
	return r.withConversation(ctx, c, func(tx pgx.Tx) error {
		var oldStatus string
		var same bool
		if err := tx.QueryRow(ctx, "SELECT status, result IS NOT DISTINCT FROM $3::jsonb AND sources IS NOT DISTINCT FROM $4::jsonb FROM tool_calls WHERE conversation_id=$1 AND id=$2", c, id, result, sources).Scan(&oldStatus, &same); err != nil {
			return err
		}
		if oldStatus != "pending" && oldStatus != "running" {
			if status == oldStatus && same {
				return nil
			}
			return ErrConflict
		}
		_, err := tx.Exec(ctx, "UPDATE tool_calls SET status=$3,result=$4,sources=$5 WHERE conversation_id=$1 AND id=$2", c, id, status, result, sources)
		return err
	})
}

// CompleteModelAttempt settles a registered running attempt. Messages retain
// independent logical result identities, so retry attempts never duplicate them.
func (r *Repository) CompleteModelAttempt(ctx context.Context, c chat.ConversationID, id, status string, usage []byte, resultEntryID, reason string) error {
	if !chat.ValidID(id) || (status != "completed" && status != "failed" && status != "cancelled") || (resultEntryID != "" && !chat.ValidID(resultEntryID)) {
		return chat.ErrInvalid
	}
	usage, err := canonicalJSON(usage)
	if err != nil {
		return err
	}
	return r.withConversation(ctx, c, func(tx pgx.Tx) error {
		var oldStatus string
		var same bool
		err := tx.QueryRow(ctx, `SELECT status, status=$3 AND usage IS NOT DISTINCT FROM $4::jsonb AND result_entry_id IS NOT DISTINCT FROM $5::uuid AND error=$6 FROM model_steps WHERE conversation_id=$1 AND id=$2`, c, id, status, usage, nullable(resultEntryID), reason).Scan(&oldStatus, &same)
		if err != nil {
			return err
		}
		if oldStatus != "running" {
			if same {
				return nil
			}
			return ErrConflict
		}
		_, err = tx.Exec(ctx, "UPDATE model_steps SET status=$3,usage=$4,result_entry_id=$5,error=$6 WHERE conversation_id=$1 AND id=$2", c, id, status, usage, nullable(resultEntryID), reason)
		return err
	})
}

// RecordApprovalDecision persists a decision already adjudicated by the
// workflow. It never applies conversation policy or treats API intent as consent.
func (r *Repository) RecordApprovalDecision(ctx context.Context, c chat.ConversationID, id string, decision chat.ApprovalDecision, revision int64, at time.Time) error {
	if !chat.ValidID(id) || (decision != chat.ApprovalOnce && decision != chat.ApprovalAll && decision != chat.ApprovalDeny) || revision < 0 || at.IsZero() {
		return chat.ErrInvalid
	}
	at = at.UTC().Truncate(time.Microsecond)
	return r.withConversation(ctx, c, func(tx pgx.Tx) error {
		var oldDecision *string
		var oldAt *time.Time
		var oldRevision int64
		if err := tx.QueryRow(ctx, "SELECT decision,policy_revision,decided_at FROM approvals WHERE conversation_id=$1 AND id=$2", c, id).Scan(&oldDecision, &oldRevision, &oldAt); err != nil {
			return err
		}
		if oldDecision != nil {
			if *oldDecision == string(decision) && oldRevision == revision && oldAt != nil && oldAt.Equal(at) {
				return nil
			}
			return ErrConflict
		}
		_, err := tx.Exec(ctx, "UPDATE approvals SET decision=$3,policy_revision=$4,decided_at=$5 WHERE conversation_id=$1 AND id=$2", c, id, decision, revision, at)
		return err
	})
}
