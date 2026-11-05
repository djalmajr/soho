package skill

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestBundlePublishesExactResourcesWithoutEngines(t *testing.T) {
	cache := t.TempDir()
	dir, err := Materialize(cache)
	if err != nil {
		t.Fatal(err)
	}
	files, _, err := bundledFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(file.name)))
		if err != nil || !bytes.Equal(data, file.data) {
			t.Fatalf("resource %s: err=%v", file.name, err)
		}
	}
	for _, name := range []string{"scripts", "assets.go", "assets_test.go"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("bundle contains an engine or build source %s: %v", name, err)
		}
	}
	again, err := Materialize(cache)
	if err != nil || again != dir {
		t.Fatalf("verified reuse: %q %v", again, err)
	}
}

func TestBundleRefusesCorruptionWithoutOverwriting(t *testing.T) {
	for _, kind := range []string{"changed", "missing", "extra", "extra-directory"} {
		t.Run(kind, func(t *testing.T) {
			cache := t.TempDir()
			dir, err := Materialize(cache)
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, "config.defaults")
			switch kind {
			case "changed":
				err = os.WriteFile(file, []byte("foreign content"), 0o600)
			case "missing":
				err = os.Remove(file)
			case "extra":
				err = os.WriteFile(filepath.Join(dir, "foreign"), []byte("keep"), 0o600)
			case "extra-directory":
				err = os.Mkdir(filepath.Join(dir, "foreign"), 0o700)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Materialize(cache); err == nil {
				t.Fatal("corrupt cache was accepted")
			}
			if kind == "changed" {
				data, err := os.ReadFile(file)
				if err != nil || string(data) != "foreign content" {
					t.Fatalf("foreign content overwritten: %q %v", data, err)
				}
			}
		})
	}
}

func TestBundleRefusesSymlinkedCacheChild(t *testing.T) {
	cache, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(cache, "soho")); err != nil {
		// Native Windows symlinks need an OS permission; this is a platform
		// capability result, never permission to weaken the production check.
		t.Skipf("native symlink creation denied: %v", err)
	}
	if _, err := Materialize(cache); err == nil {
		t.Fatal("symlinked cache child accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("wrote outside cache: %v %v", entries, err)
	}
}

func TestBundleShipsTheValidationTemplateWithItsHeadings(t *testing.T) {
	// DJA-695: templates/VALIDATION.md is part of the bundle and keeps the
	// section order and the exact headings the delivery contract defines.
	data, err := resources.ReadFile("templates/VALIDATION.md")
	if err != nil {
		t.Fatalf("bundle is missing templates/VALIDATION.md: %v", err)
	}
	lines := strings.Split(string(data), "\n")
	headings := []string{"# Validation", "## Scope", "## Preconditions", "## Test layers", "## Items", "## Evidence", "## Results", "## What this does not prove"}
	next := -1
	for _, heading := range headings {
		i := -1
		for j, line := range lines {
			if j > next && line == heading {
				i = j
				break
			}
		}
		if i < 0 {
			t.Fatalf("template lost the heading %q (or lost it out of order)", heading)
		}
		next = i
	}
	for _, layer := range []string{"- unit:", "- integration:", "- contract:", "- e2e:", "- mutation:"} {
		if !strings.Contains(string(data), layer) {
			t.Fatalf("template lost the %s layer line", layer)
		}
	}
}

func TestCriticalImplementerKeepsTheImplementerBlocks(t *testing.T) {
	// DJA-686: the shipped critical-implementer keeps the implementer's
	// blocks byte-for-byte (the test fails if they drift) and adds the
	// critical self-review; it names no project, machine path, team or model.
	base := roleBody(t, "implementer.md")
	crit := roleBody(t, "critical-implementer.md")
	if !strings.Contains(crit, base) {
		t.Fatal("critical-implementer lost or altered an implementer block")
	}
	if !strings.Contains(crit, "<critical>") || !strings.Contains(crit, "</critical>") {
		t.Fatal("missing the critical self-review block")
	}
	for _, needle := range []string{"interleaves", "idempotency", "swallowed", "The red test existed before the fix"} {
		if !strings.Contains(crit, needle) {
			t.Fatalf("critical block missing %q", needle)
		}
	}
	for _, banned := range []string{"Skedly", "Kashes", "zommehq", "/Users/", "gpt-"} {
		if strings.Contains(crit, banned) {
			t.Fatalf("critical-implementer names %q (machine- or team-specific)", banned)
		}
	}
}

