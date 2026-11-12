package context

import (
	"unicode/utf8"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
)

// Excerpt clips only optional readable text, never source/status/coverage
// metadata or mandatory prompt/tool arguments. UTF-8 byte limits are stricter
// than the approximate 4K-token target. Existing partial semantics survive.
func Excerpt(result chat.WebToolResult, remaining *int) chat.WebToolResult {
	n := *remaining
	if n < 0 {
		n = 0
	}
	if len(result.Text) > n {
		for n > 0 && !utf8.RuneStart(result.Text[n]) {
			n--
		}
		result.Text = result.Text[:n]
		result.Truncated = true
	}
	*remaining -= len(result.Text)
	return result
}
