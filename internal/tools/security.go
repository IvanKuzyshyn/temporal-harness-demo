package tools

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
)

const maxArgs = 1 << 20

func resolvedRoot(root string) (string, error) {
	if root == "" {
		return "", errors.New("empty project root")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return "", errors.New("project root must be a directory")
	}
	return abs, nil
}
func safeRelative(path string) error {
	if filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\\") {
		return errors.New("path must be project-relative")
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." || part == ".git" || part == ".data" {
			return errors.New("traversal or private project path rejected")
		}
	}
	return nil
}
func contained(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	return safeRelative(rel)
}

// ResolvePath resolves existing targets, or a missing write target with an existing
// parent. Both the lexical path and resolved path must remain inside root.
// Execution additionally uses os.Root to prevent symlink-swap escape races.
func ResolvePath(root, path string, forWrite bool) (string, error) {
	root, err := resolvedRoot(root)
	if err != nil {
		return "", err
	}
	if err = safeRelative(path); err != nil {
		return "", err
	}
	target := filepath.Join(root, path)
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil && forWrite && errors.Is(err, os.ErrNotExist) {
		// A dangling symlink is not a new file: never replace it implicitly.
		if _, lerr := os.Lstat(target); !errors.Is(lerr, os.ErrNotExist) {
			return "", errors.New("write target is an unresolved symlink or inaccessible")
		}
		parent, e := filepath.EvalSymlinks(filepath.Dir(target))
		if e != nil {
			return "", e
		}
		resolved = filepath.Join(parent, filepath.Base(target))
		err = nil
	}
	if err != nil {
		return "", err
	}
	if err = contained(root, resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

// canonicalObject rejects duplicate keys, trailing JSON, excessive nesting and
// non-object arguments. Numbers retain their exact lexical value (no float loss).
func canonicalObject(raw json.RawMessage) (map[string]any, error) {
	if len(raw) > maxArgs {
		return nil, errors.New("tool arguments exceed 1 MiB")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var parse func(int) (any, error)
	parse = func(depth int) (any, error) {
		if depth > 64 {
			return nil, errors.New("JSON nesting exceeds limit")
		}
		tok, err := d.Token()
		if err != nil {
			return nil, err
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return tok, nil
		}
		switch delim {
		case '{':
			m := map[string]any{}
			for d.More() {
				key, e := d.Token()
				if e != nil {
					return nil, e
				}
				s, ok := key.(string)
				if !ok {
					return nil, errors.New("invalid object key")
				}
				if _, exists := m[s]; exists {
					return nil, errors.New("duplicate JSON key")
				}
				v, e := parse(depth + 1)
				if e != nil {
					return nil, e
				}
				m[s] = v
			}
			end, e := d.Token()
			if e != nil || end != json.Delim('}') {
				return nil, errors.New("invalid object")
			}
			return m, nil
		case '[':
			a := []any{}
			for d.More() {
				v, e := parse(depth + 1)
				if e != nil {
					return nil, e
				}
				a = append(a, v)
			}
			end, e := d.Token()
			if e != nil || end != json.Delim(']') {
				return nil, errors.New("invalid array")
			}
			return a, nil
		}
		return nil, errors.New("invalid JSON delimiter")
	}
	v, err := parse(0)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, errors.New("trailing JSON")
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("arguments must be a JSON object")
	}
	return m, nil
}
func decodeArgs(raw json.RawMessage, dst any, required ...string) error {
	m, err := canonicalObject(raw)
	if err != nil {
		return err
	}
	for _, k := range required {
		if _, ok := m[k]; !ok {
			return fmt.Errorf("missing argument %s", k)
		}
	}
	for k, v := range m {
		if v == nil {
			return fmt.Errorf("null argument %s", k)
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(dst)
}

// ActionHash excludes the call ID, hashing the name and recursively key-sorted JSON.
func ActionHash(call llm.ToolCall) (string, error) {
	if call.Name == "" {
		return "", errors.New("empty tool name")
	}
	m, err := canonicalObject(call.Args)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(struct {
		Name string         `json:"name"`
		Args map[string]any `json:"args"`
	}{call.Name, m})
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

// ValidateCommand is deliberately a narrow argv allowlist, not a shell parser.
// Go test/build execute project code; approval is NOT a sandbox. The local
// registry never executes commands, even after validation.
func ValidateCommand(root string, args CommandArgs) error {
	root, err := resolvedRoot(root)
	if err != nil {
		return err
	}
	wd, err := ResolvePath(root, args.Workdir, false)
	if err != nil {
		return err
	}
	st, err := os.Stat(wd)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return errors.New("command workdir must be a directory")
	}
	if len(args.Args) > 128 {
		return errors.New("too many command arguments")
	}
	pathArg := func(p string) error {
		if p == "" || strings.HasPrefix(p, "-") || strings.ContainsAny(p, "\n\r\x00;|&`$<>()*?[]{}!:") {
			return errors.New("unsafe path argument")
		}
		resolved, e := ResolvePath(wd, p, false)
		if e != nil {
			return e
		}
		if e = contained(root, resolved); e != nil {
			return e
		}
		return nil
	}
	switch args.Cmd {
	case "go":
		if len(args.Args) == 2 && (args.Args[0] == "test" || args.Args[0] == "build" || args.Args[0] == "vet") && args.Args[1] == "./..." {
			return nil
		}
	case "ls", "cat":
		if args.Cmd == "cat" && len(args.Args) == 0 {
			return errors.New("cat requires a path")
		}
		for _, p := range args.Args {
			if err := pathArg(p); err != nil {
				return err
			}
		}
		return nil
	case "git":
		a := args.Args
		if len(a) == 2 && a[0] == "status" && a[1] == "--short" {
			return nil
		}
		if len(a) >= 3 && a[0] == "diff" && a[1] == "--no-ext-diff" && a[2] == "--no-textconv" {
			if len(a) == 3 {
				return nil
			}
			if a[3] != "--" {
				return errors.New("git diff paths require --")
			}
			for _, p := range a[4:] {
				if err := pathArg(p); err != nil {
					return err
				}
			}
			return nil
		}
	}
	return errors.New("command or argv not allowlisted")
}

func ValidateCall(root string, call llm.ToolCall) error {
	switch call.Name {
	case "read_file", "list_dir", "write_file":
		var a FileArgs
		required := []string{"path"}
		if call.Name == "write_file" {
			required = append(required, "content")
		}
		if err := decodeArgs(call.Args, &a, required...); err != nil {
			return err
		}
		if call.Name != "write_file" {
			m, _ := canonicalObject(call.Args)
			if _, ok := m["content"]; ok {
				return errors.New("content is only valid for write_file")
			}
		}
		p, err := ResolvePath(root, a.Path, call.Name == "write_file")
		if err != nil {
			return err
		}
		st, err := os.Stat(p)
		if call.Name == "write_file" && errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if call.Name == "list_dir" {
			if !st.IsDir() {
				return errors.New("expected directory")
			}
		} else if !st.Mode().IsRegular() {
			return errors.New("expected regular file")
		}
		return nil
	case "grep":
		var a GrepArgs
		if err := decodeArgs(call.Args, &a, "path", "pattern"); err != nil {
			return err
		}
		if len(a.Pattern) > 1024 {
			return errors.New("grep pattern too large")
		}
		if _, err := regexp.Compile(a.Pattern); err != nil {
			return err
		}
		_, err := ResolvePath(root, a.Path, false)
		return err
	case "tree":
		var a TreeArgs
		if err := decodeArgs(call.Args, &a, "path"); err != nil {
			return err
		}
		if a.Depth < 0 || a.Depth > 32 {
			return errors.New("tree depth must be 0..32")
		}
		_, err := ResolvePath(root, a.Path, false)
		return err
	case "delegate":
		var a DelegateArgs
		if err := decodeArgs(call.Args, &a, "path", "question"); err != nil {
			return err
		}
		if strings.TrimSpace(a.Question) == "" {
			return errors.New("empty delegation question")
		}
		_, err := ResolvePath(root, a.Path, false)
		return err
	case "run_command":
		var a CommandArgs
		if err := decodeArgs(call.Args, &a, "cmd", "args"); err != nil {
			return err
		}
		return ValidateCommand(root, a)
	default:
		return fmt.Errorf("unknown tool %q", call.Name)
	}
}
func BeforeTool(root string, call llm.ToolCall) (decision string, reason string) {
	if err := ValidateCall(root, call); err != nil {
		return "block", err.Error()
	}
	if call.Name == "write_file" || call.Name == "run_command" {
		return "needsApproval", "unsafe tool always requires approval; Go test/build execute project code, approval is not a sandbox"
	}
	return "allow", "validated project-local tool"
}
