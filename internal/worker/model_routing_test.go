package worker

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/config"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
)

type recordingModel struct {
	requests []llm.Request
	err      error
}

func (m *recordingModel) Chat(_ context.Context, req llm.Request, emit func(llm.Delta) error) (llm.Response, error) {
	m.requests = append(m.requests, req)
	if m.err != nil {
		return llm.Response{}, m.err
	}
	if err := emit(llm.Delta{Text: req.Model}); err != nil {
		return llm.Response{}, err
	}
	return llm.Response{Message: llm.Message{Content: req.Model}}, nil
}

func TestRequestAuthoritativeProvider(t *testing.T) {
	for _, selected := range []string{"mock", "client-real"} {
		t.Run(selected, func(t *testing.T) {
			mock, real := &recordingModel{}, &recordingModel{}
			builds := 0
			model := newRequestModel(config.Config{Model: selected, OllamaHost: "http://explicit:11435"}, mock, func(host string) (llm.Model, error) {
				builds++
				if host != "http://explicit:11435" {
					t.Fatalf("host substituted: %q", host)
				}
				return real, nil
			})
			for _, original := range []string{"mock", "original-real:tag", "mock", "Mock", " mock ", ""} {
				req := llm.Request{Model: original, Scenario: "parallel", Turn: 3, Messages: []llm.Message{{Role: "user", Content: "original"}}}
				var delta string
				res, err := model.Chat(context.Background(), req, func(d llm.Delta) error { delta += d.Text; return nil })
				if err != nil || res.Message.Content != original || delta != original {
					t.Fatalf("response=%+v delta=%q err=%v", res, delta, err)
				}
				target := real
				if original == "mock" {
					target = mock
				}
				if len(target.requests) == 0 || !reflect.DeepEqual(target.requests[len(target.requests)-1], req) {
					t.Fatalf("wrong provider or changed request: mock=%+v real=%+v", mock.requests, real.requests)
				}
			}
			if builds != 1 || len(mock.requests) != 2 || len(real.requests) != 4 {
				t.Fatalf("builds=%d mock=%d real=%d", builds, len(mock.requests), len(real.requests))
			}
			boom := errors.New("provider failed")
			for _, original := range []string{"mock", "original-real"} {
				mock.err, real.err = boom, boom
				before := len(mock.requests) + len(real.requests)
				_, err := model.Chat(context.Background(), llm.Request{Model: original}, nil)
				if !errors.Is(err, boom) || len(mock.requests)+len(real.requests) != before+1 {
					t.Fatalf("fallback or lost error: %v", err)
				}
			}
		})
	}
}

func TestRequestProviderLazyConstructionFailure(t *testing.T) {
	mock := &recordingModel{}
	boom := errors.New("invalid explicit endpoint")
	builds := 0
	model := newRequestModel(config.Config{Model: "client-real", OllamaHost: "ftp://bad"}, mock, func(host string) (llm.Model, error) {
		builds++
		if host != "ftp://bad" {
			t.Fatal(host)
		}
		return nil, boom
	})
	if _, err := model.Chat(context.Background(), llm.Request{Model: "mock"}, func(llm.Delta) error { return nil }); err != nil || builds != 0 {
		t.Fatalf("mock constructed Ollama: %v builds=%d", err, builds)
	}
	for i := 0; i < 2; i++ {
		if _, err := model.Chat(context.Background(), llm.Request{Model: "original-real"}, nil); !errors.Is(err, boom) {
			t.Fatalf("lost construction error: %v", err)
		}
	}
	if builds != 1 || len(mock.requests) != 1 {
		t.Fatal("construction retried or silently fell back to mock")
	}
}
