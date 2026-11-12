//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestHistoryExposesContextCoverageWithoutDeletingMessages(t *testing.T) {
	r, _ := database(t)
	h := realHandler(t, r)
	c := create(t, h)
	path := "/conversations/" + c + "/messages"
	accepted := request(h, "POST", path, fmt.Sprintf(`{"requestId":%q,"text":"earlier question"}`, uuid.NewString()), nil)
	require.Equal(t, 202, accepted.Code)
	a := receipt(t, accepted.Body.Bytes())
	ref := chat.TurnRef{ConversationID: chat.ConversationID(c), TurnID: a.TurnID}
	metadata := chat.ContextMetadata{Coverage: chat.ContextCoverage{RecentExchanges: 6, OmittedExchanges: 4, SummaryFailure: "summary_failed", SourceLimited: true, OutputTruncated: true}, Accounting: chat.ContextAccounting{Method: "source-bound estimate", InputTokens: 6000, OutputTokens: 2000, Reserve: 2384, Total: 10384}}
	metadata.Coverage.Evidence = &chat.ContextEvidenceCoverage{Eligible: 2, Omitted: 1, Included: []chat.ContextEvidenceSource{{Scope: chat.ApprovalScope{ConversationID: ref.ConversationID, TurnID: ref.TurnID, ToolCallID: uuid.NewString(), ContentRef: "checksum"}, Sequence: 2, ArgumentsHash: "instruction-hash", ResultHash: "original-result-hash", ExcerptBytes: 10, ExcerptTruncated: true, SourceTruncated: true, SourceError: true, ExtractionStatus: "partial"}}}
	metadata.Coverage.Quotations = &chat.ContextQuotationCoverage{Eligible: 5, Omitted: 4, Included: []chat.ContextExchangeQuotationSource{{User: chat.ContextQuotationSource{SourceID: uuid.NewString(), Speaker: "user", TurnID: ref.TurnID, Sequence: 2, TextHash: "user-text-hash", Bytes: 32}, Assistant: chat.ContextQuotationSource{SourceID: uuid.NewString(), Speaker: "assistant", TurnID: ref.TurnID, Sequence: 2, TextHash: "assistant-text-hash", Bytes: 17}}}}
	require.NoError(t, r.SaveChatModelResult(context.Background(), chat.ChatModelResult{TurnRef: ref, Text: "bounded answer", Context: &metadata}))
	require.NoError(t, r.PersistTurnResult(context.Background(), chat.TurnResult{TurnRef: ref, Status: "completed"}))
	response := request(h, "GET", path, "", nil)
	require.Equal(t, 200, response.Code)
	var page struct {
		Messages []struct {
			Role    string
			Context *chat.ContextMetadata `json:"context"`
		}
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	require.Len(t, page.Messages, 2)
	require.Nil(t, page.Messages[0].Context)
	require.NotNil(t, page.Messages[1].Context)
	require.Equal(t, metadata, *page.Messages[1].Context)
}
