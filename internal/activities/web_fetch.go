package activities

import (
	"context"
	"encoding/json"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
)

// NewWebFetchAdapter runs only behind ExecuteWebTool's durable authorization
// and invocation fence. Recheck the URL/action binding before even DNS lookup.
func NewWebFetchAdapter(f *tools.Fetcher) tools.WebAdapter {
	return func(ctx context.Context, s chat.ApprovalScope, args json.RawMessage) (chat.WebToolResult, error) {
		if s.ToolName != "web_fetch" || s.ToolVersion != "1" || s.ToolsetVersion != chat.WebToolset {
			return chat.WebToolResult{}, chat.ErrInvalid
		}
		call, err := tools.NewWebRegistry(nil).Canonicalize("web_fetch", args, "")
		if err != nil || call.ContentRef != s.ContentRef {
			return chat.WebToolResult{}, chat.ErrInvalid
		}
		call.ID = s.ToolCallID
		raw, _ := json.Marshal(struct {
			Conversation chat.ConversationID
			Turn         chat.TurnID
			Set          string
			Call         chat.WebModelCall
		}{s.ConversationID, s.TurnID, chat.WebToolset, call})
		if tools.WebDigest(raw) != s.ActionHash {
			return chat.WebToolResult{}, chat.ErrInvalid
		}
		var in tools.FetchArgs
		if err = json.Unmarshal(call.Arguments, &in); err != nil {
			return chat.WebToolResult{}, err
		}
		out, err := f.Fetch(ctx, in)
		if err != nil {
			return chat.WebToolResult{}, err
		}
		return chat.WebToolResult{Text: out.Text, RequestedURL: out.RequestedURL, FinalURL: out.FinalURL, ContentType: out.ContentType, Truncated: out.Truncated}, nil
	}
}

func productionWebTools() *tools.WebRegistry {
	return tools.NewWebRegistry(map[string]tools.WebAdapter{"web_fetch": NewWebFetchAdapter(tools.NewFetcher())})
}
