package main

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestConfigFailsClosed(t *testing.T) {
	good := map[string]string{"WEBCHAT_API_LISTEN": "127.0.0.1:18081", "WEBCHAT_BROWSER_ORIGIN": "http://localhost:3000", "WEBCHAT_PROXY_SECRET": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "WEBCHAT_DATABASE_URL": "postgres://webchat:abcdefghijklmnopqrstuvwxyZ123456@127.0.0.1:15432/webchat?sslmode=disable"}
	for _, key := range []string{"WEBCHAT_API_LISTEN", "WEBCHAT_BROWSER_ORIGIN", "WEBCHAT_PROXY_SECRET", "WEBCHAT_DATABASE_URL"} {
		t.Run(key, func(t *testing.T) {
			env := func(k string) string {
				if k == key {
					return ""
				}
				return good[k]
			}
			_, _, e := loadConfig(env)
			require.Error(t, e)
		})
	}
	_, _, e := loadConfig(func(k string) string { return good[k] })
	require.NoError(t, e)
	for _, raw := range []string{
		"postgres://webchat:short@127.0.0.1:15432/webchat",
		"postgres://webchat:abcdefghijklmnopqrstuvwxyZ123456@127.0.0.1:15432/webchat?user=postgres",
		"postgres://webchat:abcdefghijklmnopqrstuvwxyZ123456@127.0.0.1:15432/webchat?dbname=temporal",
		"postgres://webchat:abcdefghijklmnopqrstuvwxyZ123456@127.0.0.1:15432/webchat?options=-c%20role%3Dpostgres",
		"postgres://webchat:abcdefghijklmnopqrstuvwxyZ123456@192.168.1.2:15432/webchat",
	} {
		_, _, e := loadConfig(func(k string) string {
			if k == "WEBCHAT_DATABASE_URL" {
				return raw
			}
			return good[k]
		})
		require.Error(t, e)
		require.NotContains(t, e.Error(), "abcdefghijklmnopqrstuvwxyZ123456")
	}
	_, _, e = loadConfig(func(k string) string {
		if k == "WEBCHAT_EXECUTION_BACKEND" {
			return "fake"
		}
		return good[k]
	})
	require.Error(t, e)
}

// A shutdown signal drains HTTP requests and returns; it never submits stop or
// deletes pending commands. The listener is test-owned and loopback ephemeral.
func TestGracefulHTTPShutdown(t *testing.T) {
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, e)
	entered := make(chan struct{})
	release := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; w.WriteHeader(202) })}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, server, listener) }()
	result := make(chan int, 1)
	go func() {
		resp, e := http.Get("http://" + listener.Addr().String())
		if e != nil {
			result <- 0
			return
		}
		defer resp.Body.Close()
		result <- resp.StatusCode
	}()
	<-entered
	cancel()
	close(release)
	select {
	case code := <-result:
		require.Equal(t, 202, code)
	case <-time.After(time.Second):
		t.Fatal("request did not drain")
	}
	select {
	case e := <-done:
		require.NoError(t, e)
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not finish")
	}
}
