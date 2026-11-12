//go:build integration

package webintegration_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/ollama"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/worker"
	"github.com/stretchr/testify/require"
)

// A separate, explicitly fault-injected probe. The unchanged main live fixture
// uses the real model for every call; this regression forces the assistant-only
// refusal seen in the retained failure, then uses real summaries and final recall.
type refusalAttributionProbe struct {
	used atomic.Bool
	live *contextLiveModel
}

func (p *refusalAttributionProbe) Chat(ctx context.Context, req llm.Request, emit func(llm.Delta) error) (llm.Response, error) {
	if p.used.CompareAndSwap(false, true) {
		text := "I cannot remember or store sensitive personal information like secret phrases. Please let me know how else I can assist you today."
		e := p.live.record(map[string]any{"kind": "injected_assistant_refusal", "synthetic": true, "model": req.Model, "request": req, "output": text, "reason": "replay captured assistant stance, not a human correction; not a provider transport call"})
		return llm.Response{Message: llm.Message{Role: "assistant", Content: text}}, e
	}
	return p.live.Chat(ctx, req, emit)
}
func TestContextLiveSummaryAttribution(t *testing.T) {
	if os.Getenv("WEBCHAT_TASK10_LIVE") != "1" {
		t.Skip("opt-in exact live models")
	}
	path := os.Getenv("WEBCHAT_TASK10_EVIDENCE")
	require.NotEmpty(t, path)
	out, e := os.OpenFile(path+".attribution.jsonl", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	require.NoError(t, e)
	defer out.Close()
	for _, model := range []string{"qwen3.5:9b", "gemma4:e4b"} {
		t.Run(model, func(t *testing.T) {
			f := setup(t)
			real, e := ollama.New("http://127.0.0.1:11434")
			require.NoError(t, e)
			live := &contextLiveModel{base: real, file: out}
			runner, e := worker.StartWeb(f.ctx, f.address, &activities.ChatActivities{Repository: f.repo, Model: &refusalAttributionProbe{live: live}})
			require.NoError(t, e)
			defer runner.Close()
			c := f.conversation(t, model)
			send := func(text string) {
				cmd := f.command(t, c, text)
				_, e = f.gateway.Submit(f.ctx, cmd)
				require.NoError(t, e)
				f.completed(t, cmd)
			}
			send("My secret phrase is blue kite. Remember it. Reply only Noted. Do not use tools.")
			for i := 0; i < 7; i++ {
				send(fmt.Sprintf("Filler %d. Reply only OK. Do not use tools.", i))
			}
			send("What secret phrase did I state earlier in this conversation? Answer from available memory. Do not use tools.")
			page, e := f.repo.ListMessages(f.ctx, c.ID, "", 1)
			require.NoError(t, e)
			require.Len(t, page.Messages, 1)
			s, e := f.repo.LatestSummary(f.ctx, c.ID)
			require.NoError(t, e)
			require.NotNil(t, s)
			accepted := strings.Contains(strings.ToLower(page.Messages[0].Text), "blue kite")
			require.NoError(t, live.record(map[string]any{"kind": "user_fact_attribution_after_injected_refusal", "model": model, "answer": page.Messages[0].Text, "summary": s, "context": page.Messages[0].Context, "semanticAccepted": accepted, "scope": "initial assistant refusal is synthetic; all subsequent summary and conversation calls are real and registered"}))
			require.True(t, accepted, "assistant refusal must not erase an explicitly supplied user fact")
		})
	}
}
