package metrics

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// exportFixture holds a project directory (a plain dir, not a git repo, so
// the project root resolves to the cwd itself) and an env whose
// SOHO_DIR points at a state root with the given
// <workspace>/metrics.jsonl fixtures. HOME points at an empty dir and PATH
// is absent, so no skill is found and no git is run.
type exportFixture struct {
	proj  string
	state string
	env   platform.Env
	cfg   *core.Config
}

func newExportFixture(t *testing.T, stateFiles map[string]string) *exportFixture {
	t.Helper()
	proj := t.TempDir()
	state := filepath.Join(t.TempDir(), "state")
	for name, body := range stateFiles {
		path := filepath.Join(state, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := platform.Env{
		"SOHO_DIR": state,
		"HOME":     filepath.Join(t.TempDir(), "home"),
	}
	return &exportFixture{
		proj:  proj,
		state: state,
		env:   env,
		cfg:   &core.Config{Entries: map[string]core.ConfigEntry{}},
	}
}

// run calls CmdExport with the fixture context and captures stdout/stderr.
// The skeleton errors die with the platform.ExitError panic; cli.Run is the
// recovery boundary in production, and the test plays that role here.
func (f *exportFixture) run(t *testing.T, args ...string) (string, string, int, string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	oldOut, oldErr := platform.Stdout, platform.Stderr
	platform.Stdout, platform.Stderr = &out, &errBuf
	code := 0
	message := ""
	func() {
		defer func() {
			value := recover()
			if value == nil {
				return
			}
			exitErr, ok := value.(*platform.ExitError)
			if !ok {
				t.Fatalf("unexpected panic: %#v", value)
			}
			code = exitErr.Code
			message = exitErr.Msg
		}()
		code = CmdExport(args, CommandContext{Config: f.cfg, Env: f.env, Cwd: f.proj})
	}()
	platform.Stdout, platform.Stderr = oldOut, oldErr
	return out.String(), errBuf.String(), code, message
}

// runMetrics is run, routed through the skeleton CmdMetrics.
func (f *exportFixture) runMetrics(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	oldOut, oldErr := platform.Stdout, platform.Stderr
	platform.Stdout, platform.Stderr = &out, &errBuf
	code := 0
	message := ""
	func() {
		defer func() {
			value := recover()
			if value == nil {
				return
			}
			exitErr, ok := value.(*platform.ExitError)
			if !ok {
				t.Fatalf("unexpected panic: %#v", value)
			}
			code = exitErr.Code
			message = exitErr.Msg
		}()
		code = CmdMetrics(args, CommandContext{Config: f.cfg, Env: f.env, Cwd: f.proj})
	}()
	platform.Stdout, platform.Stderr = oldOut, oldErr
	_ = out
	_ = errBuf
	return code, message
}

// wantProjectLabel recomputes the default label from the spec: "p-" plus the
// first 8 hex chars of the sha256 of the base name of the dir that contains
// the state root.
func wantProjectLabel(state string) string {
	base := filepath.Base(filepath.Dir(state))
	sum := sha256.Sum256([]byte(base))
	return "p-" + hex.EncodeToString(sum[:])[:8]
}

// treeSnapshot lists every path under root, sorted, for the nothing-written
// proofs.
func treeSnapshot(t *testing.T, root string) []string {
	t.Helper()
	paths := []string{}
	err := filepath.Walk(root, func(path string, _ os.FileInfo, err error) error {
		if err == nil {
			paths = append(paths, filepath.ToSlash(path))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func sameTree(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// exportClosedSettled* are settled lines like wait writes them (plus the
// report name and for array of the other slice), with the stray "secret" key
// and a path inside `for` that must never reach the output.
const exportClosedSettledImplementer = `{"ts":"2026-09-30T21:00:00Z","agent":"worker","report":"worker.md","role":"implementer","kind":"claude","model":"claude-opus","effort":"high","family":"anthropic","type":"backend","duration_s":120,"amendment":true,"session":"20260930T210000","arrival":"queued","items":{"done":3,"partial":1,"skipped":1,"path":"/x"},"secret":"x"}`

const exportClosedSettledReviewer = `{"ts":"2026-09-30T22:00:00Z","agent":"reviewer","report":"reviewer.md","role":"reviewer","kind":"codex","model":"gpt-5","effort":"max","family":"openai","type":"review","duration_s":80,"items":{"done":2,"partial":0,"skipped":0},"verdict":"pass","findings":3,"severity":{"P0":0,"P1":1,"P2":2,"P3":0,"url":"https://x"},"verdict_effective":"fail","for":[{"kind":"claude","model":"claude-opus","effort":"high","family":"anthropic","cwd":"/Users/secret/proj"},{"kind":"codex"}],"secret":"x"}`

const exportClosedSettledOther = `{"ts":"2026-09-30T20:00:00Z","agent":"other","report":"other.md","kind":"pi","model":"gpt","secret":"x"}`

func TestExportClosedKeyList(t *testing.T) {
	f := newExportFixture(t, map[string]string{
		"ws1/metrics.jsonl": exportClosedSettledImplementer + "\n" + exportClosedSettledReviewer + "\n",
		// A second workspace proves the export reads every workspace.
		"ws2/metrics.jsonl": exportClosedSettledOther + "\n",
	})
	stdout, stderr, code, _ := f.run(t)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 output lines, got %d: %s", len(lines), stdout)
	}

	parse := func(line int) map[string]json.RawMessage {
		t.Helper()
		var got map[string]json.RawMessage
		if err := json.Unmarshal([]byte(lines[line]), &got); err != nil {
			t.Fatalf("output line %d is not a JSON object: %v: %s", line, err, lines[line])
		}
		return got
	}
	want := func(name string, got map[string]json.RawMessage) string {
		t.Helper()
		raw, ok := got[name]
		if !ok {
			t.Fatalf("output line misses the closed key %s: %v", name, got)
		}
		return string(raw)
	}
	forbidden := []string{"agent", "report", "ts", "secret", "cwd", "path", "url", "workspace"}

	// Output order is by ts: ws2 20:00, then ws1 21:00, then ws1 22:00, even
	// though the ws1 file sorts first.
	first := parse(0)
	if !strings.Contains(want("kind", first), "pi") {
		t.Fatalf("first output line is not the ws2 20:00 line: %s", lines[0])
	}

	implementer := parse(1)
	for _, key := range forbidden {
		if _, ok := implementer[key]; ok {
			t.Fatalf("key %q leaked into the summary line: %s", key, lines[1])
		}
	}
	if got := want("project", implementer); got != `"`+wantProjectLabel(f.state)+`"` {
		t.Fatalf("project=%s want %s", got, wantProjectLabel(f.state))
	}
	if got := want("date", implementer); got != `"2026-09-30"` {
		t.Fatalf("date=%s want \"2026-09-30\"", got)
	}
	if got := want("duration_s", implementer); got != "120" {
		t.Fatalf("duration_s=%s want 120", got)
	}
	if got := want("amendment", implementer); got != "true" {
		t.Fatalf("amendment=%s want true", got)
	}
	var itemsRaw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(want("items", implementer)), &itemsRaw); err != nil || len(itemsRaw) != 3 {
		t.Fatalf("items=%v want exactly the closed keys done/partial/skipped", itemsRaw)
	}
	for _, key := range []string{"done", "partial", "skipped"} {
		if _, ok := itemsRaw[key]; !ok {
			t.Fatalf("items misses the closed key %s: %v", key, itemsRaw)
		}
	}
	var items map[string]int
	if err := json.Unmarshal([]byte(want("items", implementer)), &items); err != nil || items["done"] != 3 || items["partial"] != 1 || items["skipped"] != 1 {
		t.Fatalf("items=%v want {done:3 partial:1 skipped:1}", items)
	}
	// The closed list: exactly these keys, no more.
	var wantKeys = map[string]bool{
		"project": true, "date": true, "role": true, "kind": true,
		"model": true, "effort": true, "family": true, "type": true,
		"duration_s": true, "amendment": true, "session": true,
		"arrival": true, "items": true,
	}
	if len(implementer) != len(wantKeys) {
		t.Fatalf("implementer line has %d keys, want %d: %s", len(implementer), len(wantKeys), lines[1])
	}
	for key := range implementer {
		if !wantKeys[key] {
			t.Fatalf("unexpected key %q in the closed list: %s", key, lines[1])
		}
	}

	reviewer := parse(2)
	for _, key := range forbidden {
		if _, ok := reviewer[key]; ok {
			t.Fatalf("key %q leaked into the reviewer line: %s", key, lines[2])
		}
	}
	if got := want("verdict", reviewer); got != `"pass"` {
		t.Fatalf("verdict=%s want pass", got)
	}
	if got := want("findings", reviewer); got != "3" {
		t.Fatalf("findings=%s want 3", got)
	}
	var severityRaw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(want("severity", reviewer)), &severityRaw); err != nil || len(severityRaw) != 4 {
		t.Fatalf("severity=%v want exactly the closed keys P0..P3", severityRaw)
	}
	for _, key := range []string{"P0", "P1", "P2", "P3"} {
		if _, ok := severityRaw[key]; !ok {
			t.Fatalf("severity misses the closed key %s: %v", key, severityRaw)
		}
	}
	var severity map[string]int
	if err := json.Unmarshal([]byte(want("severity", reviewer)), &severity); err != nil || severity["P2"] != 2 {
		t.Fatalf("severity=%v want P2=2", severity)
	}
	if got := want("verdict_effective", reviewer); got != `"fail"` {
		t.Fatalf("verdict_effective=%s want fail", got)
	}
	// `for` keeps only kind/model/effort/family from each author object, and
	// the path the first author carried is dropped.
	var authors []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(want("for", reviewer)), &authors); err != nil || len(authors) != 2 {
		t.Fatalf("for=%v want 2 author objects", authors)
	}
	if len(authors[0]) != 4 || len(authors[1]) != 1 {
		t.Fatalf("for author keys = %v %v; want only kind/model/effort/family", authors[0], authors[1])
	}
	for _, key := range []string{"kind", "model", "effort", "family"} {
		if _, ok := authors[0][key]; !ok {
			t.Fatalf("for[0] misses %s: %v", key, authors[0])
		}
	}
	if string(authors[1]["kind"]) != `"codex"` {
		t.Fatalf("for[1]=%v want kind codex", authors[1])
	}
}

func TestExportMarksApplied(t *testing.T) {
	f := newExportFixture(t, map[string]string{
		"ws1/metrics.jsonl": strings.Join([]string{
			`{"ts":"2026-09-30T21:00:00Z","agent":"reviewer","report":"rev.md","role":"reviewer","kind":"codex","model":"gpt-5","family":"openai","items":{"done":1,"partial":0,"skipped":0},"verdict":"fail","findings":3,"severity":{"P0":0,"P1":0,"P2":2,"P3":0},"verdict_effective":"fail"}`,
			`{"ts":"2026-09-30T23:00:00Z","label":"mark","report":"rev.md","findings":{"1":"real","2":"real"},"missed":{"P2":1},"amendment":"implementer"}`,
			`{"ts":"2026-09-30T23:30:00Z","label":"mark","report":"rev.md","findings":{"1":"false"},"missed":{"P2":1,"P0":1},"amendment":"brief"}`,
		}, "\n") + "\n",
		"ws2/metrics.jsonl": `{"ts":"2026-09-30T21:30:00Z","agent":"planner","report":"plan.md","role":"planner","kind":"pi","model":"gpt","items":{"done":5,"partial":0,"skipped":0}}` + "\n",
	})
	stdout, stderr, code, _ := f.run(t)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 output lines (marks produce no line of their own), got %d: %s", len(lines), stdout)
	}
	var rev, plan map[string]json.RawMessage
	if err := json.Unmarshal([]byte(lines[0]), &rev); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &plan); err != nil {
		t.Fatal(err)
	}
	// For the same finding the last mark wins: finding 1 was real then false,
	// so the final counts are real=1 false=1, not real=2 false=1.
	if string(rev["findings_real"]) != "1" || string(rev["findings_false"]) != "1" {
		t.Fatalf("final counts = real:%v false:%v want 1/1: %s", rev["findings_real"], rev["findings_false"], lines[0])
	}
	// findings_unlabeled = findings (3) - real (1) - false (1).
	if string(rev["findings_unlabeled"]) != "1" {
		t.Fatalf("findings_unlabeled=%v want 1: %s", rev["findings_unlabeled"], lines[0])
	}
	// missed is summed across the marks: P2 1+1, P0 1.
	var missed map[string]int
	if err := json.Unmarshal(rev["missed"], &missed); err != nil || missed["P2"] != 2 || missed["P0"] != 1 {
		t.Fatalf("missed=%v want {P0:1 P2:2}", missed)
	}
	// The last amendment is the cause.
	if string(rev["amendment_cause"]) != `"brief"` {
		t.Fatalf("amendment_cause=%v want brief: %s", rev["amendment_cause"], lines[0])
	}
	// A report without marks keeps none of the mark-derived keys.
	for _, key := range []string{"findings_real", "findings_false", "findings_unlabeled", "missed", "amendment_cause"} {
		if _, ok := plan[key]; ok {
			t.Fatalf("unmarked report carries %s: %s", key, lines[1])
		}
	}
}

func TestExportAmendmentCauseLast(t *testing.T) {
	f := newExportFixture(t, map[string]string{
		"ws1/metrics.jsonl": strings.Join([]string{
			`{"ts":"2026-09-30T21:00:00Z","agent":"w1","report":"a.md","role":"implementer","kind":"claude","model":"claude-opus","items":{"done":1,"partial":0,"skipped":0}}`,
			`{"ts":"2026-09-30T21:30:00Z","agent":"w2","report":"b.md","role":"implementer","kind":"claude","model":"claude-opus","items":{"done":1,"partial":0,"skipped":0}}`,
			`{"ts":"2026-09-30T23:00:00Z","label":"mark","report":"a.md","amendment":"implementer"}`,
			`{"ts":"2026-09-30T23:10:00Z","label":"mark","report":"a.md","findings":{"1":"real"}}`,
			`{"ts":"2026-09-30T23:20:00Z","label":"mark","report":"a.md","amendment":"brief"}`,
			`{"ts":"2026-09-30T23:30:00Z","label":"mark","report":"b.md","findings":{"1":"real"}}`,
		}, "\n") + "\n",
	})
	stdout, stderr, code, _ := f.run(t)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 output lines, got %d: %s", len(lines), stdout)
	}
	var a, b map[string]json.RawMessage
	if err := json.Unmarshal([]byte(lines[0]), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &b); err != nil {
		t.Fatal(err)
	}
	// The last amendment among the marks wins, even with a mark in between
	// that carries no amendment.
	if string(a["amendment_cause"]) != `"brief"` {
		t.Fatalf("amendment_cause=%v want brief: %s", a["amendment_cause"], lines[0])
	}
	// A mark without an amendment leaves the report without amendment_cause.
	if _, ok := b["amendment_cause"]; ok {
		t.Fatalf("amendment_cause present without an amendment mark: %s", lines[1])
	}
	if string(b["findings_real"]) != "1" {
		t.Fatalf("findings_real=%v want 1: %s", b["findings_real"], lines[1])
	}
}

