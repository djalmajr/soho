package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/reportscan"
)

type parsedReportJSON struct {
	Schema     int    `json:"schema"`
	Agent      string `json:"agent"`
	ReportPath string `json:"report_path"`
	Role       string `json:"role"`
	Kind       string `json:"kind"`
	Model      string `json:"model"`
	Counts     struct {
		Done, Partial, Env, Skipped int
	} `json:"counts"`
	Items []struct {
		Line int    `json:"line"`
		Mark string `json:"mark"`
		Text string `json:"text"`
	} `json:"items"`
	TestLayers struct {
		Found    bool `json:"found"`
		Pass     int  `json:"pass"`
		Fail     int  `json:"fail"`
		NotRun   int  `json:"not_run"`
		Blocked  int  `json:"blocked"`
		NA       int  `json:"na"`
		Unmarked int  `json:"unmarked"`
		Layers   []struct {
			Name, Result, Reason string
		} `json:"layers"`
	} `json:"test_layers"`
	Validation   map[string]string `json:"validation"`
	ReviewHeader struct {
		Findings float64            `json:"findings"`
		Severity map[string]float64 `json:"severity"`
		Verdict  string             `json:"verdict"`
	} `json:"review_header"`
	ReviewResponses struct {
		Found      bool  `json:"found"`
		Fixed      []int `json:"fixed"`
		Declined   []int `json:"declined"`
		Unmarked   int   `json:"unmarked"`
		Missing    []int `json:"missing"`
		Unknown    []int `json:"unknown"`
		Unnumbered int   `json:"unnumbered"`
	} `json:"review_responses"`
	FindingMarks *struct {
		Brief, Impl, Unknown, Unmarked int
		Classes                        map[string]int
		Numbers                        []int
	} `json:"finding_marks"`
	BlockedByScope []string `json:"blocked_by_scope"`
	Warnings       []struct {
		Kind          string `json:"kind"`
		Message       string `json:"message"`
		AlreadyWarned bool   `json:"already_warned"`
	} `json:"warnings"`
}

const reportFixtureText = "findings: 3 (P0 1, P1 0, P2 0, P3 2) | verdict: pass\n" +
	"validation: PASS\n" +
	"# report fixture\n" +
	"- [done] a\n" +
	"- [partial] b\n" +
	"- [skipped] c\n" +
	"```md\n" +
	"## Review responses\n" +
	"- F9: fixed\n" +
	"- [partial] fenced\n" +
	"```\n" +
	"## Test layers\n" +
	"- unit: PASS\n" +
	"- race: NOT RUN: host gate\n" +
	"- contract: FAIL: broken\n" +
	"## Review responses\n" +
	"- F1: fixed\n" +
	"- F2: declined: no\n" +
	"- bad response\n" +
	"blocked-by-scope: internal/foo.go:12 — smallest change\n"

const reportFixtureReview = "findings: 4 (P0 1, P1 0, P2 0, P3 3) | verdict: fail\n" +
	"- [F1] [origin: brief] [class: scope] first\n" +
	"- [F2] [origin: impl] [class: logic] second\n" +
	"- [F3] [origin: brief] [class: scope] third\n"

func reportFixture(t *testing.T) (platform.Env, string, string, string) {
	t.Helper()
	env, cwd := commandFixture(t)
	env["SOHO_NOWRITE"] = "1"
	sd := filepath.Join(env.Get("SOHO_DIR"), "ws")
	for _, dir := range []string{filepath.Join(sd, "wait"), filepath.Join(sd, "briefs")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	report := filepath.Join(cwd, "report-fixture.md")
	review := filepath.Join(cwd, "review-fixture.md")
	writeReportFixtureFile(t, report, reportFixtureText)
	writeReportFixtureFile(t, review, reportFixtureReview)
	writeReportFixtureFile(t, filepath.Join(sd, "briefs", "report-fixture.md"),
		"Review: `"+review+"`\nReference: ../reference.md\n")
	writeReportFixtureFile(t, core.LastReportPath(sd, "worker"), report+"\n")
	row := strings.Join([]string{"worker", "p1", "pi", "implementer", "by model", "", "/tmp/work", "2026", "model-x", "unsandboxed", "", "build"}, "\t")
	writeReportFixtureFile(t, filepath.Join(sd, "agents.tsv"), "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n"+row+"\n")
	writeReportFixtureFile(t, filepath.Join(sd, "wait", "worker.partial-warned"), report+"\n")
	writeReportFixtureFile(t, filepath.Join(sd, "wait", "worker.test-layers-warned"), report+"\n")
	writeReportFixtureFile(t, filepath.Join(sd, "wait", "worker.finding-marks-warned"), report+"\n")
	writeReportFixtureFile(t, filepath.Join(sd, "wait", "worker.review-responses-warned"), strconv.Quote(report)+"\nmissing\nunnumbered\n")
	return env, cwd, sd, report
}

func writeReportFixtureFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func parseReportOutput(t *testing.T, out string) parsedReportJSON {
	t.Helper()
	var parsed parsedReportJSON
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("parse report JSON: %v\n%s", err, out)
	}
	return parsed
}

