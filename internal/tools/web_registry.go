package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
)

var ErrWebAdapterUnavailable = errors.New("web tool adapter unavailable")

// WebAdapter is injected by Task7/8/9. It receives only an authorized immutable
// action and canonical arguments. No legacy registry, shell or explorer fallback.
type WebAdapter func(context.Context, chat.ApprovalScope, json.RawMessage) (chat.WebToolResult, error)
type WebRegistry struct{ adapters map[string]WebAdapter }

func NewWebRegistry(adapters map[string]WebAdapter) *WebRegistry {
	r := &WebRegistry{adapters: map[string]WebAdapter{}}
	for _, name := range []string{"web_fetch", "file_upload"} {
		if f := adapters[name]; f != nil {
			r.adapters[name] = f
		}
	}
	return r
}
func (*WebRegistry) Definitions() []llm.ToolDefinition {
	return []llm.ToolDefinition{
		{Name: "web_fetch", Description: "Request approval to fetch a known public URL.", Parameters: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"}},"required":["url"],"additionalProperties":false}`)},
		{Name: "file_upload", Description: "Request approval to analyse an already uploaded attachment with a specific instruction. Upload is not consent.", Parameters: json.RawMessage(`{"type":"object","properties":{"attachmentId":{"type":"string"},"instruction":{"type":"string"}},"required":["attachmentId","instruction"],"additionalProperties":false}`)},
	}
}
func WebDigest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func (*WebRegistry) Canonicalize(name string, raw json.RawMessage, checksum string) (chat.WebModelCall, error) {
	out := chat.WebModelCall{Name: name}
	if len(raw) > 8192 {
		return out, chat.ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return out, chat.ErrInvalid
	}
	fields := map[string]string{}
	for d.More() {
		k, e := d.Token()
		if e != nil {
			return out, chat.ErrInvalid
		}
		key, ok := k.(string)
		if !ok {
			return out, chat.ErrInvalid
		}
		if _, ok = fields[key]; ok {
			return out, chat.ErrInvalid
		}
		var value string
		if d.Decode(&value) != nil {
			return out, chat.ErrInvalid
		}
		fields[key] = value
	}
	if _, err = d.Token(); err != nil {
		return out, chat.ErrInvalid
	}
	if _, err = d.Token(); err != io.EOF {
		return out, chat.ErrInvalid
	}
	switch name {
	case "web_fetch":
		if len(fields) != 1 || fields["url"] == "" || len(fields["url"]) > 2048 {
			return out, chat.ErrInvalid
		}
		u, e := parseFetchURL(fields["url"])
		if e != nil {
			return out, chat.ErrInvalid
		}
		u.Host = strings.ToLower(u.Host)
		fields["url"] = u.String()
		out.ContentRef = WebDigest([]byte(u.String()))
	case "file_upload":
		if len(fields) != 2 || !chat.ValidID(fields["attachmentId"]) || strings.TrimSpace(fields["instruction"]) == "" || len(fields["instruction"]) > 4096 || !chat.ValidHash(checksum) {
			return out, chat.ErrInvalid
		}
		out.ContentRef = checksum
	default:
		return out, chat.ErrInvalid
	}
	out.Arguments, _ = json.Marshal(fields)
	return out, nil
}
func (r *WebRegistry) Execute(ctx context.Context, s chat.ApprovalScope, args json.RawMessage) (chat.WebToolResult, error) {
	if s.ToolsetVersion != chat.WebToolset || s.ToolVersion != "1" || (s.ToolName != "web_fetch" && s.ToolName != "file_upload") {
		return chat.WebToolResult{}, chat.ErrInvalid
	}
	if r == nil || r.adapters[s.ToolName] == nil {
		return chat.WebToolResult{}, ErrWebAdapterUnavailable
	}
	return r.adapters[s.ToolName](ctx, s, args)
}
