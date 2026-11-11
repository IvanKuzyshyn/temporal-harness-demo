package activities

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/ollama"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"go.temporal.io/sdk/temporal"
)

func TestDemoPlanOverridesProviderBeforeValidation(t *testing.T) {
	for _, unsafe := range []bool{false, true} {
		t.Run(fmt.Sprint(unsafe), func(t *testing.T) {
			calls := 0
			a := &Activities{Model: modelFunc(func(_ context.Context, req llm.Request, emit func(llm.Delta) error) (llm.Response, error) {
				calls++
				if err := emit(llm.Delta{Text: "Provider commentary 界"}); err != nil {
					return llm.Response{}, err
				}
				m := llm.Message{Content: strings.Repeat("界", 10000)}
				if unsafe {
					for range 40 {
						m.ToolCalls = append(m.ToolCalls, llm.ToolCall{Name: "run_command", Args: json.RawMessage(`{"cmd":"rm","args":["-rf","/"]}`)})
					}
				}
				return llm.Response{Message: m}, nil
			})}
			in, dir := modelFixture(t, a)
			in.Scenario = "demo"
			for turn, want := range []string{"read_file", "delegate", "write_file", "run_command", ""} {
				in.Turn = turn
				got, err := a.LLMStream(context.Background(), in)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(got.Summary, "Forced demo plan") || len(got.Summary) > MaxSummaryBytes || !utf8.ValidString(got.Summary) {
					t.Fatalf("summary=%q", got.Summary)
				}
				before := calls
				again, err := a.LLMStream(context.Background(), in)
				if err != nil || calls != before || fmt.Sprint(got.ToolCalls) != fmt.Sprint(again.ToolCalls) {
					t.Fatal("retry did not reuse committed response", err)
				}
				if want == "" {
					if len(got.ToolCalls) != 0 || !strings.Contains(got.Summary, "tests passed") || !strings.Contains(got.Summary, "write applied") {
						t.Fatalf("final=%+v", got)
					}
					continue
				}
				if len(got.ToolCalls) == 0 || got.ToolCalls[0].Name != want {
					t.Fatalf("turn %d: %+v", turn, got)
				}
				if turn == 0 && len(got.ToolCalls) != 2 {
					t.Fatal("missing parallel reads")
				}
				ids := map[string]bool{}
				var messages []llm.Message
				for i, call := range got.ToolCalls {
					if call.ID == "" || ids[call.ID] {
						t.Fatal("invalid IDs")
					}
					ids[call.ID] = true
					output := "ok"
					if turn == 0 {
						output = "package calc\n"
						if i == 0 {
							output = demoBrokenFixture
						}
					}
					if turn == 2 {
						var args tools.FileArgs
						_ = json.Unmarshal(call.Args, &args)
						if args.Path != "calc/calc.go" || args.Content != strings.Replace(demoBrokenFixture, "return a - b", "return a + b", 1) {
							t.Fatalf("unsafe write: %+v", args)
						}
					}
					if turn > 0 {
						b, _ := json.Marshal(tools.Result{ToolCallID: call.ID, Output: output, Details: map[string]string{"exitCode": "0"}})
						output = string(b)
					}
					messages = append(messages, llm.Message{Role: "tool", ToolCallID: call.ID, ToolName: call.Name, Content: output})
				}
				_, err = a.AppendTranscript(context.Background(), workflows.AppendInput{Scope: in.Scope, DataDir: dir, TranscriptRef: in.TranscriptRef, EntryID: fmt.Sprintf("%s/turn/%d/tools", in.InputID, turn), Messages: messages})
				if err != nil {
					t.Fatal(err)
				}
			}
			if calls != 5 {
				t.Fatalf("provider calls=%d", calls)
			}
		})
	}
}

