//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/api"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Removing the metadata-only path, ready/ownership filter or stable upload
// number must fail this test. There is deliberately NO attachment store/original.
func TestAttachmentMetadataForConsent(t *testing.T) {
	ctx := context.Background()
	r, _ := database(t)
	h, err := api.NewServer(r, api.Config{ListenAddress: "127.0.0.1:18081", ProxySecret: secret, BrowserOrigin: "http://localhost:3000"})
	require.NoError(t, err)
	c := create(t, h)
	other := create(t, h)
	ids := []chat.AttachmentID{}
	for i := 0; i < 3; i++ {
		a := chat.Attachment{ID: chat.AttachmentID(uuid.NewString()), ConversationID: chat.ConversationID(c), OriginalName: "same.txt", MediaType: "text/plain", Size: 1, Checksum: strings.Repeat("a", 64), StorageKey: "no-original-" + uuid.NewString(), Status: "staged"}
		require.NoError(t, r.CreateAttachment(ctx, a))
		if i > 0 {
			require.NoError(t, r.MarkAttachmentReady(ctx, a.ConversationID, a.ID, a.Checksum))
		}
		ids = append(ids, a.ID)
	}
	for i, id := range ids[1:] {
		path := "/conversations/" + c + "/attachments/" + string(id) + "/metadata"
		w := request(h, "GET", path, "", nil)
		require.Equal(t, 200, w.Code, w.Body.String())
		var got map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
		require.Equal(t, map[string]any{"attachment": map[string]any{"id": string(id), "conversationId": c, "name": "same.txt", "uploadNumber": float64(i + 2)}}, got)
		require.Equal(t, 404, request(h, "GET", strings.Replace(path, c, other, 1), "", nil).Code)
		unauth := httptest.NewRequest("GET", "http://127.0.0.1:18081"+path, nil)
		out := httptest.NewRecorder()
		h.ServeHTTP(out, unauth)
		require.Equal(t, 403, out.Code)
		require.Equal(t, 403, request(h, "GET", path, "", map[string]string{"Origin": "https://evil.example"}).Code)
	}
	for _, id := range []string{string(ids[0]), uuid.NewString()} {
		require.Equal(t, 404, request(h, "GET", "/conversations/"+c+"/attachments/"+id+"/metadata", "", nil).Code)
	}
	require.Equal(t, 400, request(h, "GET", "/conversations/"+c+"/attachments/invalid/metadata", "", nil).Code)
}
