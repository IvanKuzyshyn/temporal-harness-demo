package api_test

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"os"
	"strings"
	"testing"
)

func TestAttachmentErrorContract(t *testing.T) {
	b, e := os.ReadFile("../../docs/web-api.md")
	require.NoError(t, e)
	raw := strings.Split(strings.Split(string(b), "```json\n")[1], "\n```")[0]
	var schema struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &schema))
	var definition struct {
		Properties struct {
			Error struct {
				Properties struct {
					Code struct {
						Enum []string `json:"enum"`
					} `json:"code"`
				} `json:"properties"`
			} `json:"error"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(schema.Defs["Error"], &definition))
	for _, code := range []string{"attachment_store_unavailable", "multipart_required", "one_file_required", "file_exceeds_1048576_bytes", "invalid_signature_or_unreadable_file"} {
		require.Contains(t, definition.Properties.Error.Properties.Code.Enum, code)
	}
}
