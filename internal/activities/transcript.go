package activities

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"go.temporal.io/sdk/activity"
)

const maxTranscriptBytes = 32 << 20

type transcriptEntry struct {
	ID      string      `json:"id"`
	Message llm.Message `json:"message"`
	Tokens  int         `json:"tokens,omitempty"`
}

// TranscriptRef is an opaque relative artifact name, safe across case-folding
// filesystems. Identity is encoded rather than interpreted as a path.
func TranscriptRef(workflowID string) string {
	sum := sha256.Sum256([]byte(workflowID))
	return "artifacts/" + hex.EncodeToString(sum[:]) + ".transcript.json"
}

func transcriptPath(dataDir, ref string) (string, error) {
	if !strings.HasPrefix(ref, "artifacts/") || !strings.HasSuffix(ref, ".transcript.json") {
		return "", fmt.Errorf("invalid transcript reference")
	}
	name := strings.TrimSuffix(strings.TrimPrefix(ref, "artifacts/"), ".transcript.json")
	if len(name) != 64 || strings.ToLower(name) != name {
		return "", fmt.Errorf("invalid transcript identity")
	}
	if _, err := hex.DecodeString(name); err != nil {
		return "", err
	}
	return filepath.Join(dataDir, filepath.FromSlash(ref)), nil
}

func readEntries(path string) ([]transcriptEntry, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxTranscriptBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxTranscriptBytes {
		return nil, fmt.Errorf("transcript size limit exceeded")
	}
	var entries []transcriptEntry
	if err = json.Unmarshal(b, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// ReadTranscript is an activity-side helper. Atomic replacement permits readers
// to see either complete version without taking the writer lock.
func ReadTranscript(dataDir, ref string) ([]llm.Message, error) {
	path, err := transcriptPath(dataDir, ref)
	if err != nil {
		return nil, err
	}
	entries, err := readEntries(path)
	if err != nil {
		return nil, err
	}
	messages := make([]llm.Message, 0, len(entries))
	for _, e := range entries {
		messages = append(messages, e.Message)
	}
	return messages, nil
}

func lockFile(f *os.File, op int) error {
	for {
		err := syscall.Flock(int(f.Fd()), op)
		if err != syscall.EINTR {
			return err
		}
	}
}

// A separate lock inode survives atomic transcript replacement. Locks cover only
// persistence, never the model call. The first committed model response wins.
func mutateTranscript(ctx context.Context, dataDir, ref string, fn func(*[]transcriptEntry) error) error {
	path, err := transcriptPath(dataDir, ref)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = lockFile(lock, syscall.LOCK_EX); err != nil {
		return err
	}
	defer lockFile(lock, syscall.LOCK_UN)
	if err = ctx.Err(); err != nil {
		return err
	}
	entries, err := readEntries(path)
	if err != nil {
		return err
	}
	if err = fn(&entries); err != nil {
		return err
	}
	b, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	if len(b) > maxTranscriptBytes {
		return fmt.Errorf("transcript size limit exceeded")
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".transcript-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	if _, err = temp.Write(b); err != nil {
		return err
	}
	if err = temp.Sync(); err != nil {
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

func entryKey(scope events.Scope, key string) string {
	b, _ := json.Marshal([]string{scope.WorkflowID, scope.RunID, key})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func activityKey(ctx context.Context, key string) (string, error) {
	if key != "" {
		return key, nil
	}
	if activity.IsActivity(ctx) {
		return activity.GetInfo(ctx).ActivityID, nil
	}
	return "", fmt.Errorf("stable entry/input ID required outside activity context")
}

func (a *Activities) AppendTranscript(ctx context.Context, in workflows.AppendInput) (workflows.AppendResult, error) {
	ref := in.TranscriptRef
	if ref == "" {
		ref = TranscriptRef(in.Scope.WorkflowID)
	}
	if ref != TranscriptRef(in.Scope.WorkflowID) {
		return workflows.AppendResult{}, fmt.Errorf("transcript identity mismatch")
	}
	key, err := activityKey(ctx, in.EntryID)
	if err != nil {
		return workflows.AppendResult{}, err
	}
	err = mutateTranscript(ctx, in.DataDir, ref, func(entries *[]transcriptEntry) error {
		for i, m := range in.Messages {
			id := entryKey(in.Scope, fmt.Sprintf("%s/%d/%s", key, i, m.Role))
			found := false
			for _, e := range *entries {
				if e.ID == id {
					old, _ := json.Marshal(e.Message)
					next, _ := json.Marshal(m)
					if string(old) != string(next) {
						return fmt.Errorf("conflicting transcript entry %s", id)
					}
					found = true
					break
				}
			}
			if !found {
				*entries = append(*entries, transcriptEntry{ID: id, Message: m})
			}
		}
		return nil
	})
	if err != nil {
		return workflows.AppendResult{}, err
	}
	result := workflows.AppendResult{TranscriptRef: ref}
	// Lifecycle events may repeat on retry, but durable entries never do. Do not
	// publish anything until the entire batch has committed successfully.
	for i, m := range in.Messages {
		if m.Role != "user" && m.Role != "tool" {
			continue
		}
		id := entryKey(in.Scope, fmt.Sprintf("%s/%d/%s", key, i, m.Role))
		for _, typ := range []events.Type{events.MessageStart, events.MessageEnd} {
			if err := a.emitDisplay(ctx, workflows.EmitInput{Scope: in.Scope, DataDir: in.DataDir, Type: typ, Pattern: events.None, Data: events.MessageData{Role: m.Role, MessageRef: ref + "#" + id}}, &result.LastSeq); err != nil {
				return result, err
			}
		}
	}
	return result, nil
}
