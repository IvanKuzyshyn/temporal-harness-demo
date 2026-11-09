package activities

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

// GatherContext persists a first-writer-wins snapshot. Retries cannot conflict if
// the project changes, and activity history contains only the artifact reference.
func (a *Activities) GatherContext(ctx context.Context, in workflows.GatherInput) (workflows.AppendResult, error) {
	switch in.Kind {
	case "git", "tree", "readme", "todo":
	default:
		return workflows.AppendResult{}, fmt.Errorf("unknown context kind %q", in.Kind)
	}
	ref := TranscriptRef(in.Scope.WorkflowID)
	key := "context/" + in.Kind
	id := entryKey(in.Scope, key+"/0/system")
	message := llm.Message{Role: "system", Content: "Project context (" + in.Kind + "):\n" + toolBound(gatherProject(ctx, in))}
	err := mutateTranscript(ctx, in.DataDir, ref, func(entries *[]transcriptEntry) error {
		for _, entry := range *entries {
			if entry.ID == id {
				message = entry.Message
				return nil
			}
		}
		*entries = append(*entries, transcriptEntry{ID: id, Message: message})
		return nil
	})
	if err != nil {
		return workflows.AppendResult{}, err
	}
	return a.AppendTranscript(ctx, workflows.AppendInput{Scope: in.Scope, DataDir: in.DataDir, TranscriptRef: ref, EntryID: key, Messages: []llm.Message{message}})
}

func gatherProject(ctx context.Context, in workflows.GatherInput) string {
	registry, err := tools.New(in.Workdir)
	if err != nil {
		return "Context unavailable: " + err.Error()
	}
	defer registry.Close()
	run := func(name string, args any) string {
		raw, _ := json.Marshal(args)
		res := registry.Execute(ctx, llm.ToolCall{Name: name, Args: raw})
		if res.IsError {
			return "Context unavailable: " + res.Output
		}
		return res.Output
	}
	switch in.Kind {
	case "tree":
		return run("tree", tools.TreeArgs{Path: ".", Depth: 2})
	case "todo":
		return run("grep", tools.GrepArgs{Path: ".", Pattern: "TODO"})
	case "readme":
		root, err := os.OpenRoot(in.Workdir)
		if err != nil {
			return "Context unavailable: " + err.Error()
		}
		defer root.Close()
		f, err := root.Open(".")
		if err != nil {
			return "Context unavailable: " + err.Error()
		}
		defer f.Close()
		names, err := f.Readdirnames(4097)
		if err != nil && len(names) == 0 {
			return "Context unavailable: no README"
		}
		if len(names) > 4096 {
			return "Context unavailable: directory too large"
		}
		// Sort before selecting bounded README content.
		sort.Strings(names)
		var out strings.Builder
		for _, name := range names {
			if strings.HasPrefix(strings.ToUpper(name), "README") {
				out.WriteString(name + ":\n" + run("read_file", tools.FileArgs{Path: name}) + "\n")
				if out.Len() >= 8192 {
					break
				}
			}
		}
		if out.Len() == 0 {
			return "Context unavailable: no README"
		}
		return toolBound(out.String())
	case "git":
		return gatherGit(ctx, in.Workdir)
	}
	return "Context unavailable"
}

type contextOutput struct{ data []byte }

func (b *contextOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := 8192 - len(b.data)
	if len(p) > remaining {
		p = p[:remaining]
	}
	b.data = append(b.data, p...)
	return n, nil
}

func gatherGit(ctx context.Context, dir string) string {
	root, err := filepath.Abs(dir)
	if err != nil {
		return "Context unavailable: " + err.Error()
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "Context unavailable: " + err.Error()
	}
	// Refuse discovery above root and indirection through gitfiles or symlinks.
	gitdir := filepath.Join(root, ".git")
	st, err := os.Lstat(gitdir)
	if err != nil {
		return "Context unavailable: not a git repository"
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return "Context unavailable: git directory must be a local directory"
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// Explicit paths override core.worktree; local config must not launch helpers
	// or cause status to inspect submodule repositories outside the project.
	cmd := exec.CommandContext(ctx, "git", "--no-pager", "--no-optional-locks",
		"--git-dir="+gitdir, "--work-tree="+root,
		"-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false",
		"-c", "core.hooksPath=/dev/null", "-c", "core.pager=cat",
		"-c", "diff.external=", "-c", "core.bare=false",
		"status", "--short", "--ignore-submodules=all", "--", ".", ":(exclude).data")
	cmd.Dir = root
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C"}
	cmd.WaitDelay = time.Second
	var out contextOutput
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err = cmd.Run(); err != nil {
		return "Context unavailable: " + err.Error() + "\n" + string(out.data)
	}
	return string(out.data)
}
