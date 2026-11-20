package doctor

import (
	"fmt"
	"path/filepath"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	setupcmd "github.com/djalmajr/soho/internal/setup"
	"github.com/djalmajr/soho/internal/validation"
)

// doctorSetupMemo caches the local-setup decision per root for one doctor
// run: tracking a repository runs several git ls-files commands, and three
// checks ask the same question. The memo is created by doctorCheck for the
// run and never shared across runs.
type doctorSetupMemo struct {
	byRoot map[string]bool
}

func (m *doctorSetupMemo) local(ctx *core.Config, env platform.Env, cwd string) bool {
	root := platform.ProjectRoot(env, cwd)
	if value, ok := m.byRoot[root]; ok {
		return value
	}
	value := core.Cfg(ctx, "setup_target", "canonical", env) == "local" || setupcmd.ImplicitLocalSetupInEffect(root, ctx, env)
	if m.byRoot == nil {
		m.byRoot = make(map[string]bool)
	}
	m.byRoot[root] = value
	return value
}

// doctorSetupChecks keeps the instruction, validation and design checks on
// one memoized decision for the run.
func doctorSetupChecks(ctx *core.Config, env platform.Env, cwd string, s *Say, memo *doctorSetupMemo) {
	checkSetup(ctx, env, cwd, s, memo)
	doctorValidation(ctx, env, cwd, s, memo)
	doctorDesign(ctx, env, cwd, s, memo)
}

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
// only line numbers and validated layer names. Under a local setup — an
// explicit setup_target=local or a local setup written because the repository
// tracks no soho file — the repository is third-party and the check is
// silent.
func doctorValidation(ctx *core.Config, env platform.Env, cwd string, s *Say, memo *doctorSetupMemo) {
	if memo.local(ctx, env, cwd) {
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
