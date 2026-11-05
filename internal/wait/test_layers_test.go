package wait

import (
	"bytes"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runCwd drives the same fixture once with an explicit caller cwd, which the
// test-layers root must ignore (the root comes from the sidecar's git_tree).
func (m *metricsFixture) runCwd(t *testing.T, cwd string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	oldOut, oldErr := platform.Stdout, platform.Stderr
	platform.Stdout, platform.Stderr = &stdout, &stderr
	code := WaitFor([]string{"worker"}, m.sd, m.ctx, m.env, 5000, false, cwd)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	return stdout.String(), stderr.String(), code
}

const testLayersFailReport = "# Report\n\n## Test layers\n- unit: FAIL: the suite failed\n- integration: n/a\n\n## Items\n| item | state |\n| --- | --- |\n| slice a | [done] |\n"

const wantTestLayersFailJSON = `"test_layers":{"pass":0,"fail":1,"not_run":0,"blocked":0,"na":1,"unmarked":0}`

func TestWaitTestLayersFailWarnsOnce(t *testing.T) { // scenarios 5+7
	t.Run("wait: a FAIL layer lands in the JSON and warns with the layer name", func(t *testing.T) {
		f := newMetricsFixture(t, "off", metricsImplementerRoster, testLayersFailReport, metricsBriefBackend, "")
		out, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d out=%q err=%q", rc, out, errText)
		}
		if !strings.Contains(out, `"status":"done"`) {
			t.Fatalf("status not done: %q", out)
		}
		if !strings.Contains(out, wantTestLayersFailJSON) {
			t.Fatalf("test_layers missing or wrong in the done line: %q", out)
		}
		if !strings.Contains(errText, "report of 'worker' lists 1 test layer(s) that did not pass (unit FAIL): read them before commit, push or release") {
			t.Fatalf("missing the did-not-pass warning: %q", errText)
		}
	})
	t.Run("wait: a report without a section settles the same, with no key and no warning", func(t *testing.T) {
		f := newMetricsFixture(t, "off", metricsImplementerRoster, metricsImplementerReport, metricsBriefBackend, "")
		out, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if !strings.Contains(out, `"status":"done"`) {
			t.Fatalf("status not done: %q", out)
		}
		if strings.Contains(out, "test_layers") {
			t.Fatalf("test_layers key without a section: %q", out)
		}
		if strings.Contains(errText, "test layer") || strings.Contains(errText, "Test layers") {
			t.Fatalf("warning without a section: %q", errText)
		}
	})
	t.Run("wait: a second poll of the same report warns only once", func(t *testing.T) {
		f := newMetricsFixture(t, "off", metricsImplementerRoster, testLayersFailReport, metricsBriefBackend, "")
		_, err1, rc1 := f.run(t)
		out2, err2, rc2 := f.run(t)
		if rc1 != 0 || rc2 != 0 {
			t.Fatalf("rc1=%d rc2=%d", rc1, rc2)
		}
		if !strings.Contains(err1, "did not pass") {
			t.Fatalf("first poll did not warn: %q", err1)
		}
		if strings.Contains(err2, "did not pass") {
			t.Fatalf("second poll warned again: %q", err2)
		}
		if !strings.Contains(out2, wantTestLayersFailJSON) {
			t.Fatalf("second poll lost the test_layers key: %q", out2)
		}
	})
	t.Run("wait: an unwritable marker does not abort the wait, the warning goes out and repeats", func(t *testing.T) {
		f := newMetricsFixture(t, "off", metricsImplementerRoster, testLayersFailReport, metricsBriefBackend, "")
		// The marker path is a directory: the write cannot persist and the
		// marker read fails too, so the warning is eligible again each poll.
		if err := os.Mkdir(filepath.Join(f.sd, "wait", "worker.test-layers-warned"), 0o700); err != nil {
			t.Fatal(err)
		}
		out1, err1, rc1 := f.run(t)
		if rc1 != 0 {
			t.Fatalf("rc=%d out=%q err=%q", rc1, out1, err1)
		}
		if !strings.Contains(out1, `"status":"done"`) || !strings.Contains(out1, wantTestLayersFailJSON) {
			t.Fatalf("the done line changed: %q", out1)
		}
		if !strings.Contains(err1, "did not pass") {
			t.Fatalf("the warning did not go out: %q", err1)
		}
		_, err2, rc2 := f.run(t)
		if rc2 != 0 {
			t.Fatalf("rc=%d", rc2)
		}
		if !strings.Contains(err2, "did not pass") {
			t.Fatalf("the warning should repeat while the marker cannot persist: %q", err2)
		}
	})
}

