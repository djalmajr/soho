// Package taskreport maintains stable copies of task reports referenced by state pointers.
package taskreport

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
)

// TaskReportPointerPath returns the per-agent pointer file path.
func TaskReportPointerPath(sd, agent string) string {
	return filepath.Join(sd, "task-report-"+agent+".json")
}

// ReadTaskReportPointer returns a valid version 1 pointer or nil for absent or invalid files.
func ReadTaskReportPointer(sd, agent string) *jsonjs.Object {
	data, err := platform.ReadTextFile(TaskReportPointerPath(sd, agent))
	if err != nil {
		return nil
	}
	value, err := jsonjs.Parse([]byte(data))
	if err != nil {
		return nil
	}
	pointer, ok := value.(*jsonjs.Object)
	if !ok || !validPointer(pointer) {
		return nil
	}
	return pointer
}

func validPointer(pointer *jsonjs.Object) bool {
	version, hasVersion := pointer.Get("version")
	report, hasReport := pointer.Get("task_report")
	current, hasCurrent := pointer.Get("current")
	history, hasHistory := pointer.Get("history")
	if !hasVersion || !hasReport || !hasCurrent || !hasHistory || report == nil || current == nil {
		return false
	}
	if version != float64(1) {
		if integer, ok := version.(int); !ok || integer != 1 {
			return false
		}
	}
	if _, ok := report.(string); !ok {
		return false
	}
	if _, ok := current.(string); !ok {
		return false
	}
	items, ok := history.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		if _, ok := item.(string); !ok {
			return false
		}
	}
	return true
}

// WriteTaskReportPointer atomically writes pointer as JavaScript-compatible JSON.
func WriteTaskReportPointer(sd, agent string, pointer any) error {
	// Pane is optional origin metadata; layout changes do not invalidate it.
	if obj, ok := pointer.(*jsonjs.Object); ok {
		if _, present := obj.Get("pane"); !present {
			if fields := strings.Split(core.RosterLine(sd, agent), "\t"); len(fields) > 1 && fields[1] != "" {
				obj.Set("pane", fields[1])
			}
		}
	}
	content := jsonjs.Stringify(pointer)
	if content == "" {
		return errors.New("taskreport: pointer is not JSON-serializable")
	}
	return platform.AtomicWrite(TaskReportPointerPath(sd, agent), content+"\n")
}

// RetireTaskReportPointer ends the active task while retaining its closure
// evidence for legacy sidecars. Repeating retirement leaves history unchanged.
func RetireTaskReportPointer(sd, agent string) error {
	pointer := ReadTaskReportPointer(sd, agent)
	if pointer == nil {
		return nil
	}
	currentValue, _ := pointer.Get("current")
	current := currentValue.(string)
	if current == "" {
		return nil
	}
	historyValue, _ := pointer.Get("history")
	history := append(historyValue.([]any), current)
	pointer.Set("history", history)
	pointer.Set("current", "")
	return WriteTaskReportPointer(sd, agent, pointer)
}

func readNonEmpty(file string) []byte {
	content, err := os.ReadFile(file)
	if err != nil || len(content) == 0 {
		return nil
	}
	return content
}

// SyncTaskReport copies the current (or mirrored) non-empty report to its stable path.
func SyncTaskReport(sd, agent string) (string, error) {
	pointer := ReadTaskReportPointer(sd, agent)
	if pointer == nil {
		return "", nil
	}
	stableValue, _ := pointer.Get("task_report")
	currentValue, _ := pointer.Get("current")
	stable := stableValue.(string)
	current := currentValue.(string)
	if current == "" {
		// A retired pointer (release, reopen, fresh spawn) has no open task:
		// the stable copy of its last report stays as it is.
		return "", nil
	}
	content := readNonEmpty(current)
	if content == nil {
		mirror := filepath.Join(sd, "reports", filepath.Base(current))
		mirrorPath, mirrorErr := filepath.Abs(mirror)
		currentPath, currentErr := filepath.Abs(current)
		if mirrorErr != nil || currentErr != nil || filepath.Clean(mirrorPath) != filepath.Clean(currentPath) {
			content = readNonEmpty(mirror)
		}
	}
	if content == nil {
		if err := os.Remove(stable); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		return "", nil
	}
	previous, err := os.ReadFile(stable)
	if err != nil || !bytes.Equal(previous, content) {
		if err := platform.AtomicWrite(stable, string(content)); err != nil {
			return "", err
		}
	}
	return stable, nil
}
