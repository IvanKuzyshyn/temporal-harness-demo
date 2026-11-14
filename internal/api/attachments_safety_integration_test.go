//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/api"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/attachments"
	"github.com/stretchr/testify/require"
)

func TestAttachmentMultipartTailCannotPublish(t *testing.T) {
	r, p := database(t)
	s, e := attachments.NewStore(t.TempDir(), r, nil)
	require.NoError(t, e)
	defer s.Close()
	h, e := api.NewServer(r, api.Config{ListenAddress: "127.0.0.1:18081", ProxySecret: secret, BrowserOrigin: "http://localhost:3000", Attachments: s})
	require.NoError(t, e)
	c := create(t, h)
	for _, badTail := range []bool{false, true} {
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		file, e := w.CreateFormFile("file", "a.txt")
		require.NoError(t, e)
		file.Write([]byte("never committed"))
		if !badTail {
			require.NoError(t, w.WriteField("unexpected", "extra"))
			require.NoError(t, w.Close())
		}
		req := httptest.NewRequest("POST", "http://127.0.0.1:18081/conversations/"+c+"/attachments", &b)
		req.Header.Set("Content-Type", w.FormDataContentType())
		req.Header.Set("X-Webchat-Secret", secret)
		out := httptest.NewRecorder()
		h.ServeHTTP(out, req)
		require.NotEqual(t, 201, out.Code)
		var n int
		require.NoError(t, p.QueryRow(context.Background(), "SELECT count(*) FROM attachments WHERE conversation_id=$1", c).Scan(&n))
		require.Zero(t, n)
	}
}
func TestAttachmentSVGAlwaysDownloads(t *testing.T) {
	if os.Getenv("WEBCHAT_TEST_PARSERS") != "1" {
		t.Skip("explicit parser container gate")
	}
	r, _ := database(t)
	s, e := attachments.NewStore(t.TempDir(), r, nil)
	require.NoError(t, e)
	defer s.Close()
	h, e := api.NewServer(r, api.Config{ListenAddress: "127.0.0.1:18081", ProxySecret: secret, BrowserOrigin: "http://localhost:3000", Attachments: s, RequestTimeout: 30 * time.Second})
	require.NoError(t, e)
	c := create(t, h)
	svg := `<svg xmlns="http://www.w3.org/2000/svg" width="20" height="20"><rect width="20" height="20"/></svg>`
	w := upload(t, h, c, "x.svg", svg)
	require.Equal(t, 201, w.Code, w.Body.String())
	var out struct {
		Attachment struct {
			ID string `json:"id"`
		} `json:"attachment"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	w = request(h, "GET", "/conversations/"+c+"/attachments/"+out.Attachment.ID, "", nil)
	require.Equal(t, 200, w.Code)
	require.True(t, strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;"))
	require.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	require.Equal(t, svg, w.Body.String())
}
