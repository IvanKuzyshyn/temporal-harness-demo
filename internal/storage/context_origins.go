package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/jackc/pgx/v5"
)

// The bounded earliest slice provides original speaker attribution independently
// of generative memory. Exclude the newest six (already supplied as recent),
// current/cancelled turns and all other conversations. No attachment access.
func loadContextQuotations(ctx context.Context, tx pgx.Tx, ref chat.TurnRef, out *chat.ContextHistory) error {
	out.QuotationTotal = out.Total - 6
	if out.QuotationTotal <= 0 {
		out.QuotationTotal = 0
		return nil
	}
	limit := min(out.QuotationTotal, chat.MaxContextQuotations)
	rows, e := tx.Query(ctx, `SELECT u.entry_id::text,u.text,a.entry_id::text,a.text,t.id::text,a.sequence`+completedContext+` ORDER BY a.sequence LIMIT $3`, ref.ConversationID, ref.TurnID, limit)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var pair chat.ContextExchangeQuotation
		if e = rows.Scan(&pair.User.SourceID, &pair.User.Text, &pair.Assistant.SourceID, &pair.Assistant.Text, &pair.User.TurnID, &pair.User.Sequence); e != nil {
			return e
		}
		pair.User.Speaker = "user"
		pair.Assistant.Speaker = "assistant"
		pair.Assistant.TurnID = pair.User.TurnID
		pair.Assistant.Sequence = pair.User.Sequence
		for _, q := range []*chat.ContextQuotation{&pair.User, &pair.Assistant} {
			hash := sha256.Sum256([]byte(q.Text))
			q.TextHash = hex.EncodeToString(hash[:])
			q.Bytes = len(q.Text)
		}
		out.Quotations = append(out.Quotations, pair)
	}
	return rows.Err()
}
