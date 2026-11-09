package events

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

// MaxRecordBytes includes the terminating LF. Oversized records are rejected.
const MaxRecordBytes = 1 << 20
const MaxTailRecords = 1024

var ErrTruncated = errors.New("event stream truncated")

type Store struct{ dataDir string }

func NewStore(dataDir string) *Store { return &Store{dataDir: dataDir} }

// Path encodes slashes rather than creating workflow-controlled directories.
func (s *Store) Path(streamID string) (string, error) {
	if streamID == "" || !utf8.ValidString(streamID) || strings.Contains(streamID, "\\") {
		return "", fmt.Errorf("invalid stream ID")
	}
	for _, r := range streamID {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("invalid stream ID")
		}
	}
	for _, part := range strings.Split(streamID, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("invalid stream ID component")
		}
	}
	name := url.PathEscape(streamID) + ".jsonl"
	if len(name) > 255 {
		return "", fmt.Errorf("encoded stream ID too long")
	}
	return filepath.Join(s.dataDir, "streams", name), nil
}

func (s *Store) Append(scope Scope, typ Type, pattern Pattern, data any) (Event, error) {
	var zero Event
	path, err := s.Path(scope.StreamID)
	if err != nil {
		return zero, err
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return zero, fmt.Errorf("event data: %w", err)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return zero, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return zero, err
	}
	defer f.Close()
	if err = flock(f, syscall.LOCK_EX); err != nil {
		return zero, err
	}
	defer flock(f, syscall.LOCK_UN)
	seq, end, err := lastRecord(f)
	if err != nil {
		return zero, err
	}
	if seq == math.MaxUint64 {
		return zero, fmt.Errorf("sequence exhausted")
	}
	e := Event{Seq: seq + 1, TS: time.Now().UTC().Format(time.RFC3339Nano), WorkflowID: scope.WorkflowID, RunID: scope.RunID, ParentWorkflowID: scope.ParentWorkflowID, Type: typ, Pattern: pattern, Data: payload}
	b, err := json.Marshal(e)
	if err != nil {
		return zero, err
	}
	b = append(b, '\n')
	if len(b) > MaxRecordBytes {
		return zero, fmt.Errorf("event record too large")
	}
	if err = f.Truncate(end); err != nil {
		return zero, err
	}
	n, err := f.WriteAt(b, end)
	if err != nil {
		return zero, err
	}
	if n != len(b) {
		return zero, io.ErrShortWrite
	}
	return e, nil
}
func flock(f *os.File, mode int) error {
	for {
		err := syscall.Flock(int(f.Fd()), mode)
		if err != syscall.EINTR {
			return err
		}
	}
}

// Only the bounded suffix containing the last record and a possible partial
// record is read. Corruption older than that suffix is detected by Tail.
func lastRecord(f *os.File) (uint64, int64, error) {
	st, err := f.Stat()
	if err != nil {
		return 0, 0, err
	}
	size := st.Size()
	start := size - int64(2*MaxRecordBytes)
	if start < 0 {
		start = 0
	}
	b := make([]byte, size-start)
	if _, err = f.ReadAt(b, start); err != nil {
		return 0, 0, err
	}
	last := bytes.LastIndexByte(b, '\n')
	if len(b)-last-1 >= MaxRecordBytes {
		return 0, 0, fmt.Errorf("partial record too large")
	}
	if last < 0 {
		if start != 0 {
			return 0, 0, fmt.Errorf("no complete record in bounded suffix")
		}
		return 0, 0, nil
	}
	prev := bytes.LastIndexByte(b[:last], '\n')
	if prev < 0 && start != 0 {
		return 0, 0, fmt.Errorf("record exceeds bounded suffix")
	}
	line := b[prev+1 : last]
	if len(line)+1 > MaxRecordBytes {
		return 0, 0, fmt.Errorf("event record too large")
	}
	e, err := decode(line)
	if err != nil {
		return 0, 0, err
	}
	return e.Seq, start + int64(last) + 1, nil
}
func decode(line []byte) (Event, error) {
	var e Event
	if !utf8.Valid(line) {
		return e, fmt.Errorf("invalid UTF-8 event")
	}
	if err := json.Unmarshal(line, &e); err != nil {
		return e, fmt.Errorf("malformed event: %w", err)
	}
	if e.Seq == 0 || len(e.Data) == 0 {
		return e, fmt.Errorf("event requires positive seq and data")
	}
	return e, nil
}
