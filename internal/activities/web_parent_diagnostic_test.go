package activities

// R2 observation only: retained API history + source-backed reconstruction.
// The HTTP server below is a fixture, NOT Ollama or a fresh provider sample.
import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/ollama"
	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/model/parsers"
	"github.com/ollama/ollama/model/renderers"
	"github.com/stretchr/testify/require"
)

type parentDiagnosticRepository struct {
	budgetWebRepository
	result chat.WebToolResult
}

func (r *parentDiagnosticRepository) WebToolResult(_ context.Context, _ chat.ConversationID, id string) (chat.WebToolResult, error) {
	if id != r.prior.Calls[0].ID {
		panic("wrong canonical tool identity")
	}
	return r.result, nil
}
func (r *parentDiagnosticRepository) LoadContext(context.Context, chat.TurnRef) (chat.ContextHistory, error) {
	return chat.ContextHistory{Model: r.turn.Model}, nil // retained coverage has zero prior exchanges/evidence
}
func (r *parentDiagnosticRepository) CommitContextSummary(context.Context, chat.ContextSummaryWrite) (chat.Summary, error) {
	panic("first-turn reconstruction must not summarize or make another model request")
}
func TestRetainedNarrowQwenParentPathDiagnostic(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/web-chat/qwen-narrow-parent-failure.json")
	require.NoError(t, err)
	var f struct {
		History struct {
			Messages []struct {
				Text           string
				TurnID         chat.TurnID
				ConversationID chat.ConversationID
				Context        *chat.ContextMetadata
			}
		}
		Tools []string
	}
	require.NoError(t, json.Unmarshal(raw, &f))
	require.Len(t, f.History.Messages, 2)
	user, final := f.History.Messages[0], f.History.Messages[1]
	ref := chat.TurnRef{ConversationID: user.ConversationID, TurnID: user.TurnID}
	var tool chat.WebToolResult
	require.NoError(t, json.Unmarshal([]byte(f.Tools[0]), &tool))
	require.Equal(t, "hello world", tool.Text)
	args, err := json.Marshal(map[string]string{"attachmentId": string(tool.AttachmentID), "instruction": user.Text})
	require.NoError(t, err)
	// Deterministic preflight call identity, derived from production path; the
	// retained child ID independently contains this same call ID.
	callID := chat.LogicalID(ref.TurnID, "model/0/tool/0")
	require.Equal(t, "0be216c9-3377-51f0-9c60-b985d7d1384d", callID)
	require.Contains(t, tool.Analysis.ChildID, "/"+callID+"/")
	repo := &parentDiagnosticRepository{budgetWebRepository: budgetWebRepository{
		turn:  chat.Turn{TurnRef: ref, Model: "qwen3.5:9b", Text: user.Text, AttachmentIDs: []chat.AttachmentID{tool.AttachmentID}},
		prior: &chat.ChatModelResult{TurnRef: ref, Calls: []chat.WebModelCall{{ID: callID, Name: "file_upload", Arguments: args}}},
	}, result: tool}
	var wire api.ChatRequest
	var wireBytes json.RawMessage
	calls := 0
	// Hypothetical NDJSON placement of the retained final, not original captured
	// provider chunks. Split the stray marker to exercise adapter concatenation.
	chunks := []api.ChatResponse{
		{Message: api.Message{Role: "assistant", Thinking: "Synthetic diagnostic reasoning; never a saved final."}},
		{Message: api.Message{Role: "assistant", Content: final.Text[:16]}},
		{Message: api.Message{Role: "assistant", Content: final.Text[16:]}, Done: true, DoneReason: "stop"},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			_ = json.NewEncoder(w).Encode(map[string]string{"version": "0.40.15"})
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]any{"name": "qwen3.5:9b", "model": "qwen3.5:9b", "digest": "d63f830f28d515fefea546f213fca7a75e9dd5176ef51a6deabc952d1c99a911", "details": map[string]any{"format": "safetensors", "runner": "mlx"}}}})
		case "/api/chat":
			calls++
			require.NoError(t, json.NewDecoder(r.Body).Decode(&wireBytes))
			require.NoError(t, json.Unmarshal(wireBytes, &wire))
			for _, chunk := range chunks {
				require.NoError(t, json.NewEncoder(w).Encode(chunk))
			}
		default:
			t.Errorf("unexpected fixture endpoint %s", r.URL.Path)
		}
	}))
	defer server.Close()
	model, err := ollama.New(server.URL)
	require.NoError(t, err)
	a := ChatActivities{Repository: repo, Model: model}
	ref.Step = 1
	_, err = a.ChatStep(context.Background(), ref)
	require.NoError(t, err)
	require.Equal(t, 1, calls, "exactly one fake HTTP chat request, zero real provider invocations")
	require.Len(t, wire.Messages, 4)
	require.Equal(t, user.Text, wire.Messages[1].Content)
	require.Equal(t, "assistant", wire.Messages[2].Role)
	require.Empty(t, wire.Messages[2].Content, "preflight did not invent an assistant narrative")
	require.Equal(t, callID, wire.Messages[2].ToolCalls[0].ID)
	require.Equal(t, "file_upload", wire.Messages[2].ToolCalls[0].Function.Name)
	require.Equal(t, "tool", wire.Messages[3].Role)
	require.Equal(t, callID, wire.Messages[3].ToolCallID)
	require.Equal(t, "file_upload", wire.Messages[3].ToolName)
	encodedTool, err := json.Marshal(tool)
	require.NoError(t, err)
	require.Equal(t, string(encodedTool), wire.Messages[3].Content)
	require.Len(t, wire.Tools, 2)
	require.Nil(t, wire.Think, "parent text continuation leaves provider thinking default; image child is a different envelope")
	require.Equal(t, "mlx", wire.Runner)
	require.False(t, *wire.Truncate)
	require.False(t, *wire.Shift)
	require.EqualValues(t, 16384, wire.Options["num_ctx"])
	require.EqualValues(t, 2000, wire.Options["num_predict"])
	for _, m := range wire.Messages {
		require.Empty(t, m.Images)
	}
	require.Equal(t, final.Text, repo.saved.Text, "adapter saves content bytes, not the separate synthetic thinking field")
	require.Equal(t, final.Context.Accounting, repo.saved.Context.Accounting, "source reconstruction must match retained accounting, not assume loss/truncation")
	rendered, err := renderers.RenderWithRenderer("qwen3.5", wire.Messages, wire.Tools, wire.Think)
	require.NoError(t, err)
	require.Contains(t, rendered, string(encodedTool))
	require.Contains(t, rendered, "<function=file_upload>")
	require.True(t, strings.HasSuffix(rendered, "<|im_start|>assistant\n<think>\n"))
	// These synthetic token streams characterize an ambiguity, not the lost
	// original token stream: the first close is structural; an extra close in
	// visible content is preserved by the pinned parser.
	parserResults := map[string]map[string]string{}
	for name, input := range map[string]string{
		"single_close":     final.Text,
		"additional_close": "<think>synthetic reasoning</think>" + final.Text,
	} {
		p := parsers.Qwen35Parser{}
		p.Init(wire.Tools, &wire.Messages[3], wire.Think)
		content, thinking, toolCalls, e := p.Add(input, true)
		require.NoError(t, e)
		require.Empty(t, toolCalls)
		if name == "single_close" {
			require.Equal(t, "Hello World.", thinking)
			require.NotContains(t, content, "</think>")
			require.Contains(t, content, "I can't directly")
		} else {
			require.Equal(t, final.Text, content)
		}
		parserResults[name] = map[string]string{"syntheticRaw": input, "content": content, "thinking": thinking}
	}
	if dir := os.Getenv("WEBCHAT_R2_EVIDENCE"); dir != "" {
		require.NoError(t, os.MkdirAll(dir, 0700))
		data, e := json.MarshalIndent(map[string]any{"kind": "source reconstruction and synthetic HTTP/parser fixtures, NOT original provider capture", "wire": json.RawMessage(wireBytes), "canonicalToolJSON": string(encodedTool), "rendered": rendered, "syntheticNDJSONChunks": chunks, "parserCharacterization": parserResults, "saved": repo.saved}, "", "  ")
		require.NoError(t, e)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "parent-reconstruction.json"), data, 0600))
	}
}
