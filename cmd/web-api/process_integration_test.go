//go:build integration

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestExecutableProcess(t *testing.T) {
	if os.Getenv("WEBCHAT_TEST_API_PROCESS") != "1" {
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}
func TestExecutableQueuedMode(t *testing.T) {
	raw := os.Getenv("WEBCHAT_TEST_DATABASE_URL")
	require.NotEmpty(t, raw)
	cfg, e := pgxpool.ParseConfig(raw)
	require.NoError(t, e)
	require.Equal(t, "127.0.0.1", cfg.ConnConfig.Host)
	require.True(t, strings.HasPrefix(cfg.ConnConfig.Database, "webchat_test_"))
	reservation, e := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, e)
	address := reservation.Addr().String()
	require.NoError(t, reservation.Close())
	exe, e := os.Executable()
	require.NoError(t, e)
	key := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	client := &http.Client{Timeout: time.Second}
	call := func(method, path, body string) (int, []byte) {
		req, e := http.NewRequest(method, "http://"+address+path, strings.NewReader(body))
		require.NoError(t, e)
		req.Header.Set("X-Webchat-Secret", key)
		req.Header.Set("Content-Type", "application/json")
		resp, e := client.Do(req)
		require.NoError(t, e)
		defer resp.Body.Close()
		b, e := io.ReadAll(resp.Body)
		require.NoError(t, e)
		return resp.StatusCode, b
	}
	start := func() func() {
		cmd := exec.Command(exe, "-test.run=^TestExecutableProcess$")
		cmd.Env = append(os.Environ(), "WEBCHAT_TEST_API_PROCESS=1", "WEBCHAT_DATABASE_URL="+raw, "WEBCHAT_API_LISTEN="+address, "WEBCHAT_BROWSER_ORIGIN=http://localhost:3000", "WEBCHAT_PROXY_SECRET="+key, "WEBCHAT_EXECUTION_BACKEND=")
		log, e := os.CreateTemp(t.TempDir(), "api-*.log")
		require.NoError(t, e)
		t.Cleanup(func() { _ = log.Close() })
		cmd.Stdout = log
		cmd.Stderr = log
		require.NoError(t, cmd.Start())
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		stopped := false
		stop := func() {
			if stopped {
				return
			}
			stopped = true
			require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
			select {
			case e := <-done:
				require.NoError(t, e)
			case <-time.After(12 * time.Second):
				_ = cmd.Process.Kill()
				<-done
				t.Fatal("API shutdown timed out")
			}
		}
		t.Cleanup(func() {
			if !stopped {
				_ = cmd.Process.Kill()
				<-done
				stopped = true
			}
		})
		deadline := time.Now().Add(10 * time.Second)
		for {
			req, _ := http.NewRequest("GET", "http://"+address+"/healthz", nil)
			req.Header.Set("X-Webchat-Secret", key)
			resp, e := client.Do(req)
			if e == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == 200 {
					break
				}
			}
			if time.Now().After(deadline) {
				t.Fatal("API failed to become live; log:", log.Name())
			}
			time.Sleep(25 * time.Millisecond)
		}
		return stop
	}
	stop := start()
	code, b := call("GET", "/readyz", "")
	require.Equal(t, 503, code)
	require.Contains(t, string(b), `"dispatcher":"unavailable"`)
	require.Contains(t, string(b), `"acceptingCommands":true`)
	code, b = call("POST", "/conversations", fmt.Sprintf(`{"requestId":%q}`, uuid.NewString()))
	require.Equal(t, 201, code)
	var c struct {
		Conversation struct {
			ID string `json:"id"`
		} `json:"conversation"`
	}
	require.NoError(t, json.Unmarshal(b, &c))
	path := "/conversations/" + c.Conversation.ID + "/messages"
	body := fmt.Sprintf(`{"requestId":%q,"text":"survive real API process shutdown"}`, uuid.NewString())
	code, first := call("POST", path, body)
	require.Equal(t, 202, code)
	stop()
	stop = start()
	defer stop()
	code, b = call("POST", path, body)
	require.Equal(t, 202, code)
	require.JSONEq(t, string(first), string(b))
	code, b = call("GET", path, "")
	require.Equal(t, 200, code)
	var history struct {
		Messages []json.RawMessage `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(b, &history))
	require.Len(t, history.Messages, 1)
}
