package doctor

import (
	"fmt"
	"path/filepath"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/validation"
)

// validationWarningCap is the maximum number of VALIDATION.md warnings the
// doctor prints per run; beyond it the last line counts the rest. The cap
// is the doctor's display choice: the shared read (validation.Check)
// returns every problem, and the validate command prints them all.
const validationWarningCap = 5

// doctorValidation checks the project's VALIDATION.md through the shared
// read (validation.Check, DJA-695 fatia 3, decision 7): the file must
// exist in the project root as a regular file, declare the "## Test layers"
// section with the required layers, and its layer lines must be filled. It
// warns and writes nothing, and it never repeats file content in the output:
// only line numbers and validated layer names. Under setup_target=local the
// repository is third-party and the check stays silent.
func doctorValidation(ctx *core.Config, env platform.Env, cwd string, s *Say) {
	if core.Cfg(ctx, "setup_target", "canonical", env) == "local" {
		return
	}
	path := filepath.Join(platform.ProjectRoot(env, cwd), "VALIDATION.md")
	check := validation.Check(path)
	if len(check.Problems) == 0 {
		s.Ok(check.OK)
		return
	}
	problems := check.Problems
	if len(problems) > validationWarningCap {
		problems = append(problems[:validationWarningCap-1:validationWarningCap-1], fmt.Sprintf("VALIDATION.md: %d more problems", len(problems)-(validationWarningCap-1)))
	}
	for _, problem := range problems {
		s.Warning(problem)
	}
}