func TestReportJSONFieldsAndSharedWarnings(t *testing.T) {
	env, cwd, _, report := reportFixture(t)
	code, out, stderr := runIn(t, []string{"report", "worker", "--json"}, env, cwd)
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	got := parseReportOutput(t, out)
	if got.Schema != 1 || got.Agent != "worker" || got.ReportPath != report {
		t.Fatalf("identity: %+v, want report %q", got, report)
	}
	if got.Role != "implementer" || got.Kind != "pi" || got.Model != "model-x" {
		t.Fatalf("roster identity: role=%q kind=%q model=%q", got.Role, got.Kind, got.Model)
	}
	if got.Counts.Done != 1 || got.Counts.Partial != 1 || got.Counts.Env != 0 || got.Counts.Skipped != 1 {
		t.Fatalf("counts: %+v", got.Counts)
	}
	partial, envCount := 0, 0
	for _, item := range got.Items {
		if item.Mark == "partial" {
			partial++
		}
		if item.Mark == "env" {
			envCount++
		}
		if item.Mark == "partial" && strings.Contains(item.Text, "fenced") {
			t.Fatal("a fenced partial became an item")
		}
	}
	if partial != reportscan.PartialCount(reportFixtureText) || envCount != reportscan.EnvCount(reportFixtureText) {
		t.Fatalf("item counts %d/%d, limits %d/%d", partial, envCount, reportscan.PartialCount(reportFixtureText), reportscan.EnvCount(reportFixtureText))
	}
	if !got.TestLayers.Found || got.TestLayers.Pass != 1 || got.TestLayers.Fail != 1 || got.TestLayers.NotRun != 1 || got.TestLayers.Unmarked != 0 {
		t.Fatalf("test layers: %+v", got.TestLayers)
	}
	layers := make([][3]string, 0, len(got.TestLayers.Layers))
	for _, layer := range got.TestLayers.Layers {
		layers = append(layers, [3]string{layer.Name, layer.Result, layer.Reason})
	}
	wantLayers := [][3]string{{"unit", "PASS", ""}, {"race", "NOT RUN", "host gate"}, {"contract", "FAIL", "broken"}}
	if !reflect.DeepEqual(layers, wantLayers) {
		t.Fatalf("layers: %v, want %v", layers, wantLayers)
	}
	if !reflect.DeepEqual(got.Validation, map[string]string{"result": "PASS"}) {
		t.Fatalf("validation: %v", got.Validation)
	}
	if got.ReviewHeader.Findings != 3 || got.ReviewHeader.Verdict != "pass" || got.ReviewHeader.Severity["P0"] != 1 {
		t.Fatalf("review header: %+v", got.ReviewHeader)
	}
	if !reflect.DeepEqual(got.ReviewResponses.Fixed, []int{1}) || !reflect.DeepEqual(got.ReviewResponses.Declined, []int{2}) ||
		got.ReviewResponses.Unmarked != 1 || !reflect.DeepEqual(got.ReviewResponses.Missing, []int{3}) ||
		len(got.ReviewResponses.Unknown) != 0 || got.ReviewResponses.Unnumbered != 1 {
		t.Fatalf("review responses: %+v", got.ReviewResponses)
	}
	if got.FindingMarks == nil || got.FindingMarks.Unmarked != 3 {
		t.Fatalf("finding marks: %+v", got.FindingMarks)
	}
	if len(got.BlockedByScope) != 1 || got.BlockedByScope[0] != "blocked by scope: internal/foo.go:12 — smallest change" {
		t.Fatalf("blocked_by_scope: %v", got.BlockedByScope)
	}
	wantKinds := []string{"partial", "verdict-open", "test-layers-non-pass", "finding-marks", "missing", "unmarked", "unnumbered", "reference-parity-missing"}
	kinds := make([]string, 0, len(got.Warnings))
	warned := map[string]bool{}
	for _, warning := range got.Warnings {
		kinds = append(kinds, warning.Kind)
		warned[warning.Kind] = warning.AlreadyWarned
	}
	if !reflect.DeepEqual(kinds, wantKinds) {
		t.Fatalf("warning kinds: %v, want %v", kinds, wantKinds)
	}
	for kind, want := range map[string]bool{
		"partial": true, "verdict-open": false, "test-layers-non-pass": true,
		"finding-marks": true, "missing": true, "unmarked": false,
		"unnumbered": true, "reference-parity-missing": false,
	} {
		if warned[kind] != want {
			t.Fatalf("%s already_warned=%t, want %t", kind, warned[kind], want)
		}
	}
	wantPartial := "report of 'worker' marks 1 item(s) partial: a partial item is not a pass; read them before commit, push or release"
	if got.Warnings[0].Message != wantPartial {
		t.Fatalf("partial warning: %q, want %q", got.Warnings[0].Message, wantPartial)
	}
}

