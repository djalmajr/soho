package validation

import (
	"bufio"
	"errors"
	"io"
	"strings"
)

// ExercisedNothing reports whether an evidence log shows that a run tested
// nothing: it carries a marker of Go's test runner, "[no tests to run]" or
// "[no test files]", and no package line that ran (a line starting with "ok"
// without a marker). Anything else is not its call: a silent run is a pass,
// because go vet, gofmt and most linters print nothing when they succeed.
// Today this covers Go only; for another tool, the tester writes a command
// that fails when nothing runs. Only the first 64 KiB of a line are looked at,
// so a huge line cannot grow the memory, and the first line, the log's command
// header, is skipped so that a command quoting a marker is not taken for one.
// A read error is returned, never folded into a verdict.
func ExercisedNothing(r io.Reader) (bool, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	header := true
	lineStart := true
	marker := false
	for {
		chunk, more, err := br.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return marker, nil
			}
			return false, err
		}
		if lineStart && !header {
			line := string(chunk)
			switch {
			case strings.Contains(line, "[no tests to run]"), strings.Contains(line, "[no test files]"):
				marker = true
			case strings.HasPrefix(line, "ok"):
				return false, nil
			}
		}
		lineStart = !more
		if !more {
			header = false
		}
	}
}