func TestBundleShipsTheDesignTemplateWithItsContract(t *testing.T) {
	// DJA-695: templates/DESIGN.md is part of the bundle, in the format of
	// the DESIGN.md specification: the delimited front matter with the
	// required keys, the prose sections in the specification's order, and
	// the token layers and specs sections after them.
	data, err := resources.ReadFile("templates/DESIGN.md")
	if err != nil {
		t.Fatalf("bundle is missing templates/DESIGN.md: %v", err)
	}
	lines := strings.Split(string(data), "\n")
	if lines[0] != "---" {
		t.Fatal("template does not open with the front matter fence")
	}
	close := -1
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			close = i
			break
		}
	}
	if close < 0 {
		t.Fatal("the front matter fence never closes")
	}
	fm := strings.Join(lines[1:close], "\n")
	for _, key := range []string{"version:", "name:", "description:", "colors:", "typography:", "rounded:", "spacing:"} {
		if !strings.Contains(fm, key) {
			t.Fatalf("front matter lost the %s key", key)
		}
	}
	body := strings.Join(lines[close+1:], "\n")
	for _, needle := range []string{"fills this file reading", "never copies", "no `<...>` behind"} {
		if !strings.Contains(body, needle) {
			t.Fatalf("opening text lost %q", needle)
		}
	}
	headings := []string{"## Overview", "## Colors", "## Typography", "## Layout", "## Elevation & Depth", "## Shapes", "## Components", "## Do's and Don'ts", "## Token layers", "## Specs"}
	next := -1
	for _, heading := range headings {
		i := -1
		for j, line := range lines {
			if j > next && j > close && line == heading {
				i = j
				break
			}
		}
		if i < 0 {
			t.Fatalf("template lost the heading %q (or lost it out of order)", heading)
		}
		next = i
	}
	layersSection := ""
	for i, line := range lines {
		if line == "## Token layers" {
			layersSection = strings.Join(lines[i+1:], "\n")
			break
		}
	}
	for _, needle := range []string{"origin", "project alias", "component"} {
		if !strings.Contains(layersSection, needle) {
			t.Fatalf("Token layers section lost %q", needle)
		}
	}
}

func TestDesignModelAndLintReferenceNameNoProjectOrMachine(t *testing.T) {
	// DJA-695: the model and the lint reference ship generic English text:
	// no project name, no machine path, no number of the sources.
	for _, path := range []string{"templates/DESIGN.md", "references/design-lint.md"} {
		data, err := resources.ReadFile(path)
		if err != nil {
			t.Fatalf("bundle is missing %s: %v", path, err)
		}
		for _, banned := range []string{"downtv", "cinzel", "dukto", "yatta", "kashes", "skedly", "pinar", "ai-memory", "zommehq", "apigate", "/Users/", "/home/", `C:\Users`, "DJA-", "w6X"} {
			if strings.Contains(string(data), banned) {
				t.Fatalf("%s names %q (project-, machine- or source-specific)", path, banned)
			}
		}
	}
}

func TestDesignerCoversTheDesignFileAndThePrototypePaths(t *testing.T) {
	// DJA-695: the designer names the model, the absent-file cases, the
	// per-component spec path, the prototype paths (the .fig and the
	// static HTML) and the rule against opening the application on its own.
	body := roleBody(t, "designer.md")
	for _, needle := range []string{
		"templates/DESIGN.md",
		"open question",
		"design/<name>.md",
		"prototype-<slice>.fig",
		"prototype-<slice>.html",
		"Never open the application",
		"timeout",
		"prototype: <path>",
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("designer lost %q", needle)
		}
	}
	if !strings.Contains(body, ".fig") {
		t.Fatal("designer lost the .fig case")
	}
}

func TestTesterDeclaresTheDesignLayerWithoutNamingAStack(t *testing.T) {
	// DJA-695: with a DESIGN.md at the root the tester declares the design
	// layer without starting a red gate, and the role points at the skill's
	// lint reference instead of naming a stack.
	body := roleBody(t, "tester.md")
	for _, needle := range []string{
		"DESIGN.md",
		"the `design` layer",
		"references/design-lint.md",
		"A red gate never starts",
		"the correct token",
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("tester lost %q", needle)
		}
	}
	for _, banned := range []string{"shadcn", "Tailwind", "React", "Vue", "Svelte", "Biome"} {
		if strings.Contains(body, banned) {
			t.Fatalf("tester names the stack %q (the reference does that)", banned)
		}
	}
}

