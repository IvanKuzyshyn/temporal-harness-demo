package mock

import (
	"context"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"testing"
)

func TestMockDoesNotProposeRetiredCodingTools(t *testing.T) {
	for _, scenario := range []string{"demo", "parallel", "write", "command", "delegate"} {
		response, err := New().Chat(context.Background(), llm.Request{Scenario: scenario}, nil)
		if err != nil || len(response.Message.ToolCalls) != 0 {
			t.Fatalf("retired scenario %s: %+v %v", scenario, response, err)
		}
	}
}
