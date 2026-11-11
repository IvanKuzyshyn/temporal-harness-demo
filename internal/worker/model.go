package worker

import (
	"context"
	"strings"
	"sync"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/config"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/mock"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/ollama"
)

// Every worker serves shared queues, not just its launching client's session.
// Dispatch using the original workflow's model in each activity request; never
// substitute a provider or model based on the reconnecting client's selection.
func selectModel(cfg config.Config) llm.Model {
	return newRequestModel(cfg, mock.New(), func(host string) (llm.Model, error) { return ollama.New(host) })
}

type requestModel struct {
	mock   llm.Model
	ollama func() (llm.Model, error)
}

func newRequestModel(cfg config.Config, mock llm.Model, buildOllama func(string) (llm.Model, error)) llm.Model {
	host := strings.TrimSpace(cfg.OllamaHost)
	if host == "" {
		host = "127.0.0.1:11434"
	}
	if !strings.Contains(host, "://") {
		host = "http://" + host
	}
	return &requestModel{mock: mock, ollama: sync.OnceValues(func() (llm.Model, error) {
		// Mock-only operation needs neither a valid endpoint nor a running Ollama.
		// Cache construction (including errors) safely for concurrent activity calls.
		return buildOllama(host)
	})}
}

func (m *requestModel) Chat(ctx context.Context, req llm.Request, emit func(llm.Delta) error) (llm.Response, error) {
	if req.Model == "mock" {
		return m.mock.Chat(ctx, req, emit)
	}
	model, err := m.ollama()
	if err != nil {
		return llm.Response{}, err
	}
	return model.Chat(ctx, req, emit)
}