// TestReviewResponseRolesCarryTheRule (DJA-684 slice C, scenario 15): the
// four edit roles carry the review-response report rule in their report
// block, with the same text, and the critical parity test keeps passing.
func TestReviewResponseRolesCarryTheRule(t *testing.T) {
	const rule = "When the brief names a review with a `Review:` line, the report carries a `## Review responses` section with one line per finding of that review, in one of the exact forms `- F<n>: fixed`, `- F<n>: fixed: <note>`, or `- F<n>: declined: <reason>` — silence is not an answer, `fixed` only after the proof ran, and `declined` with the reason. The `wait` counts the findings left without a response and warns; it only warns: it never blocks and never runs anything."
	for _, name := range []string{"implementer.md", "critical-implementer.md", "tester.md", "designer.md"} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(roleBody(t, name), rule) {
				t.Fatalf("%s lost the review-response report rule", name)
			}
		})
	}
}

// TestEditRolesListChangedFilesUnderFilesChanged (DJA-666): every edit role
// tells the worker the changed-file list belongs in a `## Files changed`
// section of the report, with the reason the wait audit reads only that
// section; the critical parity test keeps passing because the implementer
// text is the shared one.
func TestEditRolesListChangedFilesUnderFilesChanged(t *testing.T) {
	const core = "The `wait` reads only the `## Files changed` section when it checks the report's changed-file claims against the recorded tree, so a file the report merely reads or cites elsewhere is never flagged."
	for _, name := range []string{"implementer.md", "critical-implementer.md", "tester.md", "tasker.md", "designer.md", "documenter.md"} {
		t.Run(name, func(t *testing.T) {
			body := roleBody(t, name)
			if !strings.Contains(body, "## Files changed") {
				t.Fatalf("%s lost the Files changed section instruction", name)
			}
			if !strings.Contains(body, core) {
				t.Fatalf("%s lost the Files changed reason", name)
			}
		})
	}
}

// roleBody returns the markdown body of a shipped role file (everything
// after its closing frontmatter ---).
func roleBody(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("roles", name))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || lines[0] != "---" {
		t.Fatalf("%s: missing frontmatter", name)
	}
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			return strings.Join(lines[i+1:], "\n")
		}
	}
	t.Fatalf("%s: unterminated frontmatter", name)
	return ""
}

func TestBundleConcurrentPublishCleansOwnedStages(t *testing.T) {
	cache := t.TempDir()
	var wg sync.WaitGroup
	results := make(chan string, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dir, err := Materialize(cache)
			if err != nil {
				t.Errorf("concurrent materialize: %v", err)
			}
			results <- dir
		}()
	}
	wg.Wait()
	close(results)
	var want string
	for dir := range results {
		if want == "" {
			want = dir
		} else if dir != want {
			t.Errorf("different published paths: %q / %q", dir, want)
		}
	}
	entries, err := os.ReadDir(filepath.Join(cache, "soho", "bundled"))
	if err != nil || len(entries) != 1 || strings.HasPrefix(entries[0].Name(), ".staging-") {
		t.Fatalf("left owned staging directories: %v %v", entries, err)
	}
}

func TestBundleInvalidCacheDoesNotWrite(t *testing.T) {
	for _, path := range []string{"", "relative-cache"} {
		if _, err := Materialize(path); err == nil {
			t.Fatalf("accepted invalid cache %q", path)
		}
	}
}

