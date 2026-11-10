package activities

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
)

const (
	evidenceEntries   = 4096
	evidenceFiles     = 8
	evidenceFileBytes = 8 << 10
	evidenceBytes     = 32 << 10
)

// projectEvidence is automatic inspection, not tool execution or a project
// classifier. All opens are pinned to os.Root; recursive discovery follows no
// symlinks. Even excluded entries consume the traversal budget.
func projectEvidence(ctx context.Context, workdir string) string {
	root, err := os.OpenRoot(workdir)
	if err != nil {
		return "Context unavailable: " + err.Error()
	}
	defer root.Close()
	var tree, notes strings.Builder
	truncated := false
	note := func(s string) {
		if notes.Len()+len(s)+1 <= 2048 {
			notes.WriteString(s + "\n")
		} else {
			truncated = true
		}
	}
	paths := []string{}
	queue := []string{"."}
	visited := 0
	for len(queue) > 0 && visited < evidenceEntries {
		if err := ctx.Err(); err != nil {
			note("Context unavailable: " + err.Error())
			break
		}
		dir := queue[0]
		queue = queue[1:]
		f, err := root.Open(dir)
		if err != nil {
			note(fmt.Sprintf("%q: read failed: %v", dir, err))
			continue
		}
		entries, readErr := f.ReadDir(evidenceEntries - visited)
		f.Close()
		if readErr != nil && readErr != io.EOF {
			note(fmt.Sprintf("%q: read failed: %v", dir, readErr))
		}
		visited += len(entries)
		if visited == evidenceEntries {
			truncated = true
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			if tools.SkipDiscoveryEntry(entry.Name()) {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			st, err := root.Lstat(path)
			if err != nil {
				note(fmt.Sprintf("%q: read failed: %v", path, err))
				continue
			}
			if st.Mode()&os.ModeSymlink != 0 {
				note(fmt.Sprintf("%q: skipped symlink (not followed)", path))
				continue
			}
			line := fmt.Sprintf("%q\n", filepath.ToSlash(path))
			if tree.Len()+len(line) <= 4096 {
				tree.WriteString(line)
			} else {
				truncated = true
			}
			if st.IsDir() {
				queue = append(queue, path)
			} else if st.Mode().IsRegular() && evidencePriority(path) < 4 {
				paths = append(paths, path)
			}
		}
	}
	sort.Slice(paths, func(i, j int) bool {
		a, b := evidencePriority(paths[i]), evidencePriority(paths[j])
		if a != b {
			return a < b
		}
		return paths[i] < paths[j]
	})
	var out strings.Builder
	// Leave room for the terminal truncation marker; total includes labels/paths.
	emit := func(s string) {
		remaining := evidenceBytes - 128 - out.Len()
		if len(s) > remaining {
			s = bounded(s, remaining)
			truncated = true
		}
		out.WriteString(s)
	}
	emit("Automatic project evidence: observed files only; code and tests were not executed. Bounded sample, not a complete inventory. TODOs, if present below, are source text.\nObserved paths:\n" + tree.String() + "Discovery notes:\n" + notes.String())
	if len(paths) == 0 {
		emit("Context unavailable: no recognized manifest, README or source evidence found. Use inspection tools if needed.\n")
	}
	reads, readBytes := 0, 0
	for _, path := range paths {
		if reads == evidenceFiles || readBytes == evidenceBytes || out.Len() >= evidenceBytes-128 {
			truncated = true
			break
		}
		if err := ctx.Err(); err != nil {
			emit("Context unavailable: " + err.Error() + "\n")
			break
		}
		reads++
		// Recheck type before and after opening; os.Root rejects escaping links even
		// if a discovered file is replaced before this read.
		data, err := readEvidenceFile(root, path, min(evidenceFileBytes, evidenceBytes-readBytes))
		readBytes += len(data.bytes)
		if err != nil {
			emit(fmt.Sprintf("File %q: read failed: %v\n", filepath.ToSlash(path), err))
			continue
		}
		emit(fmt.Sprintf("File %q — observed contents (not executed):\n", filepath.ToSlash(path)))
		emit(string(data.bytes) + "\n")
		if data.truncated {
			emit(fmt.Sprintf("[file truncated at %d bytes]\n", len(data.bytes)))
			truncated = true
		}
	}
	if truncated {
		out.WriteString("\n[evidence truncated: entry, file, per-file, listing, diagnostic or aggregate limit reached]\n")
	}
	return out.String()
}

type evidenceRead struct {
	bytes     []byte
	truncated bool
}

func readEvidenceFile(root *os.Root, path string, limit int) (evidenceRead, error) {
	st, err := root.Lstat(path)
	if err != nil {
		return evidenceRead{}, err
	}
	if !st.Mode().IsRegular() {
		return evidenceRead{}, fmt.Errorf("not a regular file (symlinks excluded)")
	}
	f, err := root.Open(path)
	if err != nil {
		return evidenceRead{}, err
	}
	defer f.Close()
	st, err = f.Stat()
	if err != nil {
		return evidenceRead{}, err
	}
	if !st.Mode().IsRegular() {
		return evidenceRead{}, fmt.Errorf("not a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, int64(limit)))
	return evidenceRead{bytes: b, truncated: st.Size() > int64(len(b))}, err
}

// Manifests first, then likely entry points, README, then general source. This
// deliberately does not infer behavior from filenames or execute manifests.
func evidencePriority(path string) int {
	name := strings.ToLower(filepath.Base(path))
	switch name {
	case "go.mod", "package.json", "pyproject.toml", "cargo.toml", "pom.xml", "build.gradle", "gemfile", "composer.json", "requirements.txt", "mix.exs", "deno.json", "csproj":
		return 0
	case "main.go", "main.py", "app.py", "__main__.py", "index.js", "index.ts", "main.js", "main.ts", "main.rs", "lib.rs", "main.java", "program.cs":
		return 1
	}
	if strings.HasPrefix(name, "readme") {
		return 2
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go", ".py", ".js", ".jsx", ".ts", ".tsx", ".rs", ".java", ".kt", ".rb", ".php", ".c", ".h", ".cpp", ".cs", ".ex", ".exs", ".swift", ".scala", ".sh", ".vue", ".svelte":
		return 3
	}
	return 4
}