func TestWaitTestLayersAllPassIsQuiet(t *testing.T) { // scenario 6
	t.Run("wait: only PASS and n/a publish the counts without a warning", func(t *testing.T) {
		body := "# Report\n\n## Test layers\n- unit: PASS\n- integration: n/a\n- contract: n/a: not a CLI project\n\n## Items\n| item | state |\n| --- | --- |\n| slice a | [done] |\n"
		f := newMetricsFixture(t, "off", metricsImplementerRoster, body, metricsBriefBackend, "")
		out, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if !strings.Contains(out, `"test_layers":{"pass":1,"fail":0,"not_run":0,"blocked":0,"na":2,"unmarked":0}`) {
			t.Fatalf("test_layers missing or wrong: %q", out)
		}
		if strings.Contains(errText, "test layer") || strings.Contains(errText, "Test layers") {
			t.Fatalf("all-pass report warned: %q", errText)
		}
	})
	t.Run("wait: unmarked section lines warn with the count", func(t *testing.T) {
		body := "# Report\n\n## Test layers\n- unit: **PASS**\n- mutation: FAIL\n\n## Items\n| item | state |\n| --- | --- |\n| slice a | [done] |\n"
		f := newMetricsFixture(t, "off", metricsImplementerRoster, body, metricsBriefBackend, "")
		out, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if !strings.Contains(out, `"test_layers":{"pass":0,"fail":0,"not_run":0,"blocked":0,"na":0,"unmarked":2}`) {
			t.Fatalf("test_layers missing or wrong: %q", out)
		}
		if !strings.Contains(errText, "report of 'worker' has 2 Test layers line(s) without a result") {
			t.Fatalf("missing the unmarked warning: %q", errText)
		}
		if strings.Contains(errText, "did not pass") {
			t.Fatalf("unmarked lines are not non-pass layers: %q", errText)
		}
	})
}

func TestWaitTestLayersMissingSection(t *testing.T) { // scenario 8
	plainReport := "# Report\n\n## Items\n| item | state |\n| --- | --- |\n| slice a | [done] |\n"

	gitTreeSidecar := func(root string) string {
		return jsonjs.Stringify(jsonjs.O("git_head", strings.Repeat("a", 40), "git_branch", "main", "git_tree", root))
	}

	writeValidation := func(t *testing.T, root string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "VALIDATION.md"), []byte("# Validation\n\n## Test layers\n- unit: `go test ./...`\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("wait: an implementer report without the section warns when the worker's git_tree has VALIDATION.md", func(t *testing.T) {
		root := t.TempDir()
		writeValidation(t, root)
		f := newMetricsFixture(t, "off", metricsImplementerRoster, plainReport, metricsBriefBackend, gitTreeSidecar(root))
		out, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if strings.Contains(out, "test_layers") {
			t.Fatalf("test_layers key without a section: %q", out)
		}
		if !strings.Contains(errText, "report of 'worker' has no Test layers section, and the project has a VALIDATION.md") {
			t.Fatalf("missing the missing-section warning: %q", errText)
		}
	})
	t.Run("wait: the caller's project A is not the root — a git_tree B without VALIDATION.md does not warn", func(t *testing.T) { // scenario 2
		projectA, rootB := t.TempDir(), t.TempDir()
		writeValidation(t, projectA)
		f := newMetricsFixture(t, "off", metricsImplementerRoster, plainReport, metricsBriefBackend, gitTreeSidecar(rootB))
		_, errText, rc := f.runCwd(t, projectA)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if strings.Contains(errText, "no Test layers section") {
			t.Fatalf("the caller's project was inspected instead of the worker's root: %q", errText)
		}
	})
	t.Run("wait: a git_tree B with VALIDATION.md warns even when the caller's project does not have it", func(t *testing.T) { // scenario 3
		projectA, rootB := t.TempDir(), t.TempDir()
		writeValidation(t, rootB)
		f := newMetricsFixture(t, "off", metricsImplementerRoster, plainReport, metricsBriefBackend, gitTreeSidecar(rootB))
		_, errText, rc := f.runCwd(t, projectA)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if !strings.Contains(errText, "has no Test layers section, and the project has a VALIDATION.md") {
			t.Fatalf("the worker's root was not inspected: %q", errText)
		}
	})
	t.Run("wait: a sidecar without git_tree does not warn, even with the file in the caller's cwd", func(t *testing.T) { // scenario 4
		root := t.TempDir()
		writeValidation(t, root)
		sidecar := `{"git_head":"` + strings.Repeat("a", 40) + `","git_branch":"main"}`
		f := newMetricsFixture(t, "off", metricsImplementerRoster, plainReport, metricsBriefBackend, sidecar)
		_, errText, rc := f.runCwd(t, root)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if strings.Contains(errText, "no Test layers section") {
			t.Fatalf("the caller's cwd was inspected without a recorded git_tree: %q", errText)
		}
	})
	t.Run("wait: no VALIDATION.md, no warning", func(t *testing.T) {
		root := t.TempDir()
		f := newMetricsFixture(t, "off", metricsImplementerRoster, plainReport, metricsBriefBackend, gitTreeSidecar(root))
		_, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if strings.Contains(errText, "Test layers") {
			t.Fatalf("warning without VALIDATION.md: %q", errText)
		}
	})
	t.Run("wait: a reviewer role does not get the missing-section warning", func(t *testing.T) {
		root := t.TempDir()
		writeValidation(t, root)
		f := newMetricsFixture(t, "off", metricsReviewerRoster, plainReport, metricsBriefBackend, gitTreeSidecar(root))
		_, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if strings.Contains(errText, "no Test layers section") {
			t.Fatalf("reviewer got the missing-section warning: %q", errText)
		}
	})
	t.Run("wait: a linked VALIDATION.md is absent", func(t *testing.T) {
		root, outside := t.TempDir(), t.TempDir()
		if err := os.WriteFile(filepath.Join(outside, "VALIDATION.md"), []byte("# Validation\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, "VALIDATION.md"), filepath.Join(root, "VALIDATION.md")); err != nil {
			t.Skipf("native symlink creation denied: %v", err)
		}
		f := newMetricsFixture(t, "off", metricsImplementerRoster, plainReport, metricsBriefBackend, gitTreeSidecar(root))
		_, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if strings.Contains(errText, "no Test layers section") {
			t.Fatalf("a link counted as VALIDATION.md: %q", errText)
		}
	})
	t.Run("wait: a directory named VALIDATION.md is absent", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "VALIDATION.md"), 0o700); err != nil {
			t.Fatal(err)
		}
		f := newMetricsFixture(t, "off", metricsImplementerRoster, plainReport, metricsBriefBackend, gitTreeSidecar(root))
		_, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if strings.Contains(errText, "no Test layers section") {
			t.Fatalf("a directory counted as VALIDATION.md: %q", errText)
		}
	})
	t.Run("wait: a deleted git_tree directory has no VALIDATION.md", func(t *testing.T) {
		root := t.TempDir()
		writeValidation(t, root)
		if err := os.RemoveAll(root); err != nil {
			t.Fatal(err)
		}
		f := newMetricsFixture(t, "off", metricsImplementerRoster, plainReport, metricsBriefBackend, gitTreeSidecar(root))
		_, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if strings.Contains(errText, "no Test layers section") {
			t.Fatalf("a deleted root still warned: %q", errText)
		}
	})
}

