//go:build integration

package api_test

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/api"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/attachments"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func attachmentHandler(t *testing.T) (http.Handler, string) {
	r, _ := database(t)
	s, e := attachments.NewStore(t.TempDir(), r, nil)
	require.NoError(t, e)
	t.Cleanup(func() { s.Close() })
	h, e := api.NewServer(r, api.Config{ListenAddress: "127.0.0.1:18081", ProxySecret: secret, BrowserOrigin: "http://localhost:3000", Attachments: s})
	require.NoError(t, e)
	return h, create(t, h)
}
func upload(t *testing.T, h http.Handler, c, name, content string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	p, e := w.CreateFormFile("file", name)
	require.NoError(t, e)
	_, e = p.Write([]byte(content))
	require.NoError(t, e)
	require.NoError(t, w.Close())
	r := httptest.NewRequest("POST", "http://127.0.0.1:18081/conversations/"+c+"/attachments", &body)
	r.Header.Set("Content-Type", w.FormDataContentType())
	r.Header.Set("X-Webchat-Secret", secret)
	out := httptest.NewRecorder()
	h.ServeHTTP(out, r)
	return out
}
func TestUploadOnePerMessage(t *testing.T) {
	h, c := attachmentHandler(t)
	ids := []string{}
	for i := 0; i < 5; i++ {
		w := upload(t, h, c, "a.txt", "secret original")
		require.Equal(t, 201, w.Code, w.Body.String())
		var out struct {
			Attachment struct {
				ID string `json:"id"`
			} `json:"attachment"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
		ids = append(ids, out.Attachment.ID)
		require.NotContains(t, w.Body.String(), "secret original")
		require.NotContains(t, w.Body.String(), "storageKey")
	}
	for _, n := range []int{1, 2} {
		b, _ := json.Marshal(map[string]any{"requestId": uuid.NewString(), "attachmentIds": ids[:n]})
		w := request(h, "POST", "/conversations/"+c+"/messages", string(b), nil)
		if n == 1 {
			require.Equal(t, 202, w.Code, w.Body.String())
		} else {
			require.Equal(t, 400, w.Code, w.Body.String())
		}
	}
	b, _ := json.Marshal(map[string]any{"requestId": uuid.NewString(), "attachmentIds": ids[:2], "expectedActiveTurnId": uuid.NewString()})
	w := request(h, "POST", "/conversations/"+c+"/replace", string(b), nil)
	require.Equal(t, 400, w.Code, w.Body.String())
}
func TestAttachmentDownloadAndAPIValidation(t *testing.T) {
	h, c := attachmentHandler(t)
	w := upload(t, h, c, "unsafe.txt", "<script>untrusted</script>")
	require.Equal(t, 201, w.Code, w.Body.String())
	var out struct {
		Attachment struct {
			ID string `json:"id"`
		} `json:"attachment"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	path := "/conversations/" + c + "/attachments/" + out.Attachment.ID
	w = request(h, "GET", path, "", nil)
	require.Equal(t, 200, w.Code)
	require.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	require.Contains(t, w.Header().Get("Content-Disposition"), "attachment;")
	require.Equal(t, "<script>untrusted</script>", w.Body.String())
	w = request(h, "GET", strings.Replace(path, c, uuid.NewString(), 1), "", nil)
	require.Equal(t, 404, w.Code)
	w = upload(t, h, c, "a.txt", strings.Repeat("a", 1048577))
	require.Equal(t, 413, w.Code, w.Body.String())
	w = upload(t, h, c, "a.png", "not PNG")
	require.Equal(t, 422, w.Code, w.Body.String())
	w = upload(t, h, uuid.NewString(), "a.txt", "x")
	require.Equal(t, 404, w.Code, w.Body.String())
}