func TestExportSince(t *testing.T) {
	f := newExportFixture(t, map[string]string{
		"ws1/metrics.jsonl": strings.Join([]string{
			`{"ts":"2026-09-29T10:00:00Z","agent":"w1","report":"a.md","kind":"claude","model":"claude-opus","items":{"done":1,"partial":0,"skipped":0}}`,
			`{"ts":"2026-09-30T10:00:00Z","agent":"w2","report":"b.md","kind":"codex","model":"gpt-5","items":{"done":1,"partial":0,"skipped":0}}`,
			`{"agent":"w3","report":"c.md","kind":"pi","model":"gpt","items":{"done":1,"partial":0,"skipped":0}}`,
		}, "\n") + "\n",
	})
	// A valid ISO date keeps only the line dated on or after it; the line
	// without a parseable ts is filtered out.
	stdout, stderr, code, _ := f.run(t, "--since", "2026-09-30T00:00:00Z")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], `"kind":"codex"`) {
		t.Fatalf("--since 2026-09-30T00:00:00Z kept %d lines: %s", len(lines), stdout)
	}
	// The AAAA-MM-DD format is accepted (far past: it keeps every dated line).
	stdout, stderr, code, _ = f.run(t, "--since", "2026-01-01")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n"); len(lines) != 2 {
		t.Fatalf("--since 2026-01-01 kept %d lines: %s", len(lines), stdout)
	}
	// An invalid date exits 2 with the stats --since message.
	_, stderr, code, message := f.run(t, "--since", "not-a-date")
	if code != 2 {
		t.Fatalf("exit=%d want 2 (stderr=%q)", code, stderr)
	}
	if message != "metrics export: --since expects AAAA-MM-DD or an ISO date (got 'not-a-date')" {
		t.Fatalf("message=%q", message)
	}
	// A missing value exits 2.
	_, stderr, code, message = f.run(t, "--since")
	if code != 2 {
		t.Fatalf("exit=%d want 2 (stderr=%q)", code, stderr)
	}
	if message != "metrics export: --since expects a date" {
		t.Fatalf("message=%q", message)
	}
}

