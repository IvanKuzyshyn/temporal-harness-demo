//go:build integration

package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func openEvents(t *testing.T, h http.Handler, c, cursor string) (*http.Response, context.CancelFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/conversations/"+c+"/events", nil)
	req.Host = "127.0.0.1:18081"
	req.Header.Set("X-Webchat-Secret", secret)
	if cursor != "" {
		req.Header.Set("Last-Event-ID", cursor)
	}
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { cancel(); res.Body.Close() })
	return res, cancel
}
func nextEvent(t *testing.T, s *bufio.Scanner) map[string]any {
	t.Helper()
	for s.Scan() {
		if strings.HasPrefix(s.Text(), "data: ") {
			var e map[string]any
			require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(s.Text(), "data: ")), &e))
			return e
		}
	}
	t.Fatalf("no SSE event: %v", s.Err())
	return nil
}
func TestSSECursorResumeAndReset(t *testing.T) {
	r, p := database(t)
	h := realHandler(t, r)
	c := create(t, h)
	ctx := context.Background()
	// No display events: canonical accepted prompt MUST recover from SQL.
	w := request(h, "POST", "/conversations/"+c+"/messages", fmt.Sprintf(`{"requestId":%q,"text":"durable without tokens"}`, uuid.NewString()), nil)
	require.Equal(t, 202, w.Code)
	res, cancel := openEvents(t, h, c, "")
	require.Equal(t, 200, res.StatusCode)
	require.Contains(t, res.Header.Get("Content-Type"), "text/event-stream")
	s := bufio.NewScanner(res.Body)
	s.Buffer(make([]byte, 4096), 8<<20)
	e := nextEvent(t, s)
	require.Equal(t, "snapshot", e["type"])
	require.Contains(t, fmt.Sprint(e["data"]), "durable without tokens")
	w = request(h, "POST", "/conversations/"+c+"/messages", fmt.Sprintf(`{"requestId":%q,"text":"next lifecycle"}`, uuid.NewString()), nil)
	require.Equal(t, 202, w.Code)
	seen := map[string]bool{}
	for len(seen) < 3 {
		e = nextEvent(t, s)
		if e["type"] == "message.persisted" || e["type"] == "command.result" || e["type"] == "turn.status" {
			seen[e["type"].(string)] = true
		}
	}
	cancel()
	res.Body.Close()
	require.NoError(t, r.AppendEvent(ctx, chat.Event{ID: uuid.NewString(), ConversationID: chat.ConversationID(c), Type: "test.first", Payload: []byte(`{}`)}))
	ev, err := r.ListEvents(ctx, chat.ConversationID(c), 0, 10)
	require.NoError(t, err)
	seq := ev[0].Sequence
	require.NoError(t, r.AppendEvent(ctx, chat.Event{ID: uuid.NewString(), ConversationID: chat.ConversationID(c), Type: "test.next", Payload: []byte(`{}`)}))
	res, cancel = openEvents(t, h, c, fmt.Sprint(seq))
	require.Equal(t, 200, res.StatusCode)
	s = bufio.NewScanner(res.Body)
	s.Buffer(make([]byte, 4096), 8<<20)
	e = nextEvent(t, s)
	require.Equal(t, "test.next", e["type"])
	require.Equal(t, c, e["conversationId"])
	cancel()
	res.Body.Close()
	// Retention loss is explicit, never silently treated as a successful replay.
	_, err = p.Exec(ctx, "DELETE FROM events WHERE conversation_id=$1 AND sequence=$2", c, seq)
	require.NoError(t, err)
	res, cancel = openEvents(t, h, c, fmt.Sprint(seq))
	s = bufio.NewScanner(res.Body)
	s.Buffer(make([]byte, 4096), 8<<20)
	e = nextEvent(t, s)
	require.Equal(t, "snapshot", e["type"])
	require.Equal(t, true, e["data"].(map[string]any)["reset"])
	cancel()
	res.Body.Close()
	res, cancel = openEvents(t, h, c, "garbage")
	require.Equal(t, 400, res.StatusCode)
	cancel()
}
func TestPagedHistoryRetainsFailureStatus(t *testing.T) {
	r, p := database(t)
	h := realHandler(t, r)
	c := create(t, h)
	w := request(h, "POST", "/conversations/"+c+"/messages", fmt.Sprintf(`{"requestId":%q,"text":"failed history"}`, uuid.NewString()), nil)
	require.Equal(t, 202, w.Code)
	rc := receipt(t, w.Body.Bytes())
	_, err := p.Exec(context.Background(), "UPDATE turns SET status='failed',reason='provider_unavailable' WHERE id=$1", rc.TurnID)
	require.NoError(t, err)
	w = request(h, "GET", "/conversations/"+c+"/messages", "", nil)
	require.Equal(t, 200, w.Code)
	var page struct {
		Messages []struct{ TurnStatus, TurnReason string }
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &page))
	require.Len(t, page.Messages, 1)
	require.Equal(t, "failed", page.Messages[0].TurnStatus)
	require.Equal(t, "provider_unavailable", page.Messages[0].TurnReason)
}
func TestDisplayEventRetentionDoesNotRemoveHistory(t *testing.T) {
	r, p := database(t)
	h := realHandler(t, r)
	c := create(t, h)
	w := request(h, "POST", "/conversations/"+c+"/messages", fmt.Sprintf(`{"requestId":%q,"text":"canonical"}`, uuid.NewString()), nil)
	require.Equal(t, 202, w.Code)
	rc := receipt(t, w.Body.Bytes())
	require.NoError(t, r.RecordModelAttempt(context.Background(), chat.ModelAttempt{ID: uuid.NewString(), ConversationID: chat.ConversationID(c), TurnID: rc.TurnID, Attempt: 1, Status: "running"}))
	for i := 0; i < 140; i++ {
		require.NoError(t, r.AppendEvent(context.Background(), chat.Event{ID: uuid.NewString(), ConversationID: chat.ConversationID(c), TurnID: rc.TurnID, Attempt: 1, Type: "message.delta", Payload: []byte(`{"text":"preview"}`)}))
	}
	var count int
	require.NoError(t, p.QueryRow(context.Background(), "SELECT count(*) FROM events WHERE conversation_id=$1 AND type='message.delta'", c).Scan(&count))
	require.LessOrEqual(t, count, 128)
	page, e := r.ListMessages(context.Background(), chat.ConversationID(c), "", 20)
	require.NoError(t, e)
	require.Len(t, page.Messages, 1)
	require.Equal(t, "canonical", page.Messages[0].Text)
}
func TestSlowClientDoesNotBlockWorker(t *testing.T) {
	r, _ := database(t)
	h := realHandler(t, r)
	c := create(t, h)
	res, cancel := openEvents(t, h, c, "")
	require.Equal(t, 200, res.StatusCode)
	// Do not consume the stream. Writes use independent SQL transactions, not a
	// subscriber channel or HTTP context; close never submits a stop command.
	ctx, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	for i := 0; i < 80; i++ {
		require.NoError(t, r.AppendEvent(ctx, chat.Event{ID: uuid.NewString(), ConversationID: chat.ConversationID(c), Type: "test.large", Payload: []byte(`{"text":"` + strings.Repeat("x", 60000) + `"}`)}))
	}
	cancel()
	res.Body.Close()
	w := request(h, "POST", "/conversations/"+c+"/messages", fmt.Sprintf(`{"requestId":%q,"text":"still accepted"}`, uuid.NewString()), nil)
	require.Equal(t, 202, w.Code)
}
