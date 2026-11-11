package workflows

import (
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
)

func TestBeforeToolPolicy(t *testing.T) {
	safe := PreflightResult{Allowed: true, Mode: tools.Parallel, Replay: tools.Safe, ActionHash: "opaque-nonempty-hash"}
	unsafe := PreflightResult{Allowed: true, Mode: tools.Sequential, Replay: tools.Unsafe, ActionHash: "opaque-nonempty-hash"}
	with := func(p PreflightResult, change func(*PreflightResult)) PreflightResult {
		change(&p)
		return p
	}
	tests := []struct {
		name      string
		tool      string
		preflight PreflightResult
		want      toolPolicyDecision
	}{
		{"allowed safe", "read_file", safe, toolPolicyDecision{Allowed: true}},
		{"allowed sequential safe", "delegate", with(safe, func(p *PreflightResult) { p.Mode = tools.Sequential }), toolPolicyDecision{Allowed: true}},
		{"unknown", "unknown", safe, toolPolicyDecision{Reason: "unknown tool"}},
		{"unknown precedes preflight denial", "unknown", PreflightResult{Reason: "denied"}, toolPolicyDecision{Reason: "unknown tool"}},
		{"denied precedes metadata and approval", "read_file", PreflightResult{Reason: "path denied", RequiresApproval: true}, toolPolicyDecision{Reason: "path denied"}},
		{"denied empty reason preserved", "read_file", PreflightResult{}, toolPolicyDecision{}},
		{"mismatched mode", "read_file", with(safe, func(p *PreflightResult) { p.Mode = tools.Sequential }), toolPolicyDecision{Reason: "invalid preflight metadata"}},
		{"mismatched replay", "read_file", with(safe, func(p *PreflightResult) { p.Replay = tools.Unsafe }), toolPolicyDecision{Reason: "invalid preflight metadata"}},
		{"missing hash", "read_file", with(safe, func(p *PreflightResult) { p.ActionHash = "" }), toolPolicyDecision{Reason: "invalid preflight metadata"}},
		{"unsafe write always approval", "write_file", unsafe, toolPolicyDecision{Allowed: true, RequiresApproval: true}},
		{"unsafe command always approval", "run_command", unsafe, toolPolicyDecision{Allowed: true, RequiresApproval: true}},
		{"preflight requires approval", "read_file", with(safe, func(p *PreflightResult) { p.RequiresApproval = true }), toolPolicyDecision{Allowed: true, RequiresApproval: true}},
		{"metadata rejection precedes approval", "write_file", with(unsafe, func(p *PreflightResult) { p.ActionHash = ""; p.RequiresApproval = true }), toolPolicyDecision{Reason: "invalid preflight metadata"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			call := llm.ToolCall{ID: "call", Name: tt.tool}
			for range 2 {
				if got := beforeTool(call, tt.preflight); got != tt.want {
					t.Fatalf("beforeTool() = %+v, want %+v", got, tt.want)
				}
			}
		})
	}
}