func TestReportReadWritesNoMarkerOrFile(t *testing.T) {
	env, cwd, _, report := reportFixture(t)
	root := filepath.Dir(cwd)
	before := nowriteTreeSnapshot(t, root)
	code, out, stderr := runIn(t, []string{"report", "--file", report, "--json"}, env, cwd)
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if after := nowriteTreeSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatalf("report --file changed the tree: before %v, after %v", before, after)
	}
	got := parseReportOutput(t, out)
	if got.Agent != "" || got.Role != "" || got.Kind != "" || got.Model != "" {
		t.Fatalf("--file identity should be empty: %+v", got)
	}
	for _, warning := range got.Warnings {
		if warning.AlreadyWarned {
			t.Fatalf("--file warning %q claimed a marker", warning.Kind)
		}
	}
}

func TestReportTextForm(t *testing.T) {
	env, cwd, _, report := reportFixture(t)
	code, out, stderr := runIn(t, []string{"report", "--file", report}, env, cwd)
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) < 4 || lines[0] != "report: "+report || lines[1] != "done: 1 partial: 1 env: 0 skipped: 1" {
		t.Fatalf("text form: %q", out)
	}
	if !strings.HasPrefix(lines[2], "report of '' marks 1 item(s) partial") {
		t.Fatalf("first warning line: %q", lines[2])
	}
}

