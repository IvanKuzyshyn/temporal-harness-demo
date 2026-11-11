package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
)

const (
	maxOutput    = 8192
	maxEntries   = 4096
	maxFileRead  = 64 * 1024
	maxScanBytes = 4 * 1024 * 1024
)

// LocalRegistry pins filesystem operations to an open project directory. Call
// Close when finished. Approval must be enforced by the caller before Execute;
// Execute has no approval token and is the post-policy execution primitive.
type LocalRegistry struct {
	workdir string
	root    *os.Root
}

var _ Registry = (*LocalRegistry)(nil)

func New(workdir string) (*LocalRegistry, error) {
	path, err := resolvedRoot(workdir)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	return &LocalRegistry{workdir: path, root: root}, nil
}
func (r *LocalRegistry) Close() error { return r.root.Close() }
func (r *LocalRegistry) Lookup(name string) (Definition, bool) {
	mode, replay := Parallel, Safe
	properties := map[string]any{"path": map[string]string{"type": "string"}}
	required := []string{"path"}
	desc := ""
	switch name {
	case "read_file":
		desc = "Read up to 8 KiB of a project-local regular file."
	case "list_dir":
		desc = "List sorted project-local directory entries, excluding .git and .data (8 KiB maximum)."
	case "grep":
		desc = "Search bounded project-local text with a Go regular expression (8 KiB maximum)."
		properties["pattern"] = map[string]string{"type": "string"}
		required = append(required, "pattern")
	case "tree":
		desc = "Sorted bounded project tree; depth 0 defaults to 4, maximum 32 (8 KiB maximum)."
		properties["depth"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 32}
	case "write_file":
		desc = "Atomically write a project-local file; always requires approval."
		mode, replay = Sequential, Unsafe
		properties["content"] = map[string]string{"type": "string"}
		required = append(required, "content")
	case "run_command":
		desc = "Request an allowlisted argv command activity. Always requires approval; Go test/build execute project code, approval is not a sandbox. Unsupported locally."
		mode, replay = Sequential, Unsafe
		properties = map[string]any{"cmd": map[string]string{"type": "string"}, "args": map[string]any{"type": "array", "items": map[string]string{"type": "string"}}, "workdir": map[string]string{"type": "string"}}
		required = []string{"cmd", "args"}
	case "delegate":
		desc = "Request a delegation workflow (unsupported locally)."
		mode = Sequential
		properties["question"] = map[string]string{"type": "string"}
		required = append(required, "question")
	default:
		return Definition{}, false
	}
	schema, _ := json.Marshal(map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false})
	return Definition{ToolDefinition: llm.ToolDefinition{Name: name, Description: desc, Parameters: schema}, ExecutionMode: mode, Replay: replay, Callers: []string{"model", "internal"}, NeedsApproval: func(json.RawMessage) bool { return replay == Unsafe }}, true
}
func (r *LocalRegistry) Definitions(caller string) []Definition {
	if caller != "model" && caller != "internal" {
		return nil
	}
	defs := make([]Definition, 0, 7)
	for _, name := range []string{"delegate", "grep", "list_dir", "read_file", "run_command", "tree", "write_file"} {
		d, _ := r.Lookup(name)
		defs = append(defs, d)
	}
	return defs
}
func (r *LocalRegistry) relative(path string, write bool) (string, error) {
	p, err := ResolvePath(r.workdir, path, write)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(r.workdir, p)
	if err != nil {
		return "", err
	}
	if err = safeRelative(rel); err != nil {
		return "", err
	}
	return rel, nil
}
func (r *LocalRegistry) readFile(path string, limit int) ([]byte, error) {
	st, err := r.root.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("expected regular file")
	}
	f, err := r.root.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err = f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("expected regular file")
	}
	return io.ReadAll(io.LimitReader(f, int64(limit)))
}
func (r *LocalRegistry) entries(path string) ([]os.DirEntry, error) {
	f, err := r.root.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(maxEntries + 1)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(entries) > maxEntries {
		return nil, errors.New("directory exceeds 4096 entry limit")
	}
	filtered := entries[:0]
	for _, entry := range entries {
		if entry.Name() != ".git" && entry.Name() != ".data" {
			filtered = append(filtered, entry)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].Name() < filtered[j].Name() })
	return filtered, nil
}
func (r *LocalRegistry) atomicWrite(path, content string) error {
	// Pin the resolved parent separately; both temp creation and rename use it.
	parent, err := r.root.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer parent.Close()
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return err
	}
	tmp := ".tool-write-" + hex.EncodeToString(random[:])
	f, err := parent.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer parent.Remove(tmp)
	_, err = io.WriteString(f, content)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return parent.Rename(tmp, filepath.Base(path))
}
func (r *LocalRegistry) Execute(ctx context.Context, call llm.ToolCall) Result {
	res := Result{ToolCallID: call.ID}
	fail := func(err error) Result {
		res.IsError = true
		res.Interrupted = errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
		res.Output = err.Error()
		if len(res.Output) > maxOutput {
			res.Output = res.Output[:maxOutput]
		}
		return res
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if err := ValidateCall(r.workdir, call); err != nil {
		return fail(err)
	}
	if call.Name == "run_command" {
		return fail(errors.New("unsupported locally: run_command requires an approved command activity"))
	}
	if call.Name == "delegate" {
		return fail(errors.New("unsupported locally: delegate requires a delegation workflow"))
	}
	var common struct{ Path string }
	if err := json.Unmarshal(call.Args, &common); err != nil {
		return fail(err)
	}
	path, err := r.relative(common.Path, call.Name == "write_file")
	if err != nil {
		return fail(err)
	}
	var out strings.Builder
	truncated := false
	emit := func(s string) {
		remaining := maxOutput - out.Len()
		if len(s) > remaining {
			s = s[:remaining]
			truncated = true
		}
		out.WriteString(s)
	}
	switch call.Name {
	case "read_file":
		data, e := r.readFile(path, maxOutput+1)
		if e != nil {
			return fail(e)
		}
		emit(string(data))
	case "write_file":
		var a FileArgs
		json.Unmarshal(call.Args, &a)
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if err := r.atomicWrite(path, a.Content); err != nil {
			return fail(err)
		}
		emit("wrote " + filepath.ToSlash(path))
	case "list_dir":
		entries, e := r.entries(path)
		if e != nil {
			return fail(e)
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return fail(err)
			}
			suffix := ""
			if entry.IsDir() {
				suffix = "/"
			}
			emit(entry.Name() + suffix + "\n")
			if out.Len() == maxOutput {
				truncated = true
				break
			}
		}
	case "tree", "grep":
		depth := 32
		var pattern *regexp.Regexp
		if call.Name == "tree" {
			var a TreeArgs
			json.Unmarshal(call.Args, &a)
			depth = a.Depth
			if depth == 0 {
				depth = 4
			}
		} else {
			var a GrepArgs
			json.Unmarshal(call.Args, &a)
			pattern, err = regexp.Compile(a.Pattern)
			if err != nil {
				return fail(err)
			}
		}
		visited, scanned := 0, 0
		var walk func(string, int) error
		walk = func(p string, level int) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if visited >= maxEntries || out.Len() >= maxOutput || scanned >= maxScanBytes {
				truncated = true
				return nil
			}
			visited++
			st, e := r.root.Lstat(p)
			if e != nil {
				return e
			}
			// Do not follow directory/file symlinks during recursive traversal.
			if st.Mode()&os.ModeSymlink != 0 {
				return nil
			}
			if pattern == nil {
				suffix := ""
				if st.IsDir() {
					suffix = "/"
				}
				emit(filepath.ToSlash(p) + suffix + "\n")
			}
			if st.IsDir() {
				if level >= depth {
					truncated = true
					return nil
				}
				entries, e := r.entries(p)
				if e != nil {
					return e
				}
				for _, entry := range entries {
					if visited >= maxEntries || out.Len() >= maxOutput || scanned >= maxScanBytes {
						truncated = true
						break
					}
					if e = walk(filepath.Join(p, entry.Name()), level+1); e != nil {
						return e
					}
				}
				return nil
			}
			if pattern != nil && st.Mode().IsRegular() {
				limit := min(maxFileRead, maxScanBytes-scanned)
				data, e := r.readFile(p, limit)
				if e != nil {
					return e
				}
				scanned += len(data)
				if st.Size() > int64(len(data)) {
					truncated = true
				}
				if strings.IndexByte(string(data), 0) >= 0 {
					return nil
				}
				for i, line := range strings.Split(string(data), "\n") {
					if err := ctx.Err(); err != nil {
						return err
					}
					if pattern.MatchString(line) {
						emit(fmt.Sprintf("%s:%d:%s\n", filepath.ToSlash(p), i+1, line))
						if out.Len() >= maxOutput {
							truncated = true
							break
						}
					}
				}
			}
			return nil
		}
		if err := walk(path, 0); err != nil {
			return fail(err)
		}
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	res.Output = out.String()
	if truncated {
		res.Details = map[string]string{"truncated": "true"}
	}
	return res
}
