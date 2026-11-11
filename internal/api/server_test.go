package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/api"
	"github.com/stretchr/testify/require"
)

const secret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func handler(t *testing.T) http.Handler {
	t.Helper()
	h, err := api.NewServer(nil, api.Config{ListenAddress: "127.0.0.1:18081", ProxySecret: secret, BrowserOrigin: "http://localhost:3000"})
	require.NoError(t, err)
	return h
}
func request(h http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:18081"+path, strings.NewReader(body))
	r.Header.Set("X-Webchat-Secret", secret)
	r.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// Removing the secret guard would expose DB-backed operations to local browsers.
func TestPrivateAPISecretRequired(t *testing.T) {
	for _, s := range []string{"", "incorrect"} {
		w := request(handler(t), "GET", "/models", "", map[string]string{"X-Webchat-Secret": s})
		require.Equal(t, 403, w.Code)
		require.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
	}
	require.Equal(t, 200, request(handler(t), "GET", "/models", "", nil).Code)
}
func TestCrossSiteMutationRejected(t *testing.T) {
	for _, headers := range []map[string]string{{"Origin": "https://evil.example"}, {"Sec-Fetch-Site": "cross-site"}, {"Origin": "null"}, {"Sec-Fetch-Site": "same-site"}, {"Origin": "http://localhost:3000", "Sec-Fetch-Mode": "navigate"}} {
		require.Equal(t, 403, request(handler(t), "POST", "/conversations", `{}`, headers).Code)
	}
	r := httptest.NewRequest("GET", "http://evil.example/models", nil)
	r.Header.Set("X-Webchat-Secret", secret)
	w := httptest.NewRecorder()
	handler(t).ServeHTTP(w, r)
	require.Equal(t, 403, w.Code)
}
func TestInvalidModelRejected(t *testing.T) {
	for _, model := range []string{`"qwen:latest"`, `""`, `null`} {
		w := request(handler(t), "POST", "/conversations", `{"requestId":"11111111-1111-4111-8111-111111111111","model":`+model+`}`, nil)
		require.Equal(t, 400, w.Code, w.Body.String())
	}
}
func TestBoundedStrictJSON(t *testing.T) {
	for _, tc := range []struct {
		body, media string
		status      int
	}{
		{`{"requestId":"11111111-1111-4111-8111-111111111111","backend":"fake"}`, "application/json", 400},
		{`{} {}`, "application/json", 400}, {`null`, "application/json", 400},
		{`{}`, "text/plain", 415}, {strings.Repeat(" ", 500000), "application/json", 413},
	} {
		require.Equal(t, tc.status, request(handler(t), "POST", "/conversations", tc.body, map[string]string{"Content-Type": tc.media}).Code)
	}
	require.Equal(t, 501, request(handler(t), "GET", "/conversations/11111111-1111-4111-8111-111111111111/events", "", nil).Code)
}
func TestConfigurationRejectsInsecureValues(t *testing.T) {
	good := api.Config{ListenAddress: "127.0.0.1:18081", ProxySecret: secret, BrowserOrigin: "http://localhost:3000"}
	for _, change := range []func(*api.Config){
		func(c *api.Config) { c.ListenAddress = "0.0.0.0:18081" }, func(c *api.Config) { c.ListenAddress = "localhost:18081" },
		func(c *api.Config) { c.ProxySecret = "" }, func(c *api.Config) { c.ProxySecret = strings.Repeat("0", 64) },
		func(c *api.Config) { c.BrowserOrigin = "https://evil.example" }, func(c *api.Config) { c.BrowserOrigin = "http://localhost:3000/path" },
	} {
		cfg := good
		change(&cfg)
		_, err := api.NewServer(nil, cfg)
		require.Error(t, err)
	}
}
