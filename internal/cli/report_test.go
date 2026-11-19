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
