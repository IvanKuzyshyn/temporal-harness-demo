package activities

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type fetchActivityRepository struct {
	budgetWebRepository
	scope                     chat.ApprovalScope
	args                      json.RawMessage
	authorized, cached, acked bool
	result                    chat.WebToolResult
}

func (r *fetchActivityRepository) BeginWebTool(_ context.Context, s chat.ApprovalScope, _ string, _ int) (json.RawMessage, bool, error) {
	if !r.authorized || s != r.scope {
		return nil, false, chat.ErrInvalid
	}
	return r.args, r.cached, nil
}
func (r *fetchActivityRepository) SaveWebToolResult(_ context.Context, _ chat.ApprovalScope, _ string, result chat.WebToolResult) error {
	r.result = result
	return nil
}
func (r *fetchActivityRepository) AcknowledgeWebToolCleanup(context.Context, chat.ApprovalScope, string) error {
	r.acked = true
	return nil
}
func (r *fetchActivityRepository) WebToolResult(context.Context, chat.ConversationID, string) (chat.WebToolResult, error) {
	return r.result, nil
}
func fetchScope(t *testing.T, url string) (chat.ApprovalScope, json.RawMessage) {
	t.Helper()
	args, _ := json.Marshal(map[string]string{"url": url})
	digest := sha256.Sum256([]byte(url))
	s := chat.ApprovalScope{ConversationID: chat.ConversationID(uuid.NewString()), TurnID: chat.TurnID(uuid.NewString()), ToolCallID: uuid.NewString(), ToolName: "web_fetch", ToolVersion: "1", ToolsetVersion: chat.WebToolset, ContentRef: hex.EncodeToString(digest[:])}
	raw, _ := json.Marshal(struct {
		Conversation chat.ConversationID
		Turn         chat.TurnID
		Set          string
		Call         chat.WebModelCall
	}{s.ConversationID, s.TurnID, chat.WebToolset, chat.WebModelCall{ID: s.ToolCallID, Name: "web_fetch", Arguments: args, ContentRef: s.ContentRef}})
	hash := sha256.Sum256(raw)
	s.ActionHash = hex.EncodeToString(hash[:])
	return s, args
}
func TestFetchRequiresApproval(t *testing.T) {
	s, args := fetchScope(t, "http://source.example/")
	var lookups atomic.Int32
	fetcher := tools.NewFetcherWithNetwork(func(context.Context, string) ([]netip.Addr, error) {
		lookups.Add(1)
		return nil, errors.New("test DNS reached")
	}, func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("unexpected dial") })
	repo := &fetchActivityRepository{scope: s, args: args}
	a := &ChatActivities{Repository: repo, WebTools: tools.NewWebRegistry(map[string]tools.WebAdapter{"web_fetch": NewWebFetchAdapter(fetcher)})}
	require.Error(t, a.ExecuteWebTool(context.Background(), chat.WebToolExecution{Scope: s}))
	require.Zero(t, lookups.Load())
	require.False(t, repo.acked)
	repo.authorized = true
	require.NoError(t, a.ExecuteWebTool(context.Background(), chat.WebToolExecution{Scope: s}))
	require.EqualValues(t, 1, lookups.Load())
	require.True(t, repo.acked)
	require.Equal(t, "tool_failed", repo.result.Error)
	repo.cached = true
	require.NoError(t, a.ExecuteWebTool(context.Background(), chat.WebToolExecution{Scope: s}))
	require.EqualValues(t, 1, lookups.Load(), "recorded result retry performs no DNS or HTTP")
}
func TestFetchAdapterBindsCanonicalActionBeforeIO(t *testing.T) {
	for _, field := range []string{"url", "content", "hash", "call", "conversation", "turn", "set", "version"} {
		t.Run(field, func(t *testing.T) {
			s, args := fetchScope(t, "http://source.example/")
			switch field {
			case "url":
				args = json.RawMessage(`{"url":"http://other.example/"}`)
			case "content":
				s.ContentRef = strings.Repeat("a", 64)
			case "hash":
				s.ActionHash = strings.Repeat("a", 64)
			case "call":
				s.ToolCallID = uuid.NewString()
			case "conversation":
				s.ConversationID = chat.ConversationID(uuid.NewString())
			case "turn":
				s.TurnID = chat.TurnID(uuid.NewString())
			case "set":
				s.ToolsetVersion = "old"
			case "version":
				s.ToolVersion = "2"
			}
			var effects atomic.Int32
			f := tools.NewFetcherWithNetwork(func(context.Context, string) ([]netip.Addr, error) {
				effects.Add(1)
				return nil, errors.New("unexpected DNS")
			}, nil)
			_, err := NewWebFetchAdapter(f)(context.Background(), s, args)
			require.Error(t, err)
			require.Zero(t, effects.Load())
		})
	}
}
func TestFetchProductionRegistryRejectsPrivateURL(t *testing.T) {
	s, args := fetchScope(t, "http://127.0.0.1/")
	repo := &fetchActivityRepository{scope: s, args: args, authorized: true}
	a := &ChatActivities{Repository: repo}
	require.NoError(t, a.ExecuteWebTool(context.Background(), chat.WebToolExecution{Scope: s}))
	require.Equal(t, "tool_failed", repo.result.Error, "production uses safe fetch, not adapter_unavailable")
	require.True(t, repo.acked)
}
func TestFetchContextRetainsSourceAndExplicitExcerptTruncation(t *testing.T) {
	s, args := fetchScope(t, "http://source.example/")
	ref := chat.TurnRef{ConversationID: s.ConversationID, TurnID: s.TurnID, Step: 1}
	repo := &fetchActivityRepository{budgetWebRepository: budgetWebRepository{turn: chat.Turn{Model: "qwen3.5:9b", Text: "explain"}, prior: &chat.ChatModelResult{TurnRef: chat.TurnRef{ConversationID: s.ConversationID, TurnID: s.TurnID}, Calls: []chat.WebModelCall{{ID: s.ToolCallID, Name: "web_fetch", Arguments: args}}}}}
	// Use JSON to test the durable wire contract independently of Go field names.
	require.NoError(t, json.Unmarshal([]byte(`{"text":"`+strings.Repeat("e", 5000)+`","requestedUrl":"http://source.example/","finalUrl":"http://source.example/final","contentType":"text/plain","truncated":false}`), &repo.result))
	m := &budgetWebModel{}
	a := &ChatActivities{Repository: repo, Model: m}
	_, err := a.ChatStep(context.Background(), ref)
	require.NoError(t, err)
	require.NotNil(t, m.request)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(m.request.Messages[3].Content), &got))
	require.Equal(t, "http://source.example/", got["requestedUrl"])
	require.Equal(t, "http://source.example/final", got["finalUrl"])
	require.Equal(t, "text/plain", got["contentType"])
	require.Equal(t, true, got["truncated"])
	require.LessOrEqual(t, len(got["text"].(string)), 4000)
}