func TestExportProjectLabel(t *testing.T) {
	settled := `{"ts":"2026-09-30T21:00:00Z","agent":"w","report":"a.md","kind":"claude","model":"claude-opus","items":{"done":1,"partial":0,"skipped":0}}`
	f := newExportFixture(t, map[string]string{"ws1/metrics.jsonl": settled + "\n"})
	stdout, stderr, code, _ := f.run(t)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSuffix(stdout, "\n")), &got); err != nil {
		t.Fatal(err)
	}
	label := strings.Trim(string(got["project"]), `"`)
	if want := wantProjectLabel(f.state); label != want {
		t.Fatalf("default project label=%q want %q", label, want)
	}
	if !regexp.MustCompile(`^p-[0-9a-f]{8}$`).MatchString(label) {
		t.Fatalf("default label %q does not match p-<8 hex>", label)
	}
	if strings.Contains(label, "state") {
		t.Fatalf("default label %q contains the directory name", label)
	}
	// The same containing-directory name under different trees gives the
	// same label (the herdr-soho case), whatever the platform.
	base := t.TempDir()
	mk := func(sub string) *exportFixture {
		t.Helper()
		proj := filepath.Join(base, sub, "proj")
		state := filepath.Join(base, sub, "herdr-soho", "state")
		path := filepath.Join(state, "ws1", "metrics.jsonl")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(proj, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(settled+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return &exportFixture{
			proj:  proj,
			state: state,
			env:   platform.Env{"SOHO_DIR": state, "HOME": filepath.Join(t.TempDir(), "home")},
			cfg:   &core.Config{Entries: map[string]core.ConfigEntry{}},
		}
	}
	stdoutA, stderrA, code, _ := mk("a").run(t)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderrA)
	}
	var gotA, gotB map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSuffix(stdoutA, "\n")), &gotA); err != nil {
		t.Fatal(err)
	}
	stdoutB, stderrB, code, _ := mk("b").run(t)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderrB)
	}
	if err := json.Unmarshal([]byte(strings.TrimSuffix(stdoutB, "\n")), &gotB); err != nil {
		t.Fatal(err)
	}
	labelA := string(gotA["project"])
	if labelA != string(gotB["project"]) {
		t.Fatalf("same base name gave different labels: %s vs %s", labelA, string(gotB["project"]))
	}
	if want := wantProjectLabel(filepath.Join(base, "a", "herdr-soho", "state")); labelA != `"`+want+`"` {
		t.Fatalf("soho label=%s want %s", labelA, want)
	}
	// Invalid labels exit 2; the 31-char body (32 total) is the last valid.
	for _, bad := range []string{"Bad", "bad_label", "-lead", "a.b", "", strings.Repeat("a", 33)} {
		_, stderr, code, message := f.run(t, "--project-label", bad)
		if code != 2 {
			t.Fatalf("--project-label %q exit=%d want 2 (stderr=%q)", bad, code, stderr)
		}
		if !strings.Contains(message, "--project-label") {
			t.Fatalf("message %q does not name the flag", message)
		}
	}
	ok32 := strings.Repeat("a", 32)
	stdout, stderr, code, _ = f.run(t, "--project-label", ok32)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	var got32 map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSuffix(stdout, "\n")), &got32); err != nil {
		t.Fatal(err)
	}
	if string(got32["project"]) != `"`+ok32+`"` {
		t.Fatalf("project label=%v want %s", got32["project"], ok32)
	}
}

