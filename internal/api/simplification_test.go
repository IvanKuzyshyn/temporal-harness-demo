package api_test

import (
	"fmt"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSimplifiedCatalogAndAdmission(t *testing.T) {
	h := handler(t)
	w := request(h, "GET", "/models", "", nil)
	require.Equal(t, 200, w.Code)
	require.NotContains(t, w.Body.String(), "gemma4")
	require.Contains(t, w.Body.String(), `"imageAdmissionQualified":false`)
	w = request(h, "POST", "/conversations", fmt.Sprintf(`{"requestId":%q,"model":"gemma4:e4b"}`, uuid.NewString()), nil)
	require.Equal(t, 400, w.Code)
	for _, path := range []string{"messages", "replace"} {
		extra := ""
		if path == "replace" {
			extra = fmt.Sprintf(`,"expectedActiveTurnId":%q`, uuid.NewString())
		}
		body := fmt.Sprintf(`{"requestId":%q,"attachmentIds":[%q,%q]%s}`, uuid.NewString(), uuid.NewString(), uuid.NewString(), extra)
		w = request(h, "POST", "/conversations/"+uuid.NewString()+"/"+path, body, nil)
		require.Equal(t, 400, w.Code, path)
	}
}