func TestDemoFixtureAndTruthfulResults(t *testing.T) {
	in := workflows.LLMInput{InputID: "one", Turn: 2}
	for _, content := range []string{demoBrokenFixture, strings.Replace(demoBrokenFixture, "return a - b", "return a + b", 1), demoLegacyFixedFixture, "package calc\n// unrelated\n", demoBrokenFixture + "// custom\n", ""} {
		entries := []transcriptEntry{{Message: llm.Message{Role: "tool", ToolCallID: demoCallID(in, 0, 0), ToolName: "read_file", Content: content}}}
		got := demoPlan(in, entries)
		recognized := content == demoBrokenFixture || content == strings.Replace(demoBrokenFixture, "return a - b", "return a + b", 1) || content == demoLegacyFixedFixture
		if (len(got.ToolCalls) > 0) != recognized {
			t.Fatalf("fixture=%q plan=%+v", content, got)
		}
	}
	in.Turn = 4
	for _, res := range []tools.Result{{IsError: true, Output: "tool approval denied"}, {IsError: true, Details: map[string]string{"exitCode": "1"}}, {Interrupted: true, Details: map[string]string{"exitCode": "0"}}, {}, {Details: map[string]string{"exitCode": "0"}}} {
		res.ToolCallID = demoCallID(in, 3, 0)
		raw, _ := json.Marshal(res)
		entries := []transcriptEntry{{Message: llm.Message{Role: "tool", ToolName: "run_command", ToolCallID: res.ToolCallID, Content: string(raw)}}}
		got := demoPlan(in, entries)
		passed := !res.IsError && !res.Interrupted && res.Details["exitCode"] == "0"
		if strings.Contains(got.Content, "tests passed") != passed || strings.Contains(got.Content, "write applied") {
			t.Fatalf("false summary: %+v -> %s", res, got.Content)
		}
	}
	// Old-run evidence cannot authorize a write or claim success in the next run.
	old := in
	old.InputID = "old"
	in.Turn = 2
	got := demoPlan(in, []transcriptEntry{{Message: llm.Message{Role: "tool", ToolCallID: demoCallID(old, 0, 0), ToolName: "read_file", Content: demoBrokenFixture}}})
	if len(got.ToolCalls) != 0 {
		t.Fatal("reused stale read")
	}
}

func TestDemoCancellationAndTypedErrors(t *testing.T) {
	for _, kind := range []string{"cancel-return-error", "cancel-return-success", "ModelNotFound", "InvalidRequest"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a := &Activities{Model: modelFunc(func(_ context.Context, _ llm.Request, emit func(llm.Delta) error) (llm.Response, error) {
				if err := emit(llm.Delta{Text: "partial commentary"}); err != nil {
					return llm.Response{}, err
				}
				switch kind {
				case "ModelNotFound":
					return llm.Response{}, &ollama.ModelNotFoundError{Err: errors.New("missing")}
				case "InvalidRequest":
					return llm.Response{}, &ollama.InvalidRequestError{Err: errors.New("bad")}
				case "cancel-return-error":
					cancel()
					return llm.Response{}, ctx.Err()
				default:
					cancel()
					return llm.Response{}, nil
				}
			})}
			in, dir := modelFixture(t, a)
			in.Scenario = "demo"
			_, err := a.LLMStream(ctx, in)
			if strings.HasPrefix(kind, "cancel") {
				if !temporal.IsCanceledError(err) {
					t.Fatalf("cancellation lost: %v", err)
				}
			} else {
				var app *temporal.ApplicationError
				if !errors.As(err, &app) || !app.NonRetryable() || app.Type() != kind {
					t.Fatalf("typed error lost: %v", err)
				}
			}
			messages, err := ReadTranscript(dir, in.TranscriptRef)
			if err != nil || len(messages) != 1 {
				t.Fatal("failed response committed", messages, err)
			}
		})
	}
}

func TestDemoProviderFailureNeverFabricatesPlan(t *testing.T) {
	sentinel := errors.New("transport unavailable")
	a := &Activities{Model: modelFunc(func(context.Context, llm.Request, func(llm.Delta) error) (llm.Response, error) {
		return llm.Response{}, sentinel
	})}
	in, dir := modelFixture(t, a)
	in.Scenario = "demo"
	if _, err := a.LLMStream(context.Background(), in); !errors.Is(err, sentinel) {
		t.Fatalf("error=%v", err)
	}
	messages, err := ReadTranscript(dir, in.TranscriptRef)
	if err != nil || len(messages) != 1 {
		t.Fatalf("fabricated response: %v %v", messages, err)
	}
}
