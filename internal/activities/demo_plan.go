package activities

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

// Exact bundled fixture, not a template for overwriting arbitrary user code.
// Preserve its comments and formatting; also accept the former mock's fixed file.
const demoBrokenFixture = `package calc

// TODO: Fix Add to return the sum instead of the difference.
// TODO: Add coverage for negative operands once the intentional bug is fixed.
func Add(a, b int) int {
	return a - b
}
`
const demoLegacyFixedFixture = "package calc\n\nfunc Add(a, b int) int { return a + b }\n"
const demoLabel = "Forced demo plan (harness-enforced, not provider-selected tools). "

func demoCallID(in workflows.LLMInput, turn, index int) string {
	// Stable activity retry identity, distinct across logical runs and child scopes.
	return fmt.Sprintf("demo-%s-%d-%d", entryKey(in.Scope, in.InputID)[:16], turn, index)
}

func demoToolMessage(in workflows.LLMInput, entries []transcriptEntry, turn, index int, name string) (llm.Message, bool) {
	id := demoCallID(in, turn, index)
	for i := len(entries) - 1; i >= 0; i-- {
		m := entries[i].Message
		if m.Role == "tool" && m.ToolCallID == id && m.ToolName == name {
			return m, true
		}
	}
	return llm.Message{}, false
}

func demoOutcome(in workflows.LLMInput, entries []transcriptEntry, turn int, name string) (tools.Result, bool) {
	m, ok := demoToolMessage(in, entries, turn, 0, name)
	var result tools.Result
	if !ok || json.Unmarshal([]byte(m.Content), &result) != nil || result.ToolCallID != m.ToolCallID {
		return result, false
	}
	return result, true
}

func demoStatus(in workflows.LLMInput, entries []transcriptEntry, turn int, name, success string) string {
	result, ok := demoOutcome(in, entries, turn, name)
	if !ok {
		return name + ": no confirmed result"
	}
	if result.Interrupted {
		return name + ": interrupted; outcome unconfirmed"
	}
	if result.IsError {
		status := name + ": failed or denied"
		if name == "run_command" && result.Details["exitCode"] != "" {
			status = "go test ./...: failed (exitCode=" + bounded(result.Details["exitCode"], 32) + ")"
		}
		return status + ". " + bounded(result.Output, 1024)
	}
	if name == "run_command" && result.Details["exitCode"] != "0" {
		return "go test ./...: not verified successful (exitCode=" + bounded(result.Details["exitCode"], 32) + ")"
	}
	return success
}

// demoPlan consumes only activity-owned persisted evidence, never provider prose.
// It replaces ALL provider calls before model ID/payload validation. The usual
// workflow preflight, approval, child workflow and tool activities still execute.
func demoPlan(in workflows.LLMInput, entries []transcriptEntry) llm.Message {
	out := llm.Message{Role: "assistant", Content: demoLabel}
	add := func(name string, args any) {
		raw, _ := json.Marshal(args)
		out.ToolCalls = append(out.ToolCalls, llm.ToolCall{ID: demoCallID(in, in.Turn, len(out.ToolCalls)), Name: name, Args: raw})
	}
	switch in.Turn {
	case 0:
		out.Content += "Read calc/calc.go and calc/calc_test.go in parallel."
		add("read_file", tools.FileArgs{Path: "calc/calc.go"})
		add("read_file", tools.FileArgs{Path: "calc/calc_test.go"})
	case 1:
		out.Content += "Delegate a read-only review to an actual explorer child."
		add("delegate", tools.DelegateArgs{Path: "./calc", Question: "Inspect Add and its tests; explain the correction without modifying files."})
	case 2:
		read, ok := demoToolMessage(in, entries, 0, 0, "read_file")
		fixed := strings.Replace(demoBrokenFixture, "return a - b", "return a + b", 1)
		if !ok || (read.Content != demoBrokenFixture && read.Content != fixed && read.Content != demoLegacyFixedFixture) {
			out.Content += "Stopped: calc/calc.go is not the exact recognized teaching fixture (or its read failed). No write proposed; tests not run by this plan. Unrelated content will not be overwritten."
			break
		}
		content := read.Content
		if content == demoBrokenFixture {
			content = fixed
			out.Content += "Propose correcting Add to a + b, preserving all other content, subject to approval."
		} else {
			out.Content += "Fixture is already fixed. Propose an explicit no-op write (unchanged content), subject to approval."
		}
		add("write_file", tools.FileArgs{Path: "calc/calc.go", Content: content})
	case 3:
		out.Content += "Request approval to run go test ./...; no test success is assumed."
		add("run_command", tools.CommandArgs{Cmd: "go", Args: []string{"test", "./..."}})
	default:
		out.Content += "Authoritative tool-result summary: " +
			demoStatus(in, entries, 1, "delegate", "explorer child completed") + "; " +
			demoStatus(in, entries, 2, "write_file", "write applied (an already-fixed fixture uses an approved no-op)") + "; " +
			demoStatus(in, entries, 3, "run_command", "go test ./...: tests passed (exitCode=0)") + ". Provider stream was draft commentary, not verification evidence."
	}
	return out
}