func TestWaitValidatorLine(t *testing.T) { // scenario 9
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\nworker\tp0a\tcodex\tvalidator\topenai\t\t\t20260930T210000\tgpt-5\task\tvalidator\treview\t0\t0\tmax\n"

	t.Run("wait: a validator FAIL line lands in the JSON and warns", func(t *testing.T) {
		body := "findings: 0 (p0 0, p1 0, p2 0, p3 0) | verdict: pass\nvalidation: FAIL\n\n## Findings\nnone\n"
		f := newMetricsFixture(t, "off", roster, body, metricsBriefBackend, "")
		out, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if !strings.Contains(out, `"validation":"FAIL"`) {
			t.Fatalf("validation key missing or wrong: %q", out)
		}
		if !strings.Contains(errText, "validation of 'worker' is FAIL: read it before commit, push or release") {
			t.Fatalf("missing the validation warning: %q", errText)
		}
		if strings.Contains(errText, "no Test layers section") {
			t.Fatalf("the validator role must not get the missing-section warning: %q", errText)
		}
	})
	t.Run("wait: a validator PASS line lands in the JSON without a warning", func(t *testing.T) {
		body := "findings: 0 (p0 0, p1 0, p2 0, p3 0) | verdict: pass\nvalidation: PASS\n\n## Findings\nnone\n"
		f := newMetricsFixture(t, "off", roster, body, metricsBriefBackend, "")
		out, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if !strings.Contains(out, `"validation":"PASS"`) {
			t.Fatalf("validation key missing or wrong: %q", out)
		}
		if strings.Contains(errText, "validation of") {
			t.Fatalf("a PASS validation warned: %q", errText)
		}
	})
	t.Run("wait: a validation line after the second line does not count", func(t *testing.T) {
		body := "findings: 0 (p0 0, p1 0, p2 0, p3 0) | verdict: pass\nsome other line\nvalidation: FAIL\n"
		f := newMetricsFixture(t, "off", roster, body, metricsBriefBackend, "")
		out, errText, rc := f.run(t)
		if rc != 0 {
			t.Fatalf("rc=%d err=%q", rc, errText)
		}
		if strings.Contains(out, `"validation"`) {
			t.Fatalf("a late validation line counted: %q", out)
		}
		if strings.Contains(errText, "validation of") {
			t.Fatalf("a late validation line warned: %q", errText)
		}
	})
}
