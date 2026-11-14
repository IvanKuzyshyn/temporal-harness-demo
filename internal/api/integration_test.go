//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/api"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func database(t *testing.T) (*storage.Repository, *pgxpool.Pool) {
	t.Helper()
	raw := os.Getenv("WEBCHAT_TEST_DATABASE_URL")
	require.NotEmpty(t, raw)
	cfg, e := pgxpool.ParseConfig(raw)
	require.NoError(t, e)
	require.True(t, strings.HasPrefix(cfg.ConnConfig.Database, "webchat_test_"))
	require.Equal(t, "127.0.0.1", cfg.ConnConfig.Host)
	require.Equal(t, "webchat", cfg.ConnConfig.User)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, e := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, e)
	t.Cleanup(p.Close)
	require.NoError(t, p.Ping(ctx))
	return storage.New(p), p
}
func realHandler(t *testing.T, r *storage.Repository) http.Handler {
	h, e := api.NewServer(r, api.Config{ListenAddress: "127.0.0.1:18081", ProxySecret: secret, BrowserOrigin: "http://localhost:3000"})
	require.NoError(t, e)
	return h
}
func create(t *testing.T, h http.Handler) string {
	w := request(h, "POST", "/conversations", fmt.Sprintf(`{"requestId":%q}`, uuid.NewString()), nil)
	require.Equal(t, 201, w.Code, w.Body.String())
	var out struct {
		Conversation struct {
			ID    string `json:"id"`
			Model string `json:"model"`
		} `json:"conversation"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	require.Equal(t, "qwen3.5:9b", out.Conversation.Model)
	require.NotEmpty(t, out.Conversation.ID)
	return out.Conversation.ID
}
func receipt(t *testing.T, wbody []byte) chat.CommandReceipt {
	var a struct {
		CommandID   chat.CommandID `json:"commandId"`
		TurnID      chat.TurnID    `json:"turnId"`
		ResultState string         `json:"resultState"`
	}
	require.NoError(t, json.Unmarshal(wbody, &a))
	return chat.CommandReceipt{CommandID: a.CommandID, TurnID: a.TurnID, ResultState: a.ResultState}
}
func TestAcceptedCommandSurvivesAPIRestart(t *testing.T) {
	r, p := database(t)
	h := realHandler(t, r)
	c := create(t, h)
	path := "/conversations/" + c + "/messages"
	body := fmt.Sprintf(`{"requestId":%q,"text":"hello"}`, uuid.NewString())
	w := request(h, "POST", path, body, nil)
	require.Equal(t, 202, w.Code, w.Body.String())
	a := receipt(t, w.Body.Bytes())
	require.NotEmpty(t, a.CommandID)
	// Close every API DB connection, construct a fresh API/pool. No memory receipt survives.
	p.Close()
	r, _ = database(t)
	h = realHandler(t, r)
	w = request(h, "POST", path, body, nil)
	require.Equal(t, 202, w.Code)
	require.Equal(t, a, receipt(t, w.Body.Bytes()))
	w = request(h, "GET", path, "", nil)
	require.Equal(t, 200, w.Code)
	var history struct {
		Messages []json.RawMessage `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &history))
	require.Len(t, history.Messages, 1)
	w = request(h, "GET", "/conversations/"+c+"/commands/"+string(a.CommandID), "", nil)
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"resultState":"pending"`)
	require.NoError(t, r.RecordCommandResult(context.Background(), chat.ConversationID(c), a.CommandID, "dispatched", "rejected", []byte(`{"code":"stale_target"}`)))
	w = request(h, "GET", "/conversations/"+c+"/commands/"+string(a.CommandID), "", nil)
	require.Contains(t, w.Body.String(), `"resultState":"rejected"`)
	require.Equal(t, 503, request(h, "GET", "/readyz", "", nil).Code)
}
func TestChangedPayloadReturnsConflict(t *testing.T) {
	r, _ := database(t)
	h := realHandler(t, r)
	c := create(t, h)
	id := uuid.NewString()
	path := "/conversations/" + c + "/messages"
	require.Equal(t, 202, request(h, "POST", path, fmt.Sprintf(`{"requestId":%q,"text":"one"}`, id), nil).Code)
	require.Equal(t, 409, request(h, "POST", path, fmt.Sprintf(`{"requestId":%q,"text":"two"}`, id), nil).Code)
	require.Equal(t, 409, request(h, "POST", "/conversations/"+c+"/approval-policy/revoke", fmt.Sprintf(`{"requestId":%q}`, id), nil).Code)
}
func TestCreateIdempotencyAndPagination(t *testing.T) {
	r, _ := database(t)
	h := realHandler(t, r)
	id := uuid.NewString()
	body := fmt.Sprintf(`{"requestId":%q,"model":"qwen3.5:9b"}`, id)
	first := request(h, "POST", "/conversations", body, nil)
	require.Equal(t, 201, first.Code)
	again := request(h, "POST", "/conversations", body, nil)
	require.JSONEq(t, first.Body.String(), again.Body.String())
	require.Equal(t, 201, request(h, "POST", "/conversations", fmt.Sprintf(`{"requestId":%q}`, id), nil).Code)
	require.Equal(t, 400, request(h, "POST", "/conversations", fmt.Sprintf(`{"requestId":%q,"model":"gemma4:e4b"}`, id), nil).Code)
	ids := []string{create(t, h), create(t, h), create(t, h)}
	w := request(h, "GET", "/conversations?limit=2", "", nil)
	require.Equal(t, 200, w.Code, w.Body.String())
	var page struct {
		Conversations []struct {
			ID string `json:"id"`
		} `json:"conversations"`
		Cursor string `json:"cursor"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &page))
	require.Equal(t, ids[2], page.Conversations[0].ID)
	require.Equal(t, ids[1], page.Conversations[1].ID)
	require.NotEmpty(t, page.Cursor)
	// Move a not-yet-seen conversation to most recent after the snapshot; it must not disappear.
	require.Equal(t, 202, request(h, "POST", "/conversations/"+ids[0]+"/messages", fmt.Sprintf(`{"requestId":%q,"text":"new activity"}`, uuid.NewString()), nil).Code)
	w = request(h, "GET", "/conversations?limit=2&cursor="+page.Cursor, "", nil)
	require.Equal(t, 200, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &page))
	require.Equal(t, ids[0], page.Conversations[0].ID)
	for i := 0; i < 3; i++ {
		require.Equal(t, 202, request(h, "POST", "/conversations/"+ids[2]+"/messages", fmt.Sprintf(`{"requestId":%q,"text":%q}`, uuid.NewString(), fmt.Sprint(i)), nil).Code)
	}
	w = request(h, "GET", "/conversations/"+ids[2]+"/messages?limit=2", "", nil)
	var history struct {
		Messages []struct {
			Text string `json:"text"`
		} `json:"messages"`
		Before string `json:"before"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &history))
	require.Len(t, history.Messages, 2)
	require.Equal(t, "1", history.Messages[0].Text)
	require.Equal(t, "2", history.Messages[1].Text)
	w = request(h, "GET", "/conversations/"+ids[2]+"/messages?limit=2&before="+history.Before, "", nil)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &history))
	require.Len(t, history.Messages, 1)
	require.Equal(t, "0", history.Messages[0].Text)
	require.Equal(t, 400, request(h, "GET", "/conversations?cursor=bad", "", nil).Code)
	require.Equal(t, 404, request(h, "GET", "/conversations/"+uuid.NewString()+"/messages", "", nil).Code)
}
func TestCommandValidationAndStorageFailures(t *testing.T) {
	r, p := database(t)
	h := realHandler(t, r)
	c := create(t, h)
	base := "/conversations/" + c
	for _, tc := range []struct {
		suffix, body string
		code         int
	}{
		{"/messages", fmt.Sprintf(`{"requestId":%q,"text":%q}`, uuid.NewString(), strings.Repeat("x", 65537)), 413},
		{"/messages", fmt.Sprintf(`{"requestId":%q,"text":"x","attachmentIds":[%q]}`, uuid.NewString(), uuid.NewString()), 404},
		{"/messages", fmt.Sprintf(`{"requestId":%q,"text":"x","expectedActiveTurnId":%q}`, uuid.NewString(), uuid.NewString()), 400},
		{"/stop", fmt.Sprintf(`{"requestId":%q,"expectedActiveTurnId":%q}`, uuid.NewString(), uuid.NewString()), 404},
		{"/approval-policy/revoke", fmt.Sprintf(`{"requestId":%q}`, uuid.NewString()), 202},
	} {
		w := request(h, "POST", base+tc.suffix, tc.body, nil)
		require.Equal(t, tc.code, w.Code, w.Body.String())
	}
	require.Equal(t, 400, request(h, "GET", base+"/messages?limit=201", "", nil).Code)
	p.Close()
	w := request(h, "POST", "/conversations", fmt.Sprintf(`{"requestId":%q}`, uuid.NewString()), nil)
	require.Equal(t, 503, w.Code)
	require.NotContains(t, w.Body.String(), "pool")
}
