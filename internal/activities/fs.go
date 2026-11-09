package activities

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

func toolBound(s string) string {
	if len(s) > 8192 {
		s = s[:8192]
		for !utf8.ValidString(s) && len(s) > 0 {
			s = s[:len(s)-1]
		}
	}
	return s
}

// ExecuteTool is a post-policy primitive. Approval is owned by the workflow.
func (a *Activities) ExecuteTool(ctx context.Context, in workflows.ToolInput) (tools.Result, error) {
	registry, err := tools.New(in.Workdir)
	if err != nil {
		return tools.Result{ToolCallID: in.Call.ID, IsError: true, Output: toolBound(err.Error())}, nil
	}
	defer registry.Close()
	result := registry.Execute(ctx, in.Call)
	result.Output = toolBound(result.Output)
	return result, nil
}

func (a *Activities) PreflightTool(ctx context.Context, in workflows.ToolInput) (workflows.PreflightResult, error) {
	if err := ctx.Err(); err != nil {
		return workflows.PreflightResult{}, err
	}
	var registry tools.LocalRegistry // Lookup is static and does not open a root.
	def, ok := registry.Lookup(in.Call.Name)
	if !ok {
		return workflows.PreflightResult{Reason: "unknown tool"}, nil
	}
	result := workflows.PreflightResult{Mode: def.ExecutionMode, Replay: def.Replay}
	decision, reason := tools.BeforeTool(in.Workdir, in.Call)
	result.Reason = toolBound(reason)
	if decision == "block" {
		return result, nil
	}
	hash, err := tools.ActionHash(in.Call)
	if err != nil {
		result.Reason = toolBound(err.Error())
		return result, nil
	}
	result.Allowed = true
	result.RequiresApproval = decision == "needsApproval"
	result.ActionHash = hash
	result.Preview = toolBound(in.Call.Name + " " + string(in.Call.Args))
	if in.Call.Name == "write_file" {
		result.Preview = writeApprovalPreview(ctx, in)
	}
	return result, nil
}

// Called only after schema/path validation and hashing the complete arguments.
// This is a latest bounded observation, not a snapshot bound to execution.
func writeApprovalPreview(ctx context.Context, in workflows.ToolInput) string {
	var args tools.FileArgs
	_ = json.Unmarshal(in.Call.Args, &args) // Already validated above.
	pathLabel := "(path omitted: too long)"
	if len(args.Path) <= 512 {
		pathLabel = strconv.Quote(args.Path)
	}
	header := "--- current " + pathLabel + "\n" + "+++ proposed " + pathLabel + "\n"
	// Quoting can expand unusual paths; cap headers independently of content.
	if len(header) > 1400 {
		header = "--- current (path omitted: too long)\n+++ proposed (path omitted: too long)\n"
	}
	old := tools.Result{IsError: true}
	status := "[current unreadable/unavailable; comparison incomplete]\n"
	registry, err := tools.New(in.Workdir)
	if err == nil {
		defer registry.Close()
		readArgs, _ := json.Marshal(map[string]string{"path": args.Path})
		old = registry.Execute(ctx, llm.ToolCall{Name: "read_file", Args: readArgs})
		if old.IsError {
			// The read tool exposes only textual errors. Use the existing path
			// resolver for typed missing-file classification, never a raw read.
			if _, err := tools.ResolvePath(in.Workdir, args.Path, false); errors.Is(err, os.ErrNotExist) {
				status = "[new file: current target missing at inspection]\n"
			}
		}
	}
	truncated := old.Details["truncated"] == "true"
	if !old.IsError && !truncated && old.Output == args.Content {
		return header + "[unchanged: current and proposed contents are identical]\n"
	}
	preview := header + "@@ whole-file replacement (not a minimal diff) @@\n"
	if old.IsError {
		preview += status
	} else {
		preview += approvalSide(old.Output, "-", "current", truncated)
	}
	return preview + approvalSide(args.Content, "+", "proposed", false)
}

// Independent 3300-byte budgets leave space for headers and guarantee that even
// an enormous old file cannot consume the proposed-content view. Mark partial
// lines explicitly rather than presenting a cut prefix as a complete file.
func approvalSide(content, prefix, label string, sourceTruncated bool) string {
	const budget = 3300
	var out strings.Builder
	lineStart := true
	cut := false
	for _, r := range content {
		text := string(r)
		if r != '\n' && r != '\t' && (r < 32 || r == 127) {
			text = strconv.QuoteRune(r)
		}
		if out.Len()+len(text)+len(prefix) > budget-120 {
			cut = true
			break
		}
		if lineStart {
			out.WriteString(prefix)
		}
		out.WriteString(text)
		lineStart = r == '\n'
	}
	if !lineStart {
		out.WriteByte('\n')
	}
	if cut || sourceTruncated {
		out.WriteString("[" + label + " truncated; remaining content omitted]\n")
	} else if content == "" {
		out.WriteString("[" + label + " empty]\n")
	} else if !strings.HasSuffix(content, "\n") {
		out.WriteString("\\ No newline at end of " + label + " file\n")
	}
	return out.String()
}
