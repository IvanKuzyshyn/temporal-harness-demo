//go:build integration

package webintegration_test

import (
	"bufio"
	"context"
	"encoding/json"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/api"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type streamModel struct{}

func (streamModel) Chat(ctx context.Context, _ llm.Request, emit func(llm.Delta) error) (llm.Response, error) {
	text := ""
	for i := 0; i < 20; i++ {
		select {
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		case <-time.After(30 * time.Millisecond):
		}
		text += "hello "
		if e := emit(llm.Delta{Text: "hello "}); e != nil {
			return llm.Response{}, e
		}
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: text}}, nil
}
func TestStreamDisconnectAndSnapshotRecovery(t *testing.T) {
	f := setup(t)
	f.start(t, f.repo, streamModel{})
	c := f.conversation(t, "qwen3.5:9b")
	cmd := f.command(t, c, "hello")
	srv := httptest.NewUnstartedServer(nil)
	secret := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	h, err := api.NewServer(f.repo, api.Config{ListenAddress: srv.Listener.Addr().String(), ProxySecret: secret, BrowserOrigin: "http://localhost:3000"})
	require.NoError(t, err)
	srv.Config.Handler = h
	srv.Start()
	defer srv.Close()
	open := func() (*http.Response, *bufio.Scanner, context.CancelFunc) {
		ctx, cancel := context.WithTimeout(f.ctx, 8*time.Second)
		req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/conversations/"+string(c.ID)+"/events", nil)
		req.Header.Set("X-Webchat-Secret", secret)
		res, e := srv.Client().Do(req)
		require.NoError(t, e)
		require.Equal(t, 200, res.StatusCode)
		s := bufio.NewScanner(res.Body)
		s.Buffer(make([]byte, 4096), 8<<20)
		return res, s, cancel
	}
	res, s, cancel := open()
	_, err = f.gateway.Submit(f.ctx, cmd)
	require.NoError(t, err)
	found := false
	for s.Scan() {
		line := s.Text()
		if strings.HasPrefix(line, "data: ") {
			var ev struct {
				Type                   string
				ConversationID, TurnID string
				StepID, Attempt        *int
				Data                   json.RawMessage
			}
			require.NoError(t, json.Unmarshal([]byte(line[6:]), &ev))
			if ev.Type == "message.delta" {
				require.Equal(t, string(c.ID), ev.ConversationID)
				require.Equal(t, string(cmd.TurnID), ev.TurnID)
				require.NotNil(t, ev.StepID)
				require.Equal(t, 1, *ev.Attempt)
				found = true
				break
			}
		}
	}
	require.True(t, found)
	cancel()
	res.Body.Close()
	f.completed(t, cmd) // HTTP disconnect never cancelled accepted execution.
	// Missing token events cannot erase canonical history or invent duplicates.
	_, err = f.pool.Exec(f.ctx, "DELETE FROM events WHERE conversation_id=$1", c.ID)
	require.NoError(t, err)
	res, s, cancel = open()
	defer cancel()
	defer res.Body.Close()
	found = false
	for s.Scan() {
		if strings.HasPrefix(s.Text(), "data: ") {
			var ev struct {
				Type string
				Data struct {
					Reset    bool
					Messages []struct{ EntryID, Role, Text string }
				}
			}
			require.NoError(t, json.Unmarshal([]byte(s.Text()[6:]), &ev))
			if ev.Type == "snapshot" {
				require.True(t, ev.Data.Reset)
				require.Len(t, ev.Data.Messages, 2)
				require.Equal(t, "assistant", ev.Data.Messages[1].Role)
				require.Equal(t, strings.Repeat("hello ", 20), ev.Data.Messages[1].Text)
				found = true
				break
			}
		}
	}
	require.True(t, found)
}
