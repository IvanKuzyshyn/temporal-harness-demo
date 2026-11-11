package storage

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
)

type ConversationPage struct {
	Conversations []chat.Conversation
	Cursor        string
}

// ListConversations pages most-recent activity at a stable committed watermark.
// Membership/order are a snapshot; each conversation's metadata is current.
func (r *Repository) ListConversations(ctx context.Context, cursor string, limit int) (ConversationPage, error) {
	out := ConversationPage{Conversations: []chat.Conversation{}}
	if !limitOK(limit) || len(cursor) > 128 {
		return out, chat.ErrInvalid
	}
	var watermark, before int64
	if cursor == "" {
		if err := r.pool.QueryRow(ctx, "SELECT sequence FROM sidebar_clock WHERE singleton").Scan(&watermark); err != nil {
			return out, err
		}
		before = watermark + 1
	} else {
		raw, e := base64.RawURLEncoding.DecodeString(cursor)
		if e != nil {
			return out, chat.ErrInvalid
		}
		parts := strings.Split(string(raw), ":")
		if len(parts) != 3 || parts[0] != "v1" {
			return out, chat.ErrInvalid
		}
		watermark, e = strconv.ParseInt(parts[1], 10, 64)
		if e != nil || watermark < 1 {
			return out, chat.ErrInvalid
		}
		before, e = strconv.ParseInt(parts[2], 10, 64)
		if e != nil || before < 1 || before > watermark {
			return out, chat.ErrInvalid
		}
	}
	rows, err := r.pool.Query(ctx, `WITH latest AS (
 SELECT conversation_id,max(sequence) AS seq FROM conversation_activity WHERE sequence<=$1 GROUP BY conversation_id)
 SELECT c.id::text,c.title,c.model,c.workflow_id,c.lifecycle,c.policy_revision,c.created_at,c.updated_at,l.seq
 FROM latest l JOIN conversations c ON c.id=l.conversation_id WHERE l.seq<$2 ORDER BY l.seq DESC LIMIT $3`, watermark, before, limit+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	var seq int64
	for rows.Next() {
		var c chat.Conversation
		var n int64
		if err = rows.Scan(&c.ID, &c.Title, &c.Model, &c.WorkflowID, &c.Lifecycle, &c.PolicyRevision, &c.CreatedAt, &c.UpdatedAt, &n); err != nil {
			return out, err
		}
		if len(out.Conversations) == limit {
			out.Cursor = base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("v1:%d:%d", watermark, seq)))
			break
		}
		out.Conversations = append(out.Conversations, c)
		seq = n
	}
	return out, rows.Err()
}
