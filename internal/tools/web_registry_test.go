package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/stretchr/testify/require"
)

func TestNewToolNotCoveredByOldGrant(t *testing.T) {
	r := NewWebRegistry(nil)
	for _, name := range []string{"run_command", "read_file", "delegate", "web_fetch@2", "search"} {
		_, err := r.Canonicalize(name, []byte(`{"url":"https://example.com/"}`), "")
		require.Error(t, err, "old web consent cannot authorize %s", name)
	}
	require.Len(t, r.Definitions(), 2)
}
func TestWebRegistryCanonicalArgumentsAndUnavailableAdapter(t *testing.T) {
	r := NewWebRegistry(nil)
	a, err := r.Canonicalize("web_fetch", []byte(`{"url":"https://EXAMPLE.com/a"}`), "")
	require.NoError(t, err)
	b, err := r.Canonicalize("web_fetch", []byte(`{ "url" : "https://example.com/a" }`), "")
	require.NoError(t, err)
	require.Equal(t, a, b)
	for _, args := range []string{`{"url":"https://example.com","url":"https://evil.example"}`, `{"url":"https://example.com","shell":"pwd"}`, `{"url":"file:///etc/passwd"}`} {
		_, err = r.Canonicalize("web_fetch", []byte(args), "")
		require.Error(t, err)
	}
	_, err = r.Canonicalize("file_upload", []byte(`{"attachmentId":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","instruction":"explain"}`), "")
	require.Error(t, err, "metadata checksum required, no file read")
	_, err = r.Execute(context.Background(), chat.ApprovalScope{ToolName: "web_fetch", ToolsetVersion: chat.WebToolset, ToolVersion: "1", ContentRef: strings.Repeat("a", 64)}, a.Arguments)
	require.ErrorIs(t, err, ErrWebAdapterUnavailable)
}