func TestReportNoReportAndUnknownFlag(t *testing.T) {
	env, cwd, _, _ := reportFixture(t)
	missing := filepath.Join(cwd, "not-a-report.md")
	code, out, stderr := runIn(t, []string{"report", "--file", missing, "--json"}, env, cwd)
	if code != 1 || out != "" || stderr != "soho: report: no report for "+missing+"\n" {
		t.Fatalf("missing file: code=%d out=%q stderr=%q", code, out, stderr)
	}
	writeReportFixtureFile(t, core.LastReportPath(filepath.Join(env.Get("SOHO_DIR"), "ws"), "nobody"), "")
	code, out, stderr = runIn(t, []string{"report", "nobody", "--json"}, env, cwd)
	if code != 1 || out != "" || stderr != "soho: report: no report for nobody\n" {
		t.Fatalf("no report: code=%d out=%q stderr=%q", code, out, stderr)
	}
	plain := env.Clone()
	delete(plain, "SOHO_NOWRITE")
	code, out, stderr = runIn(t, []string{"report", "worker", "--bogus"}, plain, cwd)
	if code != 2 || out != "" || !strings.Contains(stderr, reportUsage) {
		t.Fatalf("unknown flag: code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestReportUsesCurrentWorkspaceLastReport(t *testing.T) {
	env, cwd, _, report := reportFixture(t)
	otherState := filepath.Join(env.Get("SOHO_DIR"), "ws2")
	if err := os.MkdirAll(otherState, 0o700); err != nil {
		t.Fatal(err)
	}
	otherReport := filepath.Join(cwd, "other-report.md")
	writeReportFixtureFile(t, otherReport, "# other\n")
	writeReportFixtureFile(t, core.LastReportPath(otherState, "worker"), otherReport+"\n")
	otherEnv := env.Clone()
	otherEnv["HERDR_WORKSPACE_ID"] = "ws2"
	code, out, stderr := runIn(t, []string{"report", "worker", "--json"}, otherEnv, cwd)
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	got := parseReportOutput(t, out)
	if got.ReportPath != otherReport || got.ReportPath == report {
		t.Fatalf("report path %q, want %q", got.ReportPath, otherReport)
	}
}

func TestReportHelpAndNowriteInvocation(t *testing.T) {
	env, cwd, _, report := reportFixture(t)
	plain := env.Clone()
	delete(plain, "SOHO_NOWRITE")
	code, out, stderr := runIn(t, []string{"report", "--help"}, plain, cwd)
	if code != 0 || stderr != "" || !strings.Contains(out, "soho report <agent> | --file <path> [--json]") {
		t.Fatalf("help: code=%d out=%q stderr=%q", code, out, stderr)
	}
	code, out, stderr = runIn(t, []string{"report", "--file", report, "--json"}, env, cwd)
	if code != 0 || stderr != "" || out == "" {
		t.Fatalf("nowrite report: code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestCapabilitiesIncludesReportJSON(t *testing.T) {
	root := t.TempDir()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr strings.Builder
	platform.Stdout, platform.Stderr = &out, &stderr
	code := Run([]string{"capabilities", "--json"}, platform.Env{"HOME": root, "USERPROFILE": root, "SOHO_DIR": filepath.Join(root, "state"), "TMPDIR": root, "SOHO_NOWRITE": "1"})
	platform.Stdout, platform.Stderr = oldOut, oldErr
	if code != 0 || stderr.Len() != 0 || !strings.Contains(out.String(), `"report_json":1`) {
		t.Fatalf("capabilities: code=%d out=%q stderr=%q", code, out.String(), stderr.String())
	}
}

// reportTaskRoleFixture is the dispatch-side fixture: a roster reviewer whose
// composed brief declares a review with a numbered F1 and whose sidecar may
// record a different dispatched role, with a VALIDATION.md at the worker root.
func reportTaskRoleFixture(t *testing.T) (platform.Env, string, string, string) {
	t.Helper()
	env, cwd := commandFixture(t)
	sd := filepath.Join(env.Get("SOHO_DIR"), "ws")
	for _, dir := range []string{filepath.Join(sd, "wait"), filepath.Join(sd, "briefs")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	report := filepath.Join(cwd, "worker-report.md")
	review := filepath.Join(cwd, "review.md")
	writeReportFixtureFile(t, report, "# Worker report\n- [partial] host result pending\n")
	writeReportFixtureFile(t, review, "findings: 1 (P0 0, P1 0, P2 1, P3 0) | verdict: fail\n- [F1] [origin: brief] [class: logic] fix it\n")
	writeReportFixtureFile(t, filepath.Join(sd, "briefs", "worker-report.md"), "Review: "+review+"\n")
	writeReportFixtureFile(t, core.LastReportPath(sd, "worker"), report+"\n")
	header := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n"
	row := strings.Join([]string{"worker", "p1", "pi", "reviewer", "by model", "", cwd, "2026", "model-x", "unsandboxed", "", "build"}, "\t")
	writeReportFixtureFile(t, filepath.Join(sd, "agents.tsv"), header+row+"\n")
	writeReportFixtureFile(t, filepath.Join(cwd, "VALIDATION.md"), "")
	return env, cwd, sd, report
}

func reportWarningKinds(got parsedReportJSON) []string {
	kinds := make([]string, 0, len(got.Warnings))
	for _, warning := range got.Warnings {
		kinds = append(kinds, warning.Kind)
	}
	return kinds
}

// TestReportRoleUsesDispatchedTaskRole: a reused worker's report uses the
// dispatched task role (task pointer or dispatch sidecar first, roster only as
// the fallback) for the role field and every role-gated warning, exactly as
// wait resolves it; kind and model stay from the roster.
func TestReportRoleUsesDispatchedTaskRole(t *testing.T) {
	env, cwd, sd, _ := reportTaskRoleFixture(t)
	code, out, stderr := runIn(t, []string{"report", "worker", "--json"}, env, cwd)
	if code != 0 || stderr != "" {
		t.Fatalf("roster reviewer: code=%d stderr=%q", code, stderr)
	}
	got := parseReportOutput(t, out)
	if got.Role != "reviewer" {
		t.Fatalf("roster role: role=%q, want reviewer", got.Role)
	}
	if kinds := reportWarningKinds(got); !reflect.DeepEqual(kinds, []string{"partial", "own-no-header"}) {
		t.Fatalf("roster warning kinds: %v", kinds)
	}

	writeReportFixtureFile(t, filepath.Join(sd, "briefs", "worker-report.dispatch.json"),
		`{"role":"implementer","git_tree":`+strconv.Quote(cwd)+`}`+"\n")
	code, out, stderr = runIn(t, []string{"report", "worker", "--json"}, env, cwd)
	if code != 0 || stderr != "" {
		t.Fatalf("dispatched implementer: code=%d stderr=%q", code, stderr)
	}
	got = parseReportOutput(t, out)
	if got.Role != "implementer" {
		t.Fatalf("dispatched role: role=%q, want implementer", got.Role)
	}
	if got.Kind != "pi" || got.Model != "model-x" {
		t.Fatalf("roster kind/model: kind=%q model=%q", got.Kind, got.Model)
	}
	if kinds := reportWarningKinds(got); !reflect.DeepEqual(kinds, []string{"partial", "test-layers-missing", "missing"}) {
		t.Fatalf("implementer warning kinds: %v", kinds)
	}
	if !reflect.DeepEqual(got.ReviewResponses.Missing, []int{1}) {
		t.Fatalf("implementer review responses: %+v", got.ReviewResponses)
	}
}

// TestReportResolvesWithoutMaterializedSkill is the binary probe's fixture in
// process: isolated home, empty bundle cache, explicit state root, a readable
// last-report marker, no SOHO_SKILL_DIR and no installed skill.
func TestReportResolvesWithoutMaterializedSkill(t *testing.T) {
	env, cwd := commandFixture(t)
	delete(env, "SOHO_SKILL_DIR")
	sd := filepath.Join(env.Get("SOHO_DIR"), "ws")
	if err := os.MkdirAll(sd, 0o700); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(cwd, "worker-report.md")
	writeReportFixtureFile(t, report, "# Report\n- [partial] host result pending\n")
	writeReportFixtureFile(t, core.LastReportPath(sd, "worker"), report+"\n")
	code, out, stderr := runIn(t, []string{"report", "worker", "--json"}, env, cwd)
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	got := parseReportOutput(t, out)
	if got.Agent != "worker" || got.ReportPath != report {
		t.Fatalf("identity: agent=%q report=%q, want worker/%q", got.Agent, got.ReportPath, report)
	}
	if got.Counts.Partial != 1 {
		t.Fatalf("counts: %+v", got.Counts)
	}
}

// TestReportItemsAndCountsFromOneClassification: the projection's counts are
// the items' totals per mark (partial and env may coexist on a line), and no
// inline-code mention in prose becomes a done or skipped item.
func TestReportItemsAndCountsFromOneClassification(t *testing.T) {
	env, cwd, _, _ := reportFixture(t)
	file := filepath.Join(cwd, "mixed-items.md")
	writeReportFixtureFile(t, file, "# Report\r\n"+
		"- [partial] needs a rerun | [env] | Linux host only |\r\n"+
		"- mentions `[done]` and nothing else\r\n"+
		"- mentions `[skipped]` and nothing else\r\n"+
		"~~~md\r\n"+
		"- [env] fenced\r\n"+
		"~~~\r\n"+
		"- `[partial]` real inline-code marker\r\n")
	code, out, stderr := runIn(t, []string{"report", "--file", file, "--json"}, env, cwd)
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	got := parseReportOutput(t, out)
	want := []struct {
		line int
		mark string
	}{
		{2, "partial"}, {2, "env"}, {8, "partial"},
	}
	if len(got.Items) != len(want) {
		t.Fatalf("items: %+v", got.Items)
	}
	for i, entry := range want {
		if got.Items[i].Line != entry.line || got.Items[i].Mark != entry.mark {
			t.Fatalf("item %d: %+v, want line %d %s", i, got.Items[i], entry.line, entry.mark)
		}
	}
	if got.Counts.Done != 0 || got.Counts.Partial != 2 || got.Counts.Env != 1 || got.Counts.Skipped != 0 {
		t.Fatalf("counts: %+v", got.Counts)
	}
}

// TestReportRefusesMalformedArgumentsUnderNowrite: one parser serves the
// command dispatch and the NOWRITE/hc admission: a flag-shaped --file
// operand, an unknown flag and a repeated flag exit 2, and a dash-prefixed
// file is reachable as ./-name.
func TestReportRefusesMalformedArgumentsUnderNowrite(t *testing.T) {
	env, cwd, _, report := reportFixture(t)
	hcEnv := env.Clone()
	hcEnv["SOHO_TRANSPORT"] = "hc"
	for _, args := range [][]string{
		{"report", "--bogus"},
		{"report", "--file", "--bogus"},
		{"report", "--file", report, "--json", "--json"},
		{"report", "--json", "--json", "--file", report},
		{"report", "--file", ""},
	} {
		code, out, stderr := runIn(t, args, hcEnv, cwd)
		if code != 2 || out != "" || !strings.Contains(stderr, "SOHO_NOWRITE=1 is read-only") {
			t.Fatalf("%v under NOWRITE/hc: code=%d out=%q stderr=%q", args, code, out, stderr)
		}
	}
	// The command dispatch refuses the same shapes without NOWRITE.
	plain := env.Clone()
	delete(plain, "SOHO_NOWRITE")
	code, out, stderr := runIn(t, []string{"report", "--file", "--bogus"}, plain, cwd)
	if code != 2 || out != "" || !strings.Contains(stderr, reportUsage) {
		t.Fatalf("dispatch refusal: code=%d out=%q stderr=%q", code, out, stderr)
	}
	// Both documented shapes still run, including a dash-prefixed file
	// addressed through ./-name.
	dashed := filepath.Join(cwd, "--dash-report.md")
	writeReportFixtureFile(t, dashed, "# Report\n- [done] one\n")
	code, out, stderr = runIn(t, []string{"report", "--file", "./--dash-report.md", "--json"}, hcEnv, cwd)
	if code != 0 || stderr != "" || out == "" {
		t.Fatalf("dash file: code=%d out=%q stderr=%q", code, out, stderr)
	}
	code, out, stderr = runIn(t, []string{"report", "--file", report, "--json"}, hcEnv, cwd)
	if code != 0 || stderr != "" || out == "" {
		t.Fatalf("valid file form: code=%d out=%q stderr=%q", code, out, stderr)
	}
}

// TestReportDistinctMissingHeaderWarnings: the worker's own missing header
// and the declared review's get distinct public kinds, and already_warned
// follows only each warning's own marker (the review marker's legacy
// no-header key never silences the worker's own warning).
func TestReportDistinctMissingHeaderWarnings(t *testing.T) {
	env, cwd, sd, report := reportTaskRoleFixture(t)
	if err := os.MkdirAll(filepath.Join(cwd, ".agents", "soho-roles"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeReportFixtureFile(t, filepath.Join(cwd, ".agents", "soho-roles", "reviewer.md"), "---\nmode: edit\n---\n")
	writeReportFixtureFile(t, filepath.Join(cwd, "review.md"), "# Declared review without a header\n")
	code, out, stderr := runIn(t, []string{"report", "worker", "--json"}, env, cwd)
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	got := parseReportOutput(t, out)
	if kinds := reportWarningKinds(got); !reflect.DeepEqual(kinds, []string{"partial", "own-no-header", "review-no-header"}) {
		t.Fatalf("kinds: %v", kinds)
	}
	for _, warning := range got.Warnings {
		if warning.AlreadyWarned {
			t.Fatalf("%s claimed a marker before one existed", warning.Kind)
		}
	}
	writeReportFixtureFile(t, filepath.Join(sd, "wait", "worker.review-responses-warned"),
		strconv.Quote(report)+"\nno-header\n")
	code, out, stderr = runIn(t, []string{"report", "worker", "--json"}, env, cwd)
	if code != 0 || stderr != "" {
		t.Fatalf("after marker: code=%d stderr=%q", code, stderr)
	}
	got = parseReportOutput(t, out)
	warned := map[string]bool{}
	for _, warning := range got.Warnings {
		warned[warning.Kind] = warning.AlreadyWarned
	}
	if warned["own-no-header"] {
		t.Fatal("the worker's own missing header followed the review marker")
	}
	if !warned["review-no-header"] {
		t.Fatal("the review's missing header ignored its own marker")
	}
}

// reportConfigFixture writes what the report config tests choose between: a
// project layer selecting the base state dir and a worker report in both the
// base and the redirect target, so a wrong resolution is visible in the JSON
// report_path instead of an empty answer. SOHO_DIR and SOHO_STATE_DIR are
// removed so the config layers, not the environment, select the state root.
func reportConfigFixture(t *testing.T) (platform.Env, string, string, string) {
	t.Helper()
	env, cwd := commandFixture(t)
	env["SOHO_NOWRITE"] = "1"
	delete(env, "SOHO_DIR")
	delete(env, "SOHO_STATE_DIR")
	if err := os.MkdirAll(filepath.Join(cwd, ".agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeReportFixtureFile(t, filepath.Join(cwd, ".agents", "soho.conf"), "state_dir = base\n")
	baseReport := filepath.Join(cwd, "base-report.md")
	redirectedReport := filepath.Join(cwd, "redirected-report.md")
	writeReportFixtureFile(t, baseReport, "# Base report\n- [done] base\n")
	writeReportFixtureFile(t, redirectedReport, "# Redirected report\n- [done] redirected\n")
	for state, report := range map[string]string{"base": baseReport, "redirected": redirectedReport} {
		dir := filepath.Join(cwd, state, "ws")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		writeReportFixtureFile(t, core.LastReportPath(dir, "worker"), report+"\n")
	}
	return env, cwd, baseReport, redirectedReport
}

// runReportPath runs the agent form and returns its report_path.
func runReportPath(t *testing.T, env platform.Env, cwd string) string {
	t.Helper()
	code, out, stderr := runIn(t, []string{"report", "worker", "--json"}, env, cwd)
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	return parseReportOutput(t, out).ReportPath
}

// TestReportConfigFollowsSymlinkedSessionFile pins the state-root redirect a
// workspace session file supplies when that file is a symlink to a regular
// file: the config-file semantics LoadConfigFor reads for wait, so report
// resolves the named worker's report in the redirected state dir, not in the
// project layer's base dir. The regular-file run is the control.
func TestReportConfigFollowsSymlinkedSessionFile(t *testing.T) {
	env, cwd, baseReport, redirectedReport := reportConfigFixture(t)
	dir := filepath.Join(cwd, "base", "ws")
	session := filepath.Join(dir, "session.conf")
	body := "state_dir = redirected\n"
	writeReportFixtureFile(t, filepath.Join(dir, "redirected-session.conf"), body)
	writeReportFixtureFile(t, session, body)
	if got := runReportPath(t, env, cwd); got != redirectedReport {
		t.Fatalf("regular session file: report_path=%q, want %q (base %q)", got, redirectedReport, baseReport)
	}
	if err := os.Remove(session); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("redirected-session.conf", session); err != nil {
		t.Fatal(err)
	}
	if got := runReportPath(t, env, cwd); got != redirectedReport {
		t.Fatalf("symlinked session file: report_path=%q, want %q (base %q must not win)", got, redirectedReport, baseReport)
	}
}

// TestReportConfigFollowsSymlinkedProjectFile: the project layer's own
// symlink is read, so its state_dir still places the session file and the
// worker report that session redirects to.
func TestReportConfigFollowsSymlinkedProjectFile(t *testing.T) {
	env, cwd, baseReport, redirectedReport := reportConfigFixture(t)
	writeReportFixtureFile(t, filepath.Join(cwd, "base", "ws", "session.conf"), "state_dir = redirected\n")
	project := filepath.Join(cwd, ".agents", "soho.conf")
	if err := os.Rename(project, filepath.Join(cwd, ".agents", "regular-project.conf")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("regular-project.conf", project); err != nil {
		t.Fatal(err)
	}
	if got := runReportPath(t, env, cwd); got != redirectedReport {
		t.Fatalf("symlinked project file: report_path=%q, want %q (base %q must not win)", got, redirectedReport, baseReport)
	}
}

// TestReportConfigFollowsSymlinkedUserFile: the user layer's own symlink is
// read, so a state root it selects places the worker report.
func TestReportConfigFollowsSymlinkedUserFile(t *testing.T) {
	env, cwd := commandFixture(t)
	env["SOHO_NOWRITE"] = "1"
	delete(env, "SOHO_DIR")
	delete(env, "SOHO_STATE_DIR")
	userDir := filepath.Join(env.Get("XDG_CONFIG_HOME"), "soho")
	if err := os.MkdirAll(userDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeReportFixtureFile(t, filepath.Join(userDir, "regular-user.conf"), "state_dir = redirected\n")
	if err := os.Symlink("regular-user.conf", filepath.Join(userDir, "config")); err != nil {
		t.Fatal(err)
	}
	redirectedReport := filepath.Join(cwd, "redirected-report.md")
	writeReportFixtureFile(t, redirectedReport, "# Redirected report\n- [done] redirected\n")
	if err := os.MkdirAll(filepath.Join(cwd, "redirected", "ws"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeReportFixtureFile(t, filepath.Join(cwd, "redirected", "ws", "last-report-worker"), redirectedReport+"\n")
	if got := runReportPath(t, env, cwd); got != redirectedReport {
		t.Fatalf("symlinked user file: report_path=%q, want %q", got, redirectedReport)
	}
}

// TestReportConfigIgnoresAnotherWorkspaceSession: the session layer is the
// caller workspace's own file, so another workspace's session.conf never
// redirects the state root; the same content in the caller's workspace does
// (the control).
func TestReportConfigIgnoresAnotherWorkspaceSession(t *testing.T) {
	env, cwd, baseReport, redirectedReport := reportConfigFixture(t)
	other := filepath.Join(cwd, "base", "ws2")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	writeReportFixtureFile(t, filepath.Join(other, "session.conf"), "state_dir = redirected\n")
	if got := runReportPath(t, env, cwd); got != baseReport {
		t.Fatalf("another workspace's session file redirected the read: report_path=%q, want %q", got, baseReport)
	}
	writeReportFixtureFile(t, filepath.Join(cwd, "base", "ws", "session.conf"), "state_dir = redirected\n")
	if got := runReportPath(t, env, cwd); got != redirectedReport {
		t.Fatalf("the caller workspace's session file: report_path=%q, want %q", got, redirectedReport)
	}
}

// TestReportConfigUnreadableLayerExitsOne: a layer that exists and cannot be
// read fails the command with exit 1 and no JSON, instead of resolving the
// base state dir a silent skip would select (its report is present).
func TestReportConfigUnreadableLayerExitsOne(t *testing.T) {
	for _, tc := range []struct {
		name       string
		breakLayer func(t *testing.T, env platform.Env, cwd string)
	}{
		{"session symlink to a missing target", func(t *testing.T, env platform.Env, cwd string) {
			if err := os.Symlink("missing-session.conf", filepath.Join(cwd, "base", "ws", "session.conf")); err != nil {
				t.Fatal(err)
			}
		}},
		{"session symlink to a directory", func(t *testing.T, env platform.Env, cwd string) {
			if err := os.MkdirAll(filepath.Join(cwd, "not-a-file"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(cwd, "not-a-file"), filepath.Join(cwd, "base", "ws", "session.conf")); err != nil {
				t.Fatal(err)
			}
		}},
		{"project symlink to a missing target", func(t *testing.T, env platform.Env, cwd string) {
			project := filepath.Join(cwd, ".agents", "soho.conf")
			if err := os.Remove(project); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("missing-project.conf", project); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, cwd, _, _ := reportConfigFixture(t)
			tc.breakLayer(t, env, cwd)
			code, out, stderr := runIn(t, []string{"report", "worker", "--json"}, env, cwd)
			if code != 1 || out != "" {
				t.Fatalf("code=%d out=%q stderr=%q, want exit 1 and no output", code, out, stderr)
			}
			if !strings.Contains(stderr, "configuration layer") {
				t.Fatalf("stderr=%q does not name the unreadable layer", stderr)
			}
		})
	}
}