func TestExportMalformedLines(t *testing.T) {
	f := newExportFixture(t, map[string]string{
		"ws1/metrics.jsonl": strings.Join([]string{
			`not json at all {`,
			`[1,2]`,
			`{"label":"weird"}`,
			`{"ts":"2026-09-30T21:00:00Z","agent":"w","report":"a.md","kind":"claude","model":"claude-opus","items":{"done":1,"partial":0,"skipped":0}}`,
		}, "\n") + "\n",
	})
	stdout, stderr, code, _ := f.run(t)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("want the 1 valid line kept, got %d: %s", len(lines), stdout)
	}
	// Only the two non-object lines count as malformed; the labeled object
	// is valid JSON and simply stays out of the summary.
	if stderr != "metrics export: skipped 2 malformed line(s)\n" {
		t.Fatalf("stderr=%q want exactly the single warning", stderr)
	}
}

func TestExportNoFiles(t *testing.T) {
	t.Run("empty state root", func(t *testing.T) {
		f := newExportFixture(t, nil)
		before := treeSnapshot(t, f.state)
		stdout, stderr, code, _ := f.run(t)
		if code != 0 || stdout != "" || stderr != "" {
			t.Fatalf("exit=%d stdout=%q stderr=%q; want 0 and no output", code, stdout, stderr)
		}
		if !sameTree(treeSnapshot(t, f.state), before) {
			t.Fatalf("the state root changed: %v vs %v", treeSnapshot(t, f.state), before)
		}
	})
	t.Run("missing state root", func(t *testing.T) {
		f := newExportFixture(t, nil)
		missing := filepath.Join(t.TempDir(), "no-such-state")
		f.env["SOHO_DIR"] = missing
		before := treeSnapshot(t, f.proj)
		stdout, stderr, code, _ := f.run(t)
		if code != 0 || stdout != "" || stderr != "" {
			t.Fatalf("exit=%d stdout=%q stderr=%q; want 0 and no output", code, stdout, stderr)
		}
		if _, err := os.Stat(missing); !os.IsNotExist(err) {
			t.Fatalf("the missing state root was created (err=%v)", err)
		}
		if !sameTree(treeSnapshot(t, f.proj), before) {
			t.Fatalf("the project dir changed: %v vs %v", treeSnapshot(t, f.proj), before)
		}
	})
}

