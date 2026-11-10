package activities

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
)

const (
	// MaxTranscriptPage is the maximum number of complete records returned.
	MaxTranscriptPage    = 64
	maxTranscriptRecords = 65536
)

// TranscriptRecord carries durable message identity, not a tool-call ID.
// Messages are returned intact; terminal sanitization belongs to the caller.
type TranscriptRecord struct {
	ID      string      `json:"id"`
	Message llm.Message `json:"message"`
}

// TranscriptPage is a chronological window. Before is its first ordinal;
// HasMore distinguishes the beginning of history from the newest-page sentinel.
// Total describes this read, not a promise that the artifact cannot grow.
type TranscriptPage struct {
	Entries []TranscriptRecord
	Before  int
	HasMore bool
	Total   int
}

type transcriptContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r transcriptContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

// ReadTranscriptPage reads only the exact workflow-derived local artifact.
// before=0 selects the newest page; otherwise it is an exclusive ordinal cursor.
// Atomic writer replacement makes each read consistent; appends preserve cursors.
// This does no workflow I/O and must be called outside workflow execution.
// Cancellation is checked around reads/decoding, not a guarantee of interruptible
// filesystem syscalls. Missing artifacts are errors, not certified empty history.
func ReadTranscriptPage(ctx context.Context, dataDir, workflowID string, before, limit int) (TranscriptPage, error) {
	fail := func(err error) (TranscriptPage, error) { return TranscriptPage{}, err }
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if !filepath.IsAbs(dataDir) || workflowID == "" || before < 0 || limit <= 0 {
		return fail(fmt.Errorf("invalid transcript directory, identity, cursor or limit"))
	}
	limit = min(limit, MaxTranscriptPage)
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return fail(err)
	}
	defer root.Close()
	// Pin the artifacts directory too: a file symlink cannot reach a sibling
	// elsewhere in DataDir. Root containment protects against replacement races.
	artifacts, err := root.OpenRoot("artifacts")
	if err != nil {
		return fail(err)
	}
	defer artifacts.Close()
	// Nonblocking open lets us reject special files without FIFO waits.
	f, err := artifacts.OpenFile(filepath.Base(TranscriptRef(workflowID)), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fail(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxTranscriptBytes {
		return fail(fmt.Errorf("transcript is not a bounded regular file"))
	}
	raw, err := io.ReadAll(io.LimitReader(transcriptContextReader{ctx, f}, maxTranscriptBytes+1))
	if err != nil {
		return fail(err)
	}
	if err = ctx.Err(); err != nil {
		return fail(err)
	}
	if len(raw) > maxTranscriptBytes {
		return fail(fmt.Errorf("transcript size limit exceeded"))
	}
	if !utf8.Valid(raw) {
		return fail(fmt.Errorf("invalid transcript UTF-8"))
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	token, err := dec.Token()
	if err != nil {
		return fail(err)
	}
	if token != json.Delim('[') {
		return fail(fmt.Errorf("transcript must be a JSON array"))
	}
	// Decode one record at a time, retaining only the requested window rather
	// than allocating all decoded messages in the 32 MiB artifact.
	ring := make([]TranscriptRecord, limit)
	seen := make(map[string]struct{})
	total, kept := 0, 0
	for dec.More() {
		if err = ctx.Err(); err != nil {
			return fail(err)
		}
		if total >= maxTranscriptRecords {
			return fail(fmt.Errorf("transcript record limit exceeded"))
		}
		var record TranscriptRecord
		if err = dec.Decode(&record); err != nil {
			return fail(err)
		}
		if len(record.ID) != 64 || strings.ToLower(record.ID) != record.ID {
			return fail(fmt.Errorf("invalid transcript message identity"))
		}
		if _, err = hex.DecodeString(record.ID); err != nil {
			return fail(fmt.Errorf("invalid transcript message identity"))
		}
		if _, ok := seen[record.ID]; ok {
			return fail(fmt.Errorf("duplicate transcript message identity"))
		}
		seen[record.ID] = struct{}{}
		if len(record.Message.ToolCalls) > 32 {
			return fail(fmt.Errorf("transcript tool-call count limit exceeded"))
		}
		callIDs := make(map[string]struct{}, len(record.Message.ToolCalls))
		for _, call := range record.Message.ToolCalls {
			if call.ID == "" {
				return fail(fmt.Errorf("missing transcript tool-call identity"))
			}
			if _, exists := callIDs[call.ID]; exists {
				return fail(fmt.Errorf("ambiguous transcript tool-call identity"))
			}
			callIDs[call.ID] = struct{}{}
		}
		switch record.Message.Role {
		case "system", "user", "assistant", "tool":
		default:
			return fail(fmt.Errorf("invalid transcript message role"))
		}
		if before == 0 || total < before {
			ring[kept%limit] = record
			kept++
		}
		total++
	}
	if _, err = dec.Token(); err != nil {
		return fail(err)
	}
	var extra any
	if err = dec.Decode(&extra); err != io.EOF {
		return fail(fmt.Errorf("invalid trailing transcript JSON"))
	}
	if err = ctx.Err(); err != nil {
		return fail(err)
	}
	if before > total {
		return fail(fmt.Errorf("transcript cursor exceeds total (artifact changed)"))
	}
	count := min(kept, limit)
	page := TranscriptPage{Total: total, Before: kept - count, HasMore: kept > count, Entries: make([]TranscriptRecord, 0, count)}
	for i := kept - count; i < kept; i++ {
		page.Entries = append(page.Entries, ring[i%limit])
	}
	return page, nil
}
