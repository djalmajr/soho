package validation

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/djalmajr/soho/internal/platform"
)

// maxValidationBytes is the 1 MiB read bound for the check: the file is read
// with one byte of slack so an oversized file is detected, not truncated.
const maxValidationBytes = 1 << 20

// malformedForms is the hint the malformed-line problem carries: the three
// accepted forms, in the doctor's exact text.
const malformedForms = "use \"- <layer>: `<command>`\", \"- <layer>: n/a: <reason>\" or \"- <layer>: manual: <what runs it>\""

// Result is the outcome of one read of a VALIDATION.md, shared by the
// doctor (which warns) and the validate command (which blocks).
type Result struct {
	// Problems holds the problems in the doctor's exact text: one
	// read-level problem when the file is absent or cannot be read, or the
	// parse-level problems (malformed lines, placeholders, missing required
	// layers) when it can.
	Problems []string
	// Doc is the parsed document, valid only when Problems is empty.
	Doc Document
	// OK is the doctor's ok line (the layer counts), valid only when
	// Problems is empty.
	OK string
}

// notRegularFile is the short problem text for a path that is not a
// regular file (a FIFO, a socket, a device or a directory): the doctor's
// exact text, reached either through the pre-stat mode or through
// platform.ErrNotRegular on the opened handle.
const notRegularFile = "not a regular file"

// Check reads the project's VALIDATION.md at path and returns the shared
// problem list (DJA-695 fatia 3, decision 7). The file must exist as a
// regular file and stay under 1 MiB: os.Stat follows the link, so only a
// not-exist error means the file is absent (a broken link included), and
// any other stat error — a symlink loop, a denied traversal — is a refusal
// with the short reason. The open goes through platform.OpenRegularFile,
// which opens non-blocking and checks the opened handle, so a replacement
// between the stat and the open cannot slip a FIFO or a device in, and the
// bytes decode through platform.DecodeText (invalid UTF-8 becomes the
// replacement character, CRLF becomes LF). The read-level problems replace
// the parse-level ones (a file that cannot be read has no parse). Nothing
// is ever written, and the file's content never leaves the function except
// through the validated layer names in the placeholder problems.
func Check(path string) Result {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Result{Problems: []string{"no VALIDATION.md in the project root: the tester creates it from the skill's templates/VALIDATION.md"}}
		}
		return Result{Problems: []string{"could not check VALIDATION.md: " + shortReadReason(err)}}
	}
	if !info.Mode().IsRegular() {
		return Result{Problems: []string{"could not check VALIDATION.md: " + notRegularFile}}
	}
	file, err := platform.OpenRegularFile(path)
	if err != nil {
		return Result{Problems: []string{"could not check VALIDATION.md: " + shortCheckReason(err)}}
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxValidationBytes+1))
	if err != nil {
		return Result{Problems: []string{"could not check VALIDATION.md: " + shortReadReason(err)}}
	}
	if len(raw) > maxValidationBytes {
		return Result{Problems: []string{"could not check VALIDATION.md: exceeds 1 MiB"}}
	}
	doc := Parse(platform.DecodeText(raw))
	if !doc.HasLayers {
		return Result{Problems: []string{`VALIDATION.md has no "## Test layers" section: the tester adds it (see the skill's templates/VALIDATION.md)`}}
	}
	var problems []string
	for _, n := range doc.Malformed {
		problems = append(problems, fmt.Sprintf("VALIDATION.md:%d: malformed layer line (%s)", n, malformedForms))
	}
	for _, layer := range doc.Layers {
		if IsPlaceholder(layer.Value) {
			problems = append(problems, fmt.Sprintf("VALIDATION.md:%d: layer %s still has the template placeholder", layer.Line, layer.Name))
		}
	}
	declared := make(map[string]bool, len(doc.Layers))
	for _, layer := range doc.Layers {
		declared[layer.Name] = true
	}
	for _, name := range RequiredLayers {
		if !declared[name] {
			problems = append(problems, fmt.Sprintf("VALIDATION.md: layer %s is not declared (give its command, n/a with the reason, or manual)", name))
		}
	}
	if len(problems) > 0 {
		return Result{Problems: problems}
	}
	var commands, notApplicable, manual int
	for _, layer := range doc.Layers {
		switch layer.Kind {
		case "command":
			commands++
		case "na":
			notApplicable++
		case "manual":
			manual++
		}
	}
	return Result{Doc: doc, OK: fmt.Sprintf("VALIDATION.md: %d commands, %d n/a, %d manual", commands, notApplicable, manual)}
}

// shortReadReason reduces a read error to its short reason — the failure
// message without the path — so the output names the cause, never the file
// content.
func shortReadReason(err error) string {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err.Error()
	}
	return err.Error()
}

// shortCheckReason reduces the open's refusal to its short reason:
// platform.ErrNotRegular is the not-regular-file text, and any other error
// goes through shortReadReason (the path never appears).
func shortCheckReason(err error) string {
	if errors.Is(err, platform.ErrNotRegular) {
		return notRegularFile
	}
	return shortReadReason(err)
}
