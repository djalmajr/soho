package doctor

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/validation"
)

// designReadLimit is the 1 MiB read bound of the DESIGN.md check. A longer file
// is not read: its size alone is the diagnostic, because a count or a front
// matter taken from a part of it would claim something about the rest.
const designReadLimit = 1 << 20

// designLineCap is the DESIGN.md line count above which the detail belongs
// under design/ instead of the root file.
const designLineCap = 400

// designFrontMatter is the first-level view of the DESIGN.md front matter:
// it is the region between the two "---" lines that open the file, read as
// first-level keys only (a line with no leading whitespace). Nothing of the
// YAML beyond key names is interpreted.
type designFrontMatter struct {
	ok            bool // the file opens with --- and the region closes
	hasName       bool
	hasColors     bool
	hasTypography bool
}

// parseDesignFrontMatter returns the first-level keys of the front matter:
// a BOM at the start is stripped, CRLF is already normalized to LF by the
// caller, the first line must be exactly "---", the region ends at the next
// exact "---" line, and a key only counts when its line carries no leading
// whitespace (an indented line is a nested value, never a first-level key).
func parseDesignFrontMatter(text string) designFrontMatter {
	if strings.HasPrefix(text, "\uFEFF") {
		text = text[len("\uFEFF"):]
	}
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || lines[0] != "---" {
		return designFrontMatter{}
	}
	var fm designFrontMatter
	closed := false
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			closed = true
			break
		}
		line := lines[i]
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		key, _, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		switch key {
		case "name":
			fm.hasName = true
		case "colors":
			fm.hasColors = true
		case "typography":
			fm.hasTypography = true
		}
	}
	if !closed {
		return designFrontMatter{}
	}
	fm.ok = true
	return fm
}

// designLineCount is the number of lines of the (CRLF-normalized) text: an
// empty text has none, and a final line without a trailing newline still
// counts.
func designLineCount(text string) int {
	if text == "" {
		return 0
	}
	n := strings.Count(text, "\n")
	if !strings.HasSuffix(text, "\n") {
		n++
	}
	return n
}

// doctorDesign checks the project's DESIGN.md (brief DJA-695 fatia 2,
// decision 1): it is the presence-driven mirror of doctorValidation, with
// the same warning cap and the same early return under setup_target=local.
// It speaks only when DESIGN.md exists in the project root as a regular
// file: no file, a link and a directory are all silence (a project without
// the file is not demanded), and nothing is ever written. The front matter
// is read as first-level keys between the two opening "---" lines (no YAML
// beyond key names), the read is bounded to 1 MiB, and the design layer of
// the VALIDATION.md is looked up through the shared reader
// (validation.Parse): when VALIDATION.md is absent, its own warning
// already exists and is not repeated.
func doctorDesign(ctx *core.Config, env platform.Env, cwd string, s *Say) {
	if core.Cfg(ctx, "setup_target", "canonical", env) == "local" {
		return
	}
	root := platform.ProjectRoot(env, cwd)
	path := filepath.Join(root, "DESIGN.md")
	// Absent, a link, not regular or not this user's: silence. The read below
	// refuses a link at the open itself, so a swap after this stat is no way in.
	info, err := core.StatScratchReport(path)
	if err != nil {
		return
	}
	var problems []string
	if info.Size() > designReadLimit {
		problems = append(problems, "DESIGN.md is over 1 MiB: move the detail to design/<name>.md, read only when that part is touched")
	} else {
		raw, readErr := core.ReadScratchReportLimited(path, designReadLimit)
		if readErr != nil {
			return
		}
		text := platform.DecodeText(raw)
		fm := parseDesignFrontMatter(text)
		if !fm.ok || !fm.hasName {
			problems = append(problems, "DESIGN.md has no front matter with a name: fill it from the skill's templates/DESIGN.md")
		}
		if fm.ok && !fm.hasColors && !fm.hasTypography {
			problems = append(problems, "DESIGN.md declares no colors or typography tokens: without them the design layer has nothing to check")
		}
		if n := designLineCount(text); n > designLineCap {
			problems = append(problems, fmt.Sprintf("DESIGN.md has %d lines: move the detail to design/<name>.md, read only when that part is touched", n))
		}
	}
	if wellFormed, hasDesign := validationDesignState(root); wellFormed && !hasDesign {
		problems = append(problems, "VALIDATION.md has no design layer, and the project has a DESIGN.md: declare one, 'manual: <what runs it>' until it passes")
	}

	if len(problems) == 0 {
		s.Ok("design: DESIGN.md")
		return
	}
	// The cap mirrors doctorValidation: with at most four problems it never
	// fires today, but the shape (cap, then the count of the rest) is the
	// same one.
	if len(problems) > validationWarningCap {
		problems = append(problems[:validationWarningCap-1:validationWarningCap-1], fmt.Sprintf("DESIGN.md: %d more problems", len(problems)-(validationWarningCap-1)))
	}
	for _, problem := range problems {
		s.Warning(problem)
	}
}

// validationDesignState reads the project's VALIDATION.md through the bounded
// read and the shared reader (validation.Parse, which accepts the command,
// manual and n/a forms). It opens the file the way validation.Check does, so a
// link to a regular file counts here as it counts there. wellFormed is false
// when the file is absent, not regular or too long, when it has no layers
// section, or when a layer line is malformed: each of those has its own
// VALIDATION.md warning, and with a broken file the design question cannot be
// answered. hasDesign is meaningful only when wellFormed is true.
func validationDesignState(root string) (wellFormed bool, hasDesign bool) {
	file, err := platform.OpenRegularFile(filepath.Join(root, "VALIDATION.md"))
	if err != nil {
		return false, false
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, designReadLimit+1))
	if err != nil || len(raw) > designReadLimit {
		return false, false
	}
	doc := validation.Parse(platform.DecodeText(raw))
	if !doc.HasLayers || len(doc.Malformed) > 0 {
		return false, false
	}
	for _, layer := range doc.Layers {
		if layer.Name == "design" {
			return true, true
		}
	}
	return true, false
}