func TestExportInSkillRefuses(t *testing.T) {
	// From inside the skill the state root would land inside the skill, so
	// the standard state refusal applies (exit 2, nothing written) — the
	// choice that matches stats and session.
	skill := t.TempDir()
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("# skill\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(skill, "roles"), 0o700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(skill, "state")
	settled := `{"ts":"2026-09-30T21:00:00Z","agent":"w","report":"a.md","kind":"claude","model":"claude-opus","items":{"done":1,"partial":0,"skipped":0}}`
	if err := os.MkdirAll(filepath.Join(state, "ws1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "ws1", "metrics.jsonl"), []byte(settled+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := newExportFixture(t, nil)
	f.env["SOHO_SKILL_DIR"] = skill
	f.env["SOHO_DIR"] = state
	before := treeSnapshot(t, skill)
	stdout, stderr, code, message := f.run(t)
	if code != 2 {
		t.Fatalf("exit=%d want 2 (stderr=%q msg=%q)", code, stderr, message)
	}
	if stdout != "" {
		t.Fatalf("stdout=%q; the refusal must print nothing to stdout", stdout)
	}
	if !strings.Contains(message, "would be inside the soho skill") || !strings.Contains(message, "(nothing was written)") {
		t.Fatalf("message=%q is not the standard state refusal", message)
	}
	if !strings.HasPrefix(message, "metrics export: ") {
		t.Fatalf("message=%q is not prefixed with the command", message)
	}
	if !sameTree(treeSnapshot(t, skill), before) {
		t.Fatalf("the skill tree changed: %v vs %v", treeSnapshot(t, skill), before)
	}
}

func TestExportUnknownOption(t *testing.T) {
	f := newExportFixture(t, nil)
	for _, args := range [][]string{
		{"--bogus"},
		{"worker.md"},
		{"--since", "2026-01-01", "--bogus"},
	} {
		_, stderr, code, message := f.run(t, args...)
		if code != 2 {
			t.Fatalf("args %v exit=%d want 2 (stderr=%q)", args, code, stderr)
		}
		if message != usageLine {
			t.Fatalf("args %v message=%q want the skeleton usage line", args, message)
		}
	}
	// Routed through the skeleton: metrics export <flag> reaches the parser.
	code, message := f.runMetrics(t, "export", "--bogus")
	if code != 2 || message != usageLine {
		t.Fatalf("routed exit=%d message=%q", code, message)
	}
}

func TestExportSortedByTs(t *testing.T) {
	f := newExportFixture(t, map[string]string{
		// The later ts comes first in the file; the output must sort by ts.
		"ws1/metrics.jsonl": strings.Join([]string{
			`{"ts":"2026-09-30T22:00:00Z","agent":"w2","report":"b.md","kind":"codex","model":"gpt-5","items":{"done":1,"partial":0,"skipped":0}}`,
			`{"ts":"2026-09-30T21:00:00Z","agent":"w1","report":"a.md","kind":"claude","model":"claude-opus","items":{"done":1,"partial":0,"skipped":0}}`,
		}, "\n") + "\n",
	})
	stdout, stderr, code, _ := f.run(t)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 output lines, got %d: %s", len(lines), stdout)
	}
	if !strings.Contains(lines[0], `"kind":"claude"`) || !strings.Contains(lines[1], `"kind":"codex"`) {
		t.Fatalf("output not sorted by ts: %s", stdout)
	}
}

func TestExportNestedClosedLists(t *testing.T) {
	// R-M3 finding 1: items and severity are projected to their closed keys,
	// like for; a value that is not an object drops the whole field.
	// R-M3 finding 2: more labels than findings clamp findings_unlabeled at 0.
	f := newExportFixture(t, map[string]string{
		"ws1/metrics.jsonl": strings.Join([]string{
			`{"ts":"2026-09-30T21:00:00Z","agent":"reviewer","report":"nested.md","role":"reviewer","kind":"codex","model":"gpt-5","family":"openai","items":{"done":1,"partial":0,"skipped":0,"path":"/x"},"findings":1,"severity":{"P0":0,"P1":0,"P2":0,"P3":0,"url":"https://x"},"verdict_effective":"fail"}`,
			`{"ts":"2026-09-30T21:05:00Z","label":"mark","report":"nested.md","findings":{"1":"real","2":"real"},"missed":{"P2":1}}`,
			`{"ts":"2026-09-30T21:10:00Z","agent":"w","report":"bad.md","role":"implementer","kind":"claude","model":"claude-opus","items":"oops","severity":"high"}`,
		}, "\n") + "\n",
	})
	stdout, stderr, code, _ := f.run(t)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 output lines, got %d: %s", len(lines), stdout)
	}
	var nested, bad map[string]json.RawMessage
	if err := json.Unmarshal([]byte(lines[0]), &nested); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &bad); err != nil {
		t.Fatal(err)
	}
	// The stray keys inside items and severity do not reach the output.
	var itemsRaw map[string]json.RawMessage
	if err := json.Unmarshal(nested["items"], &itemsRaw); err != nil || len(itemsRaw) != 3 {
		t.Fatalf("items=%v want exactly done/partial/skipped (no path)", itemsRaw)
	}
	var severityRaw map[string]json.RawMessage
	if err := json.Unmarshal(nested["severity"], &severityRaw); err != nil || len(severityRaw) != 4 {
		t.Fatalf("severity=%v want exactly P0..P3 (no url)", severityRaw)
	}
	for _, key := range []string{"path", "url"} {
		if _, ok := itemsRaw[key]; ok {
			t.Fatalf("items keeps the stray key %s: %v", key, itemsRaw)
		}
		if _, ok := severityRaw[key]; ok {
			t.Fatalf("severity keeps the stray key %s: %v", key, severityRaw)
		}
	}
	// Two labels on a report with findings=1: real=2, unlabeled clamps at 0.
	if string(nested["findings_real"]) != "2" || string(nested["findings_false"]) != "0" {
		t.Fatalf("final counts = real:%v false:%v want 2/0: %s", nested["findings_real"], nested["findings_false"], lines[0])
	}
	if string(nested["findings_unlabeled"]) != "0" {
		t.Fatalf("findings_unlabeled=%v want 0 (clamped, never negative): %s", nested["findings_unlabeled"], lines[0])
	}
	var missed map[string]int
	if err := json.Unmarshal(nested["missed"], &missed); err != nil || missed["P2"] != 1 {
		t.Fatalf("missed=%v want {P2:1}", missed)
	}
	// A value that is not an object drops the whole field.
	if _, ok := bad["items"]; ok {
		t.Fatalf("non-object items must drop the field: %s", lines[1])
	}
	if _, ok := bad["severity"]; ok {
		t.Fatalf("non-object severity must drop the field: %s", lines[1])
	}
	if string(bad["kind"]) != `"claude"` || string(bad["model"]) != `"claude-opus"` {
		t.Fatalf("the rest of the line must stay: %s", lines[1])
	}
}

