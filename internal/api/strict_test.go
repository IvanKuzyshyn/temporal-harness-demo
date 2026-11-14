package api_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Duplicate keys create parser-dependent identities across Go and the proxy;
// reject them rather than let one parser silently choose the last value.
func TestDuplicateJSONKeysRejected(t *testing.T) {
	w := request(handler(t), "POST", "/conversations", `{"requestId":"11111111-1111-4111-8111-111111111111","model":"gemma4:e4b","model":"qwen3.5:9b"}`, nil)
	require.Equal(t, 400, w.Code, w.Body.String())
}
