package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"unicode/utf8"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
)

const cliEventBatch = 64

// The shared Tail API has no configurable batch limit. Keep CLI reads smaller
// without changing other consumers: at most 64 records of MaxRecordBytes each.
// Advance only over valid complete records, retaining the shared tail semantics.
func tailCLIEvents(path string, offset int64) ([]events.Event, int64, error) {
	if offset < 0 {
		return nil, offset, fmt.Errorf("negative tail offset")
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, offset, nil
	}
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, offset, err
	}
	if !st.Mode().IsRegular() {
		return nil, offset, fmt.Errorf("stream is not a regular file")
	}
	if st.Size() < offset {
		return nil, offset, events.ErrTruncated
	}
	r := bufio.NewReaderSize(io.NewSectionReader(f, offset, st.Size()-offset), events.MaxRecordBytes)
	var batch []events.Event
	for len(batch) < cliEventBatch {
		line, err := r.ReadSlice('\n')
		if errors.Is(err, io.EOF) {
			return batch, offset, nil
		}
		if err != nil {
			return batch, offset, fmt.Errorf("event record read: %w", err)
		}
		var event events.Event
		if !utf8.Valid(line) {
			return batch, offset, fmt.Errorf("invalid UTF-8 event")
		}
		if err := json.Unmarshal(line, &event); err != nil {
			return batch, offset, fmt.Errorf("malformed event: %w", err)
		}
		if event.Seq == 0 || len(event.Data) == 0 {
			return batch, offset, fmt.Errorf("event requires positive seq and data")
		}
		batch = append(batch, event)
		offset += int64(len(line))
	}
	return batch, offset, nil
}