func TestBundleCachedIsStrictlyReadOnly(t *testing.T) {
	t.Run("a missing cache or bundle is a not-exist error", func(t *testing.T) {
		cache := t.TempDir()
		if _, err := Cached(filepath.Join(cache, "missing")); !os.IsNotExist(err) {
			t.Fatalf("missing cache: err=%v; want os.IsNotExist", err)
		}
		if _, err := Cached(cache); !os.IsNotExist(err) {
			t.Fatalf("cache without a bundle: err=%v; want os.IsNotExist", err)
		}
	})
	t.Run("a completed bundle is found without creating or repairing anything", func(t *testing.T) {
		cache := t.TempDir()
		dir, err := Materialize(cache)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Cached(cache)
		if err != nil || got != dir {
			t.Fatalf("Cached() = %q %v; want %q", got, err, dir)
		}
		// The lookup must not write: with the cache read-only it still
		// succeeds, while any mkdir/write/repair would fail.
		if err := os.Chmod(cache, 0o500); err != nil {
			t.Skip("the cache cannot be made read-only on this platform")
		}
		t.Cleanup(func() { _ = os.Chmod(cache, 0o700) })
		if _, err := Cached(cache); err != nil {
			t.Fatalf("Cached on a read-only cache: %v; the lookup must not write", err)
		}
	})
	t.Run("corrupt content fails closed without repair", func(t *testing.T) {
		cache := t.TempDir()
		dir, err := Materialize(cache)
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(dir, "config.defaults")
		if err := os.WriteFile(file, []byte("foreign"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Cached(cache); err == nil || os.IsNotExist(err) {
			t.Fatalf("corrupt cache: err=%v; want a fail-closed error, not not-exist", err)
		}
		data, err := os.ReadFile(file)
		if err != nil || string(data) != "foreign" {
			t.Fatalf("Cached repaired the cache: %q %v", data, err)
		}
	})
	t.Run("a symlinked bundle is refused", func(t *testing.T) {
		cache, outside := t.TempDir(), t.TempDir()
		good, err := Materialize(outside)
		if err != nil {
			t.Fatal(err)
		}
		_, digest, err := bundledFiles()
		if err != nil {
			t.Fatal(err)
		}
		parent := filepath.Join(cache, "soho", "bundled")
		if err := os.MkdirAll(parent, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(good, filepath.Join(parent, digest)); err != nil {
			// Native Windows symlinks need an OS permission; this is a
			// platform capability result, never permission to weaken the check.
			t.Skipf("native symlink creation denied: %v", err)
		}
		if _, err := Cached(cache); err == nil {
			t.Fatal("symlinked bundle accepted")
		}
	})
}

// TestReviewRolesCarryTheFindingMarks (DJA-684 slice B, scenario 13): the
// five review roles carry the exact finding-mark form and the two new
// rules; implementer and critical-implementer are untouched (the byte
// parity test keeps them matching).
func TestReviewRolesCarryTheFindingMarks(t *testing.T) {
	for _, name := range []string{"reviewer", "security-reviewer", "validator", "ui-reviewer", "inspector"} {
		t.Run(name, func(t *testing.T) {
			body := roleBody(t, name+".md")
			for _, needle := range []string{
				`[F<n>]`,
				"starting at 1",
				`[origin: brief|impl|unknown]`,
				"does not name the implementer's brief",
				`[class: <slug>]`,
				"the class of the error, not the symptom",
				`^[a-z0-9][a-z0-9-]{0,40}$`,
				"race-read-write",
				"missing-bound",
				"follows-link",
				"error-swallowed",
				"doc-drift",
				"the list is open",
				"list the places in one finding",
				`declined: <reason>`,
				"new evidence",
			} {
				if !strings.Contains(body, needle) {
					t.Fatalf("%s.md missing the finding-marks %s", name, needle)
				}
			}
		})
	}
}

// TestResearchRolesCarryTheBugVerdict (DJA-684 slice B, scenario 13): the
// research roles carry the bug/symptom verdict block.
func TestResearchRolesCarryTheBugVerdict(t *testing.T) {
	for _, name := range []string{"researcher", "scouter"} {
		t.Run(name, func(t *testing.T) {
			body := roleBody(t, name+".md")
			for _, needle := range []string{
				"a bug or a symptom",
				"verdict: one-off|pattern|known|needs-info",
				"the cause",
				"the trigger",
				"the discarded hypotheses",
				"the level of each piece of evidence",
			} {
				if !strings.Contains(body, needle) {
					t.Fatalf("%s.md missing the bug verdict %s", name, needle)
				}
			}
		})
	}
}

// DJA-693: the shipped mcp choices of the roles. ui-reviewer,
// sub-orchestrator and planner carry mcp: none; designer carries the
// open-pencil list and inspector the two browser servers. job-orchestrator
// keeps inheriting (it reads memory only when the brief lists the scopes).
func TestShippedRolesCarryTheMcpChoice(t *testing.T) {
	want := map[string]string{
		"ui-reviewer":      "none",
		"sub-orchestrator": "none",
		"planner":          "none",
		"designer":         "open-pencil",
		"inspector":        "playwright,chrome-devtools",
	}
	for role, value := range want {
		if got := roleFm(t, role+".md", "mcp"); got != value {
			t.Fatalf("%s mcp frontmatter=%q, want %q", role, got, value)
		}
	}
	if got := roleFm(t, "job-orchestrator.md", "mcp"); got != "" {
		t.Fatalf("job-orchestrator gained a mcp key %q, want none (it inherits)", got)
	}
}

// roleFm returns one frontmatter key of a shipped role file ("" when absent).
func roleFm(t *testing.T, file, key string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("roles", file))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || lines[0] != "---" {
		t.Fatalf("%s: missing frontmatter", file)
	}
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			return ""
		}
		if line, ok := strings.CutPrefix(lines[i], key+": "); ok {
			return strings.TrimSpace(line)
		}
	}
	t.Fatalf("%s: unterminated frontmatter", file)
	return ""
}

