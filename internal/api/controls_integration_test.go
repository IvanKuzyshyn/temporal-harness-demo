//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Each route must preserve its discriminant and target, rather than accidentally
// enqueue a normal message or authorize execution in the HTTP handler.
func TestAllControlRoutesAndProjectionFreshness(t *testing.T) {
	r, p := database(t)
	h := realHandler(t, r)
	c := create(t, h)
	ctx := context.Background()
	base := "/conversations/" + c
	w := request(h, "POST", base+"/messages", fmt.Sprintf(`{"requestId":%q,"text":"first"}`, uuid.NewString()), nil)
	require.Equal(t, 202, w.Code)
	a := receipt(t, w.Body.Bytes())
	hash := strings.Repeat("a", 64)
	toolID, approvalID := uuid.NewString(), uuid.NewString()
	require.NoError(t, r.RecordToolCall(ctx, chat.ToolCall{ID: toolID, ConversationID: chat.ConversationID(c), TurnID: a.TurnID, LogicalKey: "fetch-1", Name: "web_fetch", Arguments: json.RawMessage(`{"url":"https://example.com"}`), ActionHash: hash, ParentWorkflowID: "web-chat/" + c, Status: "pending"}))
	require.NoError(t, r.RecordApproval(ctx, chat.Approval{ID: approvalID, ConversationID: chat.ConversationID(c), ToolCallID: toolID, ActionHash: hash, Deadline: time.Now().Add(time.Hour)}))
	_, e := p.Exec(ctx, "UPDATE turns SET status='awaiting_approval' WHERE id=$1", a.TurnID)
	require.NoError(t, e)
	w = request(h, "GET", base, "", nil)
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"authoritative":false`)
	require.Contains(t, w.Body.String(), approvalID)
	require.Contains(t, w.Body.String(), `"approvalAll":"unknown"`)
	for _, tc := range []struct{ path, fields, kind string }{
		{"/replace", fmt.Sprintf(`,"expectedActiveTurnId":%q,"text":"corrected"`, a.TurnID), "replace"},
		{"/stop", fmt.Sprintf(`,"expectedActiveTurnId":%q`, a.TurnID), "stop"},
		{"/approvals/" + approvalID + "/decision", fmt.Sprintf(`,"actionHash":%q,"decision":"once"`, hash), "decide"},
		{"/approval-policy/revoke", "", "revoke"},
	} {
		id := uuid.NewString()
		w = request(h, "POST", base+tc.path, fmt.Sprintf(`{"requestId":%q%s}`, id, tc.fields), nil)
		require.Equal(t, 202, w.Code, w.Body.String())
		var kind string
		require.NoError(t, p.QueryRow(ctx, "SELECT kind FROM commands WHERE conversation_id=$1 AND request_id=$2", c, id).Scan(&kind))
		require.Equal(t, tc.kind, kind)
	}
	var status string
	require.NoError(t, p.QueryRow(ctx, "SELECT status FROM turns WHERE id=$1", a.TurnID).Scan(&status))
	require.Equal(t, "awaiting_approval", status, "API intent must not change execution projection")
	other := create(t, h)
	require.Equal(t, 404, request(h, "GET", "/conversations/"+other+"/commands/"+string(a.CommandID), "", nil).Code)
	require.Equal(t, 400, request(h, "POST", base+"/messages", fmt.Sprintf(`{"requestId":%q,"text":"x","attachmentIds":[%q,%q,%q,%q,%q]}`, uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()), nil).Code)
	require.Equal(t, 202, request(h, "POST", base+"/messages", fmt.Sprintf(`{"requestId":%q,"text":%q}`, uuid.NewString(), strings.Repeat("x", 65536)), nil).Code)
}