// TestExportCarriesFindingOrigins (DJA-684 slice B): the settled line's
// items.origins joins the anonymized export with its closed origin keys, and
// a class slug inside origins stays out like every other stray key.
func TestExportCarriesFindingOrigins(t *testing.T) {
	const settled = `{"ts":"2026-09-30T22:00:00Z","agent":"rev","report":"rev.md","role":"reviewer","kind":"codex","model":"gpt-5","effort":"max","family":"openai","type":"review","duration_s":80,"items":{"done":2,"partial":0,"skipped":0,"origins":{"brief":1,"impl":1,"unknown":1,"class":"missing-bound"}},"verdict":"pass","findings":3,"severity":{"P0":0,"P1":0,"P2":2,"P3":0},"verdict_effective":"fail"}`
	f := newExportFixture(t, map[string]string{"ws1/metrics.jsonl": settled + "\n"})
	stdout, stderr, code, _ := f.run(t)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	line := strings.TrimSuffix(stdout, "\n")
	if !strings.Contains(line, `"origins":{"brief":1,"impl":1,"unknown":1}`) {
		t.Fatalf("items.origins lost by the export: %q", line)
	}
	if strings.Contains(line, "missing-bound") || strings.Contains(line, `"class"`) {
		t.Fatalf("a class slug leaked into the export line: %q", line)
	}
}
