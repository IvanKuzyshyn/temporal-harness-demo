package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	modelcontext "github.com/IvanKuzyshyn/temporal-harness-demo/internal/context"
	"github.com/jackc/pgx/v5"
)

const completedContext = ` FROM turns t JOIN messages u ON u.turn_id=t.id AND u.role='user' AND u.result_kind='prompt'
 JOIN messages a ON a.turn_id=t.id AND a.role='assistant' AND a.result_kind='final'
 WHERE t.conversation_id=$1 AND t.id<>$2 AND t.status='completed' AND a.completion_state='completed'`

func readSummary(ctx context.Context, tx pgx.Tx, c chat.ConversationID, version int64) (*chat.Summary, error) {
	var s chat.Summary
	err := tx.QueryRow(ctx, `SELECT id::text,conversation_id::text,version,covered_sequence,text,model,budget FROM conversation_summaries WHERE conversation_id=$1 AND ($2::bigint=0 OR version=$2) ORDER BY version DESC LIMIT 1`, c, version).Scan(&s.ID, &s.ConversationID, &s.Version, &s.CoveredSequence, &s.Text, &s.Model, &s.Budget)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &s, err
}
func (r *Repository) LoadContext(ctx context.Context, ref chat.TurnRef) (out chat.ContextHistory, err error) {
	err = r.withConversation(ctx, ref.ConversationID, func(tx pgx.Tx) error {
		if e := tx.QueryRow(ctx, `SELECT v.model FROM conversations v JOIN turns t ON t.conversation_id=v.id WHERE v.id=$1 AND t.id=$2`, ref.ConversationID, ref.TurnID).Scan(&out.Model); e != nil {
			return e
		}
		var e error
		out.Summary, e = readSummary(ctx, tx, ref.ConversationID, 0)
		if e != nil {
			return e
		}
		if e = tx.QueryRow(ctx, `SELECT count(*)`+completedContext, ref.ConversationID, ref.TurnID).Scan(&out.Total); e != nil {
			return e
		}
		boundary := int64(0)
		if out.Summary != nil {
			boundary = out.Summary.CoveredSequence
		}
		// Bounded SQL retrieval even for arbitrarily long histories. Only the oldest
		// unsummarized prefix can advance the watermark; oversized exchanges are not
		// skipped. The newest six are disjoint from the summary prefix.
		for _, recent := range []bool{true, false} {
			suffix := ` AND a.sequence>$3 ORDER BY a.sequence DESC LIMIT 6`
			args := []any{ref.ConversationID, ref.TurnID, boundary}
			if !recent {
				cutoff := int64(9223372036854775807)
				if len(out.Recent) > 0 {
					cutoff = out.Recent[len(out.Recent)-1].Sequence
				}
				suffix = ` AND a.sequence>$3 AND a.sequence<$4 ORDER BY a.sequence LIMIT 64`
				args = append(args, cutoff)
			}
			rows, e := tx.Query(ctx, `SELECT u.text,a.text,a.sequence,t.id::text`+completedContext+suffix, args...)
			if e != nil {
				return e
			}
			type item struct {
				exchange chat.Exchange
				turn     chat.TurnID
			}
			var items []item
			for rows.Next() {
				var i item
				if e = rows.Scan(&i.exchange.User, &i.exchange.Assistant, &i.exchange.Sequence, &i.turn); e != nil {
					rows.Close()
					return e
				}
				items = append(items, i)
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
			for _, i := range items {
				// Prompt/attachment provenance is persisted metadata, not authorization to
				// read originals. Preserve it for completed replacement turns too.
				var provenance []byte
				e = tx.QueryRow(ctx, `SELECT jsonb_build_object('attachmentIds',coalesce((SELECT jsonb_agg(ma.attachment_id ORDER BY ma.ordinal) FROM messages m JOIN message_attachments ma ON ma.entry_id=m.entry_id WHERE m.turn_id=t.id AND m.result_kind='prompt'),'[]'::jsonb),'replacementTarget',t.replacement_target,'originalPrompt',(SELECT text FROM messages WHERE turn_id=t.replacement_target AND result_kind='prompt'),'originalAttachmentIds',coalesce((SELECT jsonb_agg(ma.attachment_id ORDER BY ma.ordinal) FROM messages m JOIN message_attachments ma ON ma.entry_id=m.entry_id WHERE m.turn_id=t.replacement_target AND m.result_kind='prompt'),'[]'::jsonb)) FROM turns t WHERE t.id=$1`, i.turn).Scan(&provenance)
				if e != nil {
					return e
				}
				var p struct {
					AttachmentIDs     []string `json:"attachmentIds"`
					ReplacementTarget *string  `json:"replacementTarget"`
				}
				if e = json.Unmarshal(provenance, &p); e != nil {
					return e
				}
				if len(p.AttachmentIDs) > 0 || p.ReplacementTarget != nil {
					i.exchange.User += "\nPersisted prompt provenance (IDs only, no original access): " + string(provenance)
				}
				// Final requests retain evidence independently below, not once in
				// each recent exchange and again beside the summary.
				if recent {
					out.Recent = append(out.Recent, i.exchange)
					continue
				}
				var n int
				if e = tx.QueryRow(ctx, `SELECT count(*) FROM tool_calls WHERE conversation_id=$1 AND turn_id=$2 AND status='completed' AND authorized_scope IS NOT NULL`, ref.ConversationID, i.turn).Scan(&n); e != nil {
					return e
				}
				results, e := tx.Query(ctx, `SELECT result FROM tool_calls WHERE conversation_id=$1 AND turn_id=$2 AND status='completed' AND authorized_scope IS NOT NULL ORDER BY logical_key,id LIMIT 4`, ref.ConversationID, i.turn)
				if e != nil {
					return e
				}
				for results.Next() {
					var raw []byte
					var result chat.WebToolResult
					if e = results.Scan(&raw); e == nil {
						e = json.Unmarshal(raw, &result)
					}
					if e != nil {
						results.Close()
						return e
					}
					i.exchange.Results = append(i.exchange.Results, result)
				}
				e = results.Err()
				results.Close()
				if e != nil {
					return e
				}
				i.exchange.OmittedResults = n - len(i.exchange.Results)
				out.Older = append(out.Older, i.exchange)
			}
		}
		return loadContextEvidence(ctx, tx, ref, &out)
	})
	return
}

// CommitContextSummary atomically validates the exact contiguous eligible
// completion interval and current attempt ownership. Boundary/version are
// immutable; racing or lost-ACK retries adopt only the canonical stored winner.
func (r *Repository) CommitContextSummary(ctx context.Context, w chat.ContextSummaryWrite) (out chat.Summary, err error) {
	s := w.Summary
	var metadata map[string]json.RawMessage
	if len(s.Budget) > 8192 || json.Unmarshal(s.Budget, &metadata) != nil || metadata == nil {
		return out, chat.ErrInvalid
	}
	metadata["ownerAttemptId"], _ = json.Marshal(w.AttemptID)
	s.Budget, _ = json.Marshal(metadata)
	s.Budget, _ = canonicalJSON(s.Budget)
	if !chat.ValidID(w.AttemptID) || s.ConversationID != w.ConversationID || s.Version != w.PreviousVersion+1 || s.CoveredSequence <= w.PreviousBoundary || len(w.Sequences) == 0 || len(w.Sequences) > 64 || s.CoveredSequence != w.Sequences[len(w.Sequences)-1] || s.ID != chat.LogicalID(chat.TurnID(w.ConversationID), fmt.Sprintf("context-summary/%d", s.Version)) || !utf8.ValidString(s.Text) || strings.TrimSpace(s.Text) == "" || modelcontext.SummaryTextBound(s.Text) > 1500 {
		return out, chat.ErrInvalid
	}
	err = r.withConversation(ctx, w.ConversationID, func(tx pgx.Tx) error {
		var model string
		if e := tx.QueryRow(ctx, `SELECT model FROM conversations WHERE id=$1`, w.ConversationID).Scan(&model); e != nil {
			return e
		}
		if model != s.Model {
			return ErrConflict
		}
		winner, e := readSummary(ctx, tx, w.ConversationID, s.Version)
		if e != nil {
			return e
		}
		if winner != nil {
			var owner struct {
				AttemptID string `json:"ownerAttemptId"`
			}
			if json.Unmarshal(winner.Budget, &owner) != nil {
				return ErrConflict
			}
			if owner.AttemptID == w.AttemptID {
				winner.Budget, _ = canonicalJSON(winner.Budget)
				oldHash, e := digest(*winner)
				if e != nil {
					return e
				}
				newHash, e := digest(s)
				if e != nil {
					return e
				}
				if oldHash != newHash {
					return ErrConflict
				}
			}
			out = *winner
			return nil
		}
		prior, e := readSummary(ctx, tx, w.ConversationID, 0)
		if e != nil {
			return e
		}
		version, boundary, previousCount := int64(0), int64(0), 0
		if prior != nil {
			version, boundary = prior.Version, prior.CoveredSequence
			var m struct {
				Exchanges int `json:"exchanges"`
			}
			if json.Unmarshal(prior.Budget, &m) != nil {
				return ErrConflict
			}
			previousCount = m.Exchanges
		}
		if version != w.PreviousVersion || boundary != w.PreviousBoundary {
			return ErrConflict
		}
		var owns bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM model_steps s JOIN turns t ON t.id=s.turn_id WHERE s.id=$1 AND s.conversation_id=$2 AND s.turn_id=$3 AND s.logical_step=$4 AND s.status='running' AND t.status IN ('queued','running') AND NOT t.model_invocations_closed AND EXISTS(SELECT 1 FROM chat_model_invocations i WHERE i.turn_id=t.id AND i.cleanup_confirmed_at IS NULL) AND NOT EXISTS(SELECT 1 FROM model_steps n WHERE n.turn_id=s.turn_id AND n.logical_step=s.logical_step AND n.attempt>s.attempt))`, w.AttemptID, w.ConversationID, w.TurnID, w.Step).Scan(&owns); e != nil {
			return e
		}
		if !owns {
			return ErrStaleChatAttempt
		}
		rows, e := tx.Query(ctx, `SELECT a.sequence`+completedContext+` AND a.sequence>$3 AND a.sequence<=$4 ORDER BY a.sequence`, w.ConversationID, w.TurnID, boundary, s.CoveredSequence)
		if e != nil {
			return e
		}
		var actual []int64
		for rows.Next() {
			var sequence int64
			if e = rows.Scan(&sequence); e != nil {
				rows.Close()
				return e
			}
			actual = append(actual, sequence)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if !slices.Equal(actual, w.Sequences) {
			return ErrConflict
		}
		var meta struct {
			PreviousVersion  int64 `json:"previousVersion"`
			PreviousBoundary int64 `json:"previousBoundary"`
			Exchanges        int   `json:"exchanges"`
			SourceLimited    bool  `json:"sourceLimited"`
		}
		if json.Unmarshal(s.Budget, &meta) != nil || meta.PreviousVersion != version || meta.PreviousBoundary != boundary || meta.Exchanges != previousCount+len(actual) || !meta.SourceLimited {
			return ErrConflict
		}
		hash, e := digest(s)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `INSERT INTO conversation_summaries(id,conversation_id,version,covered_sequence,text,model,budget,payload_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, s.ID, s.ConversationID, s.Version, s.CoveredSequence, s.Text, s.Model, []byte(s.Budget), hash)
		out = s
		return e
	})
	return
}
