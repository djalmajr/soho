package doctor

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/validation"
)

// maxValidationBytes is the 1 MiB read bound for the check: the file is read
// with one byte of slack so an oversized file is detected, not truncated.
const maxValidationBytes = 1 << 20

// validationWarningCap is the maximum number of VALIDATION.md warnings the
// doctor prints per run; beyond it the last line counts the rest.
const validationWarningCap = 5

// doctorValidation checks the project's VALIDATION.md: the file must exist
// in the project root as a regular file, declare the "## Test layers"
// section with the required layers, and its layer lines must be filled. It
// warns and writes nothing, and it never repeats file content in the output:
// only line numbers and validated layer names. Under setup_target=local the
// repository is third-party and the check stays silent.
func doctorValidation(ctx *core.Config, env platform.Env, cwd string, s *Say) {
	if core.Cfg(ctx, "setup_target", "canonical", env) == "local" {
		return
	}
	path := filepath.Join(platform.ProjectRoot(env, cwd), "VALIDATION.md")
	// os.Stat follows the link. Only a not-exist error means the file is
	// absent (a broken link included); any other stat error — a symlink
	// loop, a denied traversal — is a check refusal with the short reason.
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			s.Warning("no VALIDATION.md in the project root: the tester creates it from the skill's templates/VALIDATION.md")
			return
		}
		s.Warning("could not check VALIDATION.md: " + shortReadReason(err))
		return
	}
	if !info.Mode().IsRegular() {
		s.Warning("could not check VALIDATION.md: not a regular file")
		return
	}
	// OpenRegularFile opens non-blocking and checks the opened handle, so a
	// replacement between the stat and the open cannot slip a FIFO or a
	// device in (a blocking open on a FIFO would hang the doctor).
	file, err := platform.OpenRegularFile(path)
	if err != nil {
		s.Warning("could not check VALIDATION.md: " + shortReadReason(err))
		return
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxValidationBytes+1))
	if err != nil {
		s.Warning("could not check VALIDATION.md: " + shortReadReason(err))
		return
	}
	if len(raw) > maxValidationBytes {
		s.Warning("could not check VALIDATION.md: exceeds 1 MiB")
		return
	}
	doc := validation.Parse(platform.DecodeText(raw))
	if !doc.HasLayers {
		s.Warning("VALIDATION.md has no \"## Test layers\" section: the tester adds it (see the skill's templates/VALIDATION.md)")
		return
	}
	var problems []string
	for _, n := range doc.Malformed {
		problems = append(problems, fmt.Sprintf("VALIDATION.md:%d: malformed layer line (use \"- <layer>: `<command>`\", \"- <layer>: n/a: <reason>\" or \"- <layer>: manual: <what runs it>\")", n))
	}
	for _, layer := range doc.Layers {
		if validation.IsPlaceholder(layer.Value) {
			problems = append(problems, fmt.Sprintf("VALIDATION.md:%d: layer %s still has the template placeholder", layer.Line, layer.Name))
		}
	}
	declared := make(map[string]bool, len(doc.Layers))
	for _, layer := range doc.Layers {
		declared[layer.Name] = true
	}
	for _, name := range validation.RequiredLayers {
		if !declared[name] {
			problems = append(problems, fmt.Sprintf("VALIDATION.md: layer %s is not declared (give its command, n/a with the reason, or manual)", name))
		}
	}
	if len(problems) == 0 {
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
		s.Ok(fmt.Sprintf("VALIDATION.md: %d commands, %d n/a, %d manual", commands, notApplicable, manual))
		return
	}
	if len(problems) > validationWarningCap {
		problems = append(problems[:validationWarningCap-1:validationWarningCap-1], fmt.Sprintf("VALIDATION.md: %d more problems", len(problems)-(validationWarningCap-1)))
	}
	for _, problem := range problems {
		s.Warning(problem)
	}
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