func TestBundleBriefTemplateCitesTheAuthorMeasurement(t *testing.T) {
	// DJA-684: the template's Worktree: line says the line feeds the review
	// author measurement and belongs in both briefs, the project root when
	// the slice has no worktree of its own.
	data, err := resources.ReadFile("templates/brief.md")
	if err != nil {
		t.Fatalf("bundle is missing templates/brief.md: %v", err)
	}
	lines := strings.Split(string(data), "\n")
	var worktreeLine string
	for _, line := range lines {
		if strings.HasPrefix(line, "Worktree:") {
			worktreeLine = line
			break
		}
	}
	if worktreeLine == "" {
		t.Fatal("template lost its Worktree: line")
	}
	for _, want := range []string{"author-model", "implementer", "root"} {
		if !strings.Contains(worktreeLine, want) {
			t.Fatalf("Worktree: line must cite the author measurement and the root declaration, lost %q: %s", want, worktreeLine)
		}
	}
}

func TestBundleBriefTemplateCarriesTheTypeSections(t *testing.T) {
	// DJA-685: the template carries the per-Type sections and the Mutations
	// section, each with its deletion comment and the marker meanings; a
	// filled brief generated from it (every section kept, a Type: line
	// added) produces no warning from this slice.
	data, err := resources.ReadFile("templates/brief.md")
	if err != nil {
		t.Fatalf("bundle is missing templates/brief.md: %v", err)
	}
	body := string(data)
	for _, want := range []string{
		"## Risk markers (Type: backend)",
		"<!-- Delete this section for every Type but backend. -->",
		"## Input and response (Type: security)",
		"<!-- Delete this section for every Type but security. -->",
		"## UI lifecycle (Type: ui)",
		"<!-- Delete this section for every Type but ui. -->",
		"## Mutations (Type: backend or security)",
		"<!-- Delete this section for every Type but backend and security. -->",
		"- [boundary] limits, empty input, sizes, encodings at the edge of the slice",
		"- [lifetime] what the slice opens or starts, who closes it, and what happens on timeout",
		"- [shared] state other processes or callers touch, and how it stays consistent",
		"- [identity] who owns each thing, how the caller is checked, and what a reused id does",
		"- [hostile] input built to break it: links, paths outside the tree, control characters",
		"- [async] loading, late, failed and repeated states",
		"- [test-env] what the test environment does not reproduce",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("template lost %q", want)
		}
	}
	// The filled-brief proof (no warning from this slice) runs in
	// internal/dispatch, where the lint is importable without a cycle.
	if !strings.Contains(body, "An unfinished item is [skipped], never silence.") {
		t.Fatal("template Report section lost the skipped-item rule")
	}
}

// DJA-683: the "soho" GitHub label does not exist in the feedback repo, so
// the issue template and the skill instructions must not ask for it; the
// brief template must not name this repository or its Go commands, and the
// orchestration reference must not claim the removed model-family rule.
func TestSkillAssetsDropTheSoHoIssueLabelAndRepoLeftovers(t *testing.T) {
	cases := []struct {
		path   string
		absent []string
	}{
		{"templates/issue.md", []string{"labels: soho", "--label soho"}},
		{"SKILL.md", []string{"--label soho"}},
		{"templates/brief.md", []string{"herdr-soho", "go test"}},
		{"references/orchestration.md", []string{"different model families"}},
	}
	for _, tc := range cases {
		data, err := resources.ReadFile(tc.path)
		if err != nil {
			t.Fatalf("bundle is missing %s: %v", tc.path, err)
		}
		for _, needle := range tc.absent {
			if strings.Contains(string(data), needle) {
				t.Fatalf("%s still carries %q: %s", tc.path, needle, firstLineContaining(string(data), needle))
			}
		}
	}
}

func firstLineContaining(body, needle string) string {
	for i, line := range strings.Split(body, "\n") {
		if strings.Contains(line, needle) {
			return fmt.Sprintf("line %d: %s", i+1, line)
		}
	}
	return ""
}

// TestJobOrchestratorLeavesThePushToTheSupervisor: an orchestrator that listed
// the push as its own item and marked it partial failed a job that had done
// everything right (DJA-712).
func TestJobOrchestratorLeavesThePushToTheSupervisor(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("roles", "job-orchestrator.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"The supervisor owns the push",
		"your report carries no item about the push unless the brief asks for one",
		"not being able to see the remote never makes such an item partial",
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("job-orchestrator.md lost %q", want)
		}
	}
}
