package events

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
)

// Tail reads at most MaxTailRecords from the current file-size snapshot. nextOffset
// advances only over valid complete records, including when an error is returned.
// Missing files retain offset; a file shorter than offset returns ErrTruncated.
func Tail(path string, offset int64) (events []Event, nextOffset int64, err error) {
	nextOffset = offset
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
		return nil, offset, ErrTruncated
	}
	r := bufio.NewReaderSize(io.NewSectionReader(f, offset, st.Size()-offset), MaxRecordBytes)
	for len(events) < MaxTailRecords {
		line, e := r.ReadSlice('\n')
		if errors.Is(e, bufio.ErrBufferFull) {
			return events, nextOffset, fmt.Errorf("event record too large")
		}
		if errors.Is(e, io.EOF) {
			return events, nextOffset, nil
		}
		if e != nil {
			return events, nextOffset, e
		}
		event, e := decode(line[:len(line)-1])
		if e != nil {
			return events, nextOffset, e
		}
		events = append(events, event)
		nextOffset += int64(len(line))
	}
	return events, nextOffset, nil
}

var ErrGap = errors.New("event sequence gap")

const MaxReconnectEvents = 4096

// Reconnect buffers one pending state query. It is not safe for concurrent use.
// On any error discard it and query state again. Resolve consumes the buffer.
type Reconnect struct {
	buffered []Event
	err      error
}

func NewReconnect() *Reconnect { return &Reconnect{} }
func (r *Reconnect) Buffer(events []Event) error {
	if r.err != nil {
		return r.err
	}
	if len(events) > MaxReconnectEvents-len(r.buffered) {
		r.err = fmt.Errorf("reconnect buffer full; query state again")
		return r.err
	}
	r.buffered = append(r.buffered, events...)
	return nil
}
func (r *Reconnect) Resolve(snapshotLastSeq uint64) ([]Event, error) {
	if r.err != nil {
		return nil, r.err
	}
	var result []Event
	last := snapshotLastSeq
	for _, e := range r.buffered {
		if e.Seq <= snapshotLastSeq {
			continue
		}
		if e.Seq != last+1 {
			r.err = fmt.Errorf("%w: expected %d, got %d", ErrGap, last+1, e.Seq)
			return nil, r.err
		}
		result = append(result, e)
		last = e.Seq
	}
	r.buffered = nil
	return result, nil
}
