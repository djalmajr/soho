package transcript

import (
	"bytes"
	"context"
	"encoding/json"
	"os"

	"github.com/djalmajr/soho/internal/platform"
)

// ompReceiptMaxBytes bounds each read and parse of the post-send suffix.
const ompReceiptMaxBytes = 256 << 10

// OMPReceipt records the opened journal's identity and pre-send end offset.
type OMPReceipt struct {
	path   string
	info   os.FileInfo
	offset int64
}

// OpenOMPReceipt captures a record boundary without reading retained history.
func OpenOMPReceipt(ctx context.Context, path string) (OMPReceipt, bool) {
	receipt, ok := runOMPReceiptIO(ctx, func() OMPReceipt {
		expected, err := os.Lstat(path)
		if err != nil || !expected.Mode().IsRegular() {
			return OMPReceipt{}
		}
		file, info := openOMPJournal(ctx, path, expected, platform.OpenRegularFileNoFollow)
		if file == nil {
			return OMPReceipt{}
		}
		defer file.Close()
		end := info.Size()
		if end > 0 {
			var boundary [1]byte
			if _, err := file.ReadAt(boundary[:], end-1); err != nil || boundary[0] != '\n' {
				return OMPReceipt{}
			}
		}
		if !ompJournalCurrent(ctx, path, info) {
			return OMPReceipt{}
		}
		return OMPReceipt{path: path, info: info, offset: end}
	})
	return receipt, ok && receipt.info != nil
}

// Count reads complete appended JSONL records from the pinned inode. confirm,
// when set, must also hold within this observation's I/O budget.
func (r OMPReceipt) Count(ctx context.Context, marker string, confirm func() bool) (int, bool) {
	if r.info == nil || marker == "" {
		return 0, false
	}
	count, ok := runOMPReceiptIO(ctx, func() int {
		file, info := openOMPJournal(ctx, r.path, r.info, platform.OpenRegularFileNoFollow)
		if file == nil {
			return -1
		}
		defer file.Close()
		size := info.Size() - r.offset
		if size < 0 || size > ompReceiptMaxBytes {
			return -1
		}
		data := make([]byte, int(size))
		if size != 0 {
			if _, err := file.ReadAt(data, r.offset); err != nil || data[len(data)-1] != '\n' {
				return -1
			}
		}
		n := 0
		markerBytes := []byte(marker)
		for len(data) > 0 {
			var line []byte
			line, data, _ = bytes.Cut(data, []byte{'\n'})
			line = bytes.TrimSpace(line)
			if ctx.Err() != nil || len(line) == 0 || line[0] != '{' || !json.Valid(line) {
				return -1
			}
			if piUserMarkerLine(line, markerBytes) {
				n++
			}
		}
		if ctx.Err() != nil || (confirm != nil && !confirm()) || !ompJournalCurrent(ctx, r.path, info) {
			return -1
		}
		return n
	})
	return count, ok && count >= 0
}

func openOMPJournal(ctx context.Context, path string, expected os.FileInfo, open func(string) (*os.File, error)) (*os.File, os.FileInfo) {
	if ctx.Err() != nil {
		return nil, nil
	}
	file, err := open(path)
	if err != nil {
		return nil, nil
	}
	if ctx.Err() == nil {
		info, err := file.Stat()
		if err == nil && info.Mode().IsRegular() && os.SameFile(expected, info) && ctx.Err() == nil {
			return file, info
		}
	}
	_ = file.Close()
	return nil, nil
}

func ompJournalCurrent(ctx context.Context, path string, opened os.FileInfo) bool {
	if ctx.Err() != nil {
		return false
	}
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && os.SameFile(opened, info) && info.Size() == opened.Size() && ctx.Err() == nil
}

// Regular-file syscalls cannot always be interrupted. Limit the process to
// one optional probe, with the worker owning its handles even after timeout.
// A stalled worker leaves later probes on the ordinary dispatch fallback.
var ompReceiptIO = make(chan struct{}, 1)

func runOMPReceiptIO[T any](ctx context.Context, work func() T) (T, bool) {
	var zero T
	if ctx.Err() != nil {
		return zero, false
	}
	select {
	case ompReceiptIO <- struct{}{}:
	default:
		return zero, false
	}
	done := make(chan T, 1)
	go func() {
		value := zero
		if ctx.Err() == nil {
			value = work()
		}
		<-ompReceiptIO
		done <- value
	}()
	select {
	case <-ctx.Done():
		return zero, false
	case value := <-done:
		return value, ctx.Err() == nil
	}
}
