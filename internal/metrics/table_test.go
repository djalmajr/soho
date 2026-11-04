package metrics

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

// tableRun executes CmdTable with the captured stdout/stderr, recovering the
// platform.ExitError the way cli.Run does (message to stderr, its code as
// the return value).
func tableRun(t *testing.T, args []string) (code int, out, err string) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	oldOut, oldErr := platform.Stdout, platform.Stderr
	platform.Stdout, platform.Stderr = &outBuf, &errBuf
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	defer func() {
		r := recover()
		if r == nil {
			out, err = outBuf.String(), errBuf.String()
			return
		}
		ee, ok := r.(*platform.ExitError)
		if !ok {
			t.Fatalf("unexpected panic: %v", r)
		}
		code = ee.Code
		if ee.Msg != "" {
			_, _ = errBuf.WriteString("soho: " + ee.Msg + "\n")
		}
		out, err = outBuf.String(), errBuf.String()
	}()
	code = CmdTable(args, CommandContext{})
	return
}

func tableWriteFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

func tableJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshaling %v: %v", v, err)
	}
	return string(b)
}

// tableRepeat builds n JSON lines differing only in the session field.
func tableRepeat(t *testing.T, n int, base map[string]any) string {
	t.Helper()
	var b strings.Builder
	for i := 0; i < n; i++ {
		line := make(map[string]any, len(base)+1)
		for k, v := range base {
			line[k] = v
		}
		line["session"] = i
		b.WriteString(tableJSON(t, line))
		b.WriteString("\n")
	}
	return b.String()
}

const tableHeader = "| Role | Type | Kind | Model | Effort | n | Median min | Partial | Precision | Missed | Confidence |\n| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n"

// TestTable_CoreGroupAndColumns covers grouping, the even and odd medians,
// Partial, Precision (k), Missed and the low band in one deterministic
// table.
func TestTable_CoreGroupAndColumns(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		// group A: reviewer × review × patch × alpha × high, n=4.
		tableJSON(t, map[string]any{"project": "p", "role": "reviewer", "type": "review", "kind": "patch", "model": "alpha", "effort": "high", "duration_s": 600, "items": map[string]any{"done": 1, "partial": 1, "skipped": 0}, "findings_real": 3, "findings_false": 1, "missed": map[string]any{"P0": 1, "P1": 2}}),
		tableJSON(t, map[string]any{"project": "p", "role": "reviewer", "type": "review", "kind": "patch", "model": "alpha", "effort": "high", "duration_s": 1800, "items": map[string]any{"partial": 0}, "findings_real": 1, "missed": map[string]any{"P2": 1}}),
		tableJSON(t, map[string]any{"project": "p", "role": "reviewer", "type": "review", "kind": "patch", "model": "alpha", "effort": "high", "duration_s": 1200, "items": map[string]any{"done": 2, "partial": 0, "skipped": 1}, "amendment": true}),
		tableJSON(t, map[string]any{"project": "p", "role": "reviewer", "type": "review", "kind": "patch", "model": "alpha", "effort": "high", "duration_s": 2400}),
		// group B: implementer × build × fix × beta × low, n=3.
		tableJSON(t, map[string]any{"project": "p", "role": "implementer", "type": "build", "kind": "fix", "model": "beta", "effort": "low", "duration_s": 300, "items": map[string]any{"partial": 2}}),
		tableJSON(t, map[string]any{"project": "p", "role": "implementer", "type": "build", "kind": "fix", "model": "beta", "effort": "low", "duration_s": 900, "items": map[string]any{"partial": 0}}),
		tableJSON(t, map[string]any{"project": "p", "role": "implementer", "type": "build", "kind": "fix", "model": "beta", "effort": "low", "duration_s": 1500}),
	}
	file := tableWriteFile(t, dir, "export.jsonl", strings.Join(lines, "\n")+"\n")

	code, out, err := tableRun(t, []string{file})
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, err)
	}
	if err != "" {
		t.Fatalf("stderr = %q, want empty", err)
	}
	// group A: even median (1200+1800)/2=1500 -> 25.0; Partial 1/3 -> 33%;
	// Precision (3+1)/(4+1) -> 80% (5); Missed 1+2+1=4; n=4 -> low.
	// group B: odd median 900 -> 15.0; Partial 1/2 -> 50%; not a review
	// role, so Precision and Missed are "-"; n=3 -> low.
	want := tableHeader +
		"| implementer | build | fix | beta | low | 3 | 15.0 | 50% | - | - | low |\n" +
		"| reviewer | review | patch | alpha | high | 4 | 25.0 | 33% | 80% (5) | 4 | low |\n"
	if out != want {
		t.Fatalf("stdout =\n%s\nwant:\n%s", out, want)
	}
}

// TestTable_MedianEvenOdd pins the median with 2, 3 and 1 durations, one
// decimal, in minutes.
func TestTable_MedianEvenOdd(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		tableJSON(t, map[string]any{"project": "p", "role": "a", "model": "m2even", "duration_s": 120}),
		tableJSON(t, map[string]any{"project": "p", "role": "a", "model": "m2even", "duration_s": 240}),
		tableJSON(t, map[string]any{"project": "p", "role": "a", "model": "m3odd", "duration_s": 60}),
		tableJSON(t, map[string]any{"project": "p", "role": "a", "model": "m3odd", "duration_s": 300}),
		tableJSON(t, map[string]any{"project": "p", "role": "a", "model": "m3odd", "duration_s": 61}),
		tableJSON(t, map[string]any{"project": "p", "role": "a", "model": "m1single", "duration_s": 90}),
	}
	file := tableWriteFile(t, dir, "export.jsonl", strings.Join(lines, "\n")+"\n")

	code, out, err := tableRun(t, []string{file})
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, err)
	}
	if err != "" {
		t.Fatalf("stderr = %q, want empty", err)
	}
	// n descending: m3odd, m2even, m1single.
	want := tableHeader +
		"| a | - | - | m3odd | - | 3 | 1.0 | - | - | - | low |\n" +
		"| a | - | - | m2even | - | 2 | 3.0 | - | - | - | low |\n" +
		"| a | - | - | m1single | - | 1 | 1.5 | - | - | - | low |\n"
	if out != want {
		t.Fatalf("stdout =\n%s\nwant:\n%s", out, want)
	}
}

// TestTable_ConfidenceBands covers the three bands at their boundaries: n=4
// low, n=5 medium, n=19 medium, n=20 high.
func TestTable_ConfidenceBands(t *testing.T) {
	dir := t.TempDir()
	base := map[string]any{"project": "p", "role": "r", "type": "t", "kind": "k", "effort": "e"}
	content := ""
	for _, band := range []struct {
		model string
		n     int
	}{{"m4", 4}, {"m5", 5}, {"m19", 19}, {"m20", 20}} {
		b := make(map[string]any, len(base))
		for k, v := range base {
			b[k] = v
		}
		b["model"] = band.model
		content += tableRepeat(t, band.n, b)
	}
	file := tableWriteFile(t, dir, "export.jsonl", content)

	code, out, err := tableRun(t, []string{file})
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, err)
	}
	if err != "" {
		t.Fatalf("stderr = %q, want empty", err)
	}
	// same role/type: n descending puts m20 first, then m19, m5, m4.
	want := tableHeader +
		"| r | t | k | m20 | e | 20 | - | - | - | - | high |\n" +
		"| r | t | k | m19 | e | 19 | - | - | - | - | medium |\n" +
		"| r | t | k | m5 | e | 5 | - | - | - | - | medium |\n" +
		"| r | t | k | m4 | e | 4 | - | - | - | - | low |\n"
	if out != want {
		t.Fatalf("stdout =\n%s\nwant:\n%s", out, want)
	}
}

// TestTable_OrderDeterministic checks role/type alphabetical, n descending,
// kind/model/effort alphabetical, and that shuffling the input lines does
// not change the output.
func TestTable_OrderDeterministic(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		tableJSON(t, map[string]any{"project": "p", "role": "b", "type": "t", "kind": "a", "model": "m", "effort": "e"}),
		tableJSON(t, map[string]any{"project": "p", "role": "a", "type": "t", "kind": "z", "model": "m", "effort": "e"}),
		tableJSON(t, map[string]any{"project": "p", "role": "a", "type": "t", "kind": "z", "model": "m", "effort": "e"}),
		tableJSON(t, map[string]any{"project": "p", "role": "a", "type": "t", "kind": "z", "model": "m", "effort": "e"}),
		tableJSON(t, map[string]any{"project": "p", "role": "a", "type": "t", "kind": "a", "model": "m", "effort": "e"}),
		tableJSON(t, map[string]any{"project": "p", "role": "a", "type": "t", "kind": "a", "model": "m", "effort": "e"}),
		tableJSON(t, map[string]any{"project": "p", "role": "a", "type": "t", "kind": "a", "model": "m", "effort": "e"}),
		tableJSON(t, map[string]any{"project": "p", "role": "a", "type": "t", "kind": "a", "model": "m", "effort": "f"}),
		tableJSON(t, map[string]any{"project": "p", "role": "a", "type": "t", "kind": "a", "model": "m", "effort": "f"}),
		tableJSON(t, map[string]any{"project": "p", "role": "a", "type": "t2", "kind": "a", "model": "m", "effort": "e"}),
	}
	file := tableWriteFile(t, dir, "export.jsonl", strings.Join(lines, "\n")+"\n")

	code, out, err := tableRun(t, []string{file})
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, err)
	}
	if err != "" {
		t.Fatalf("stderr = %q, want empty", err)
	}
	// (a,t): the two n=3 groups by kind (a before z), then n=2 by kind/model
	// alphabetical, then (a,t2); role b last with its own n.
	want := tableHeader +
		"| a | t | a | m | e | 3 | - | - | - | - | low |\n" +
		"| a | t | z | m | e | 3 | - | - | - | - | low |\n" +
		"| a | t | a | m | f | 2 | - | - | - | - | low |\n" +
		"| a | t2 | a | m | e | 1 | - | - | - | - | low |\n" +
		"| b | t | a | m | e | 1 | - | - | - | - | low |\n"
	if out != want {
		t.Fatalf("stdout =\n%s\nwant:\n%s", out, want)
	}

	// Shuffled input must produce the same table.
	shuffled := tableWriteFile(t, dir, "shuffled.jsonl",
		lines[8]+"\n"+lines[0]+"\n"+lines[5]+"\n"+lines[2]+"\n"+lines[9]+"\n"+lines[6]+"\n"+lines[1]+"\n"+lines[7]+"\n"+lines[3]+"\n"+lines[4]+"\n")
	code, out2, err := tableRun(t, []string{shuffled})
	if code != 0 {
		t.Fatalf("shuffled exit = %d, stderr = %q", code, err)
	}
	if out2 != out {
		t.Fatalf("shuffled stdout differs:\n%s\nvs\n%s", out2, out)
	}
}

// TestTable_DashForMissingColumns covers "-" for every column without data: the
// five group keys, Median min, Partial, Precision (also for a review role
// without labels), Missed (also "-" for a non-review role).
func TestTable_DashForMissingColumns(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		tableJSON(t, map[string]any{"project": "p", "role": "scouter"}),
		tableJSON(t, map[string]any{"project": "p", "role": "reviewer"}),
	}
	file := tableWriteFile(t, dir, "export.jsonl", strings.Join(lines, "\n")+"\n")

	code, out, err := tableRun(t, []string{file})
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, err)
	}
	if err != "" {
		t.Fatalf("stderr = %q, want empty", err)
	}
	want := tableHeader +
		"| reviewer | - | - | - | - | 1 | - | - | - | 0 | low |\n" +
		"| scouter | - | - | - | - | 1 | - | - | - | - | low |\n"
	if out != want {
		t.Fatalf("stdout =\n%s\nwant:\n%s", out, want)
	}
}

const tableWriteBefore = "# Profiles\n\ntext before the block\n\n"
const tableWriteAfter = "\ntext after the block\n\nlast line without newline"

func tableMarkerBlock() string {
	return "<!-- soho:metrics-table:start -->\n" +
		"_No field summaries yet._\n" +
		"<!-- soho:metrics-table:end -->"
}

// TestTable_WriteBlockOnly writes one row into a file with text around the
// markers and compares the bytes outside the block.
func TestTable_WriteBlockOnly(t *testing.T) {
	dir := t.TempDir()
	original := tableWriteBefore + tableMarkerBlock() + tableWriteAfter
	target := tableWriteFile(t, dir, "agent-profiles.md", original)
	export := tableWriteFile(t, dir, "export.jsonl",
		tableJSON(t, map[string]any{"project": "p", "role": "implementer", "type": "build", "kind": "fix", "model": "beta", "effort": "low"})+"\n")

	code, out, err := tableRun(t, []string{"--write", target, export})
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, err)
	}
	if err != "" {
		t.Fatalf("stderr = %q, want empty", err)
	}
	wantOut := "metrics table: wrote 1 row(s) to " + target + "\n"
	if out != wantOut {
		t.Fatalf("stdout = %q, want %q", out, wantOut)
	}
	got, rerr := os.ReadFile(target)
	if rerr != nil {
		t.Fatalf("reading target: %v", rerr)
	}
	before, after := original[:len(tableWriteBefore)], original[tableEndMarkerIndex(original):]
	if !strings.HasPrefix(string(got), before) {
		t.Fatalf("bytes before the start marker changed:\n%q\nvs\n%q", string(got)[:len(before)], before)
	}
	if !strings.HasSuffix(string(got), after) {
		t.Fatalf("bytes from the end marker on changed:\n%q\nvs\n%q", string(got)[len(got)-len(after):], after)
	}
	want := tableWriteBefore +
		"<!-- soho:metrics-table:start -->\n\n" +
		tableHeader +
		"| implementer | build | fix | beta | low | 1 | - | - | - | - | low |\n\n" +
		"<!-- soho:metrics-table:end -->" + tableWriteAfter
	if string(got) != want {
		t.Fatalf("file =\n%s\nwant:\n%s", string(got), want)
	}
}

// TestTable_WriteInvalidMarkers: missing markers, a second pair, and the end
// before the start all exit 2 without writing.
func TestTable_WriteInvalidMarkers(t *testing.T) {
	dir := t.TempDir()
	export := tableWriteFile(t, dir, "export.jsonl",
		tableJSON(t, map[string]any{"project": "p", "role": "r"})+"\n")
	cases := []struct {
		name    string
		content string
	}{
		{"no markers", "just text\nno markers here\n"},
		{"two pairs", tableMarkerBlock() + "\n\n" + tableMarkerBlock() + "\n"},
		{"end before start", "<!-- soho:metrics-table:end -->\n" +
			"_No field summaries yet._\n" +
			"<!-- soho:metrics-table:start -->\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := tableWriteFile(t, t.TempDir(), "agent-profiles.md", tc.content)
			code, out, err := tableRun(t, []string{"--write", target, export})
			if code != 2 {
				t.Fatalf("exit = %d, want 2; stderr = %q", code, err)
			}
			if out != "" {
				t.Fatalf("stdout = %q, want empty", out)
			}
			if !strings.Contains(err, "metrics table: "+target+":") {
				t.Fatalf("stderr = %q, want the path in a metrics table error", err)
			}
			got, rerr := os.ReadFile(target)
			if rerr != nil {
				t.Fatalf("reading target: %v", rerr)
			}
			if string(got) != tc.content {
				t.Fatalf("file changed:\n%s\nvs\n%s", string(got), tc.content)
			}
		})
	}
}

// TestTable_MalformedWarning skips non-object JSON lines and prints one warning
// with the total.
func TestTable_MalformedWarning(t *testing.T) {
	dir := t.TempDir()
	validA := tableJSON(t, map[string]any{"project": "p", "role": "a"})
	validB := tableJSON(t, map[string]any{"project": "p", "role": "b"})
	file := tableWriteFile(t, dir, "export.jsonl",
		validA+"\n"+
			"not json at all\n"+
			"[1, 2]\n"+
			"42\n"+
			validB+"\n"+
			"\n"+
			"null\n"+
			`{"broken": `+"\n")

	code, out, err := tableRun(t, []string{file})
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, err)
	}
	if err != "metrics table: skipped 5 malformed line(s)\n" {
		t.Fatalf("stderr = %q, want exactly the one warning", err)
	}
	want := tableHeader +
		"| a | - | - | - | - | 1 | - | - | - | - | low |\n" +
		"| b | - | - | - | - | 1 | - | - | - | - | low |\n"
	if out != want {
		t.Fatalf("stdout =\n%s\nwant:\n%s", out, want)
	}
}

// TestTable_MissingFile exits 4 with the path.
func TestTable_MissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.jsonl")
	code, out, err := tableRun(t, []string{missing})
	if code != 4 {
		t.Fatalf("exit = %d, want 4", code)
	}
	if out != "" {
		t.Fatalf("stdout = %q, want empty", out)
	}
	if !strings.Contains(err, "metrics table: cannot read "+missing) {
		t.Fatalf("stderr = %q, want the path", err)
	}
}

// TestTable_NoFiles exits 2 with the usage line, with or without --write.
func TestTable_NoFiles(t *testing.T) {
	for _, args := range [][]string{{}, {"--write", "out.md"}} {
		code, out, err := tableRun(t, args)
		if code != 2 {
			t.Fatalf("args %v: exit = %d, want 2", args, code)
		}
		if out != "" {
			t.Fatalf("stdout = %q, want empty", out)
		}
		if !strings.Contains(err, usageLine) {
			t.Fatalf("stderr = %q, want the usage line", err)
		}
	}
}

// TestTable_ZeroLines prints the placeholder text and, with --write, rewrites
// the block with it (0 rows).
func TestTable_ZeroLines(t *testing.T) {
	dir := t.TempDir()
	file := tableWriteFile(t, dir, "export.jsonl", "not json\n\n")

	code, out, err := tableRun(t, []string{file})
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, err)
	}
	if out != "_No field summaries yet._\n" {
		t.Fatalf("stdout = %q, want the placeholder", out)
	}
	if err != "metrics table: skipped 1 malformed line(s)\n" {
		t.Fatalf("stderr = %q, want the warning", err)
	}

	target := tableWriteFile(t, dir, "agent-profiles.md", "lead\n"+tableMarkerBlock()+"\ntail\n")
	code, out, err = tableRun(t, []string{"--write", target, file})
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, err)
	}
	if out != "metrics table: wrote 0 row(s) to "+target+"\n" {
		t.Fatalf("stdout = %q", out)
	}
	got, rerr := os.ReadFile(target)
	if rerr != nil {
		t.Fatalf("reading target: %v", rerr)
	}
	want := "lead\n<!-- soho:metrics-table:start -->\n\n_No field summaries yet._\n\n<!-- soho:metrics-table:end -->\ntail\n"
	if string(got) != want {
		t.Fatalf("file =\n%s\nwant:\n%s", string(got), want)
	}
}

// TestTable_WriteRealProfiles runs --write on a temporary copy of the real
// skills/soho/references/agent-profiles.md: the placeholder is
// replaced by the table and nothing outside the markers changes.
func TestTable_WriteRealProfiles(t *testing.T) {
	originalPath := filepath.Join("..", "..", "skills", "soho", "references", "agent-profiles.md")
	original, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatalf("reading %s: %v", originalPath, err)
	}
	orig := string(original)
	if !strings.Contains(orig, "_No field summaries yet._") {
		t.Fatalf("the repository agent-profiles.md no longer carries the placeholder; refusing to run")
	}
	dir := t.TempDir()
	target := tableWriteFile(t, dir, "agent-profiles.md", orig)
	export := tableWriteFile(t, dir, "export.jsonl",
		tableJSON(t, map[string]any{"project": "p", "role": "planner", "type": "plan", "kind": "roadmap", "model": "omega", "effort": "high"})+"\n")

	code, out, stderr := tableRun(t, []string{"--write", target, export})
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	if out != "metrics table: wrote 1 row(s) to "+target+"\n" {
		t.Fatalf("stdout = %q", out)
	}
	got, rerr := os.ReadFile(target)
	if rerr != nil {
		t.Fatalf("reading target: %v", rerr)
	}
	gotS := string(got)
	if strings.Contains(gotS, "_No field summaries yet._") {
		t.Fatalf("placeholder survived the write")
	}
	row := "| planner | plan | roadmap | omega | high | 1 | - | - | - | - | low |\n"
	if !strings.Contains(gotS, row) {
		t.Fatalf("table row %q missing from:\n%s", row, gotS)
	}
	// Byte for byte outside the markers.
	startIdx := strings.Index(orig, "<!-- soho:metrics-table:start -->")
	endIdx := strings.Index(orig[startIdx+1:], "<!-- soho:metrics-table:end -->") + startIdx + 1
	before, after := orig[:startIdx], orig[endIdx:]
	if !strings.HasPrefix(gotS, before) {
		t.Fatalf("bytes before the markers changed")
	}
	if !strings.HasSuffix(gotS, after) {
		t.Fatalf("bytes from the end marker on changed")
	}
	if !strings.Contains(gotS, "<!-- soho:metrics-table:start -->\n\n"+tableHeader+row+"\n<!-- soho:metrics-table:end -->") {
		t.Fatalf("the block is not the blank-line-wrapped table:\n%s", gotS[startIdx:endIdx+len("<!-- soho:metrics-table:end -->")+1])
	}
}

// tableEndMarkerIndex locates the end marker in the write fixture.
func tableEndMarkerIndex(s string) int {
	return strings.Index(s, "<!-- soho:metrics-table:end -->")
}

// TestTable_EndToEndExportAndTable is the point-to-point check: it builds a
// state metrics.jsonl with one settled review line and one mark line, runs
// CmdExport, writes the export to a file, and runs CmdTable over it. The
// table row's Precision and Missed must come from the mark.
func TestTable_EndToEndExportAndTable(t *testing.T) {
	f := newExportFixture(t, map[string]string{
		"ws1/metrics.jsonl": strings.Join([]string{
			`{"ts":"2026-09-30T21:00:00Z","report":"rev.md","role":"reviewer","type":"review","kind":"codex","model":"gpt-5","family":"openai","effort":"high","duration_s":1800,"items":{"done":2,"partial":1,"skipped":0},"verdict":"fail","findings":3,"severity":{"P0":0,"P1":1,"P2":2,"P3":0},"verdict_effective":"fail"}`,
			`{"ts":"2026-09-30T23:00:00Z","label":"mark","report":"rev.md","findings":{"1":"real","2":"real","3":"false"},"missed":{"P2":1}}`,
		}, "\n") + "\n",
	})
	exportOut, exportErr, exportCode, _ := f.run(t)
	if exportCode != 0 {
		t.Fatalf("export exit = %d, stderr = %q", exportCode, exportErr)
	}
	exportFile := tableWriteFile(t, t.TempDir(), "export.jsonl", exportOut)

	code, out, err := tableRun(t, []string{exportFile})
	if code != 0 {
		t.Fatalf("table exit = %d, stderr = %q", code, err)
	}
	if err != "" {
		t.Fatalf("table stderr = %q, want empty", err)
	}
	// n=1; median 1800/60=30.0; Partial 1/1=100%; Precision 2/(2+1)=67% (3)
	// and Missed P2:1 both come from the mark line; n=1 -> low.
	want := tableHeader +
		"| reviewer | review | codex | gpt-5 | high | 1 | 30.0 | 100% | 67% (3) | 1 | low |\n"
	if out != want {
		t.Fatalf("table stdout =\n%s\nwant:\n%s\nexport was:\n%s", out, want, exportOut)
	}
}

// TestTable_CellEscaping: | becomes \| (the row keeps exactly 11 cells),
// a backslash is doubled so \| is unambiguous, \r/\n become spaces (one line
// per row), and grouping/ordering keep the original values, so models that
// differ only by a | stay in separate groups.
func TestTable_CellEscaping(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		tableJSON(t, map[string]any{"project": "p", "role": "r", "model": "alpha|beta"}),
		tableJSON(t, map[string]any{"project": "p", "role": "r", "model": "alpha\nbeta"}),
		tableJSON(t, map[string]any{"project": "p", "role": "r", "model": "a|b"}),
		tableJSON(t, map[string]any{"project": "p", "role": "r", "model": "ab"}),
		tableJSON(t, map[string]any{"project": "p", "role": "r", "model": "a\\b"}),
	}
	file := tableWriteFile(t, dir, "export.jsonl", strings.Join(lines, "\n")+"\n")

	code, out, err := tableRun(t, []string{file})
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, err)
	}
	if err != "" {
		t.Fatalf("stderr = %q, want empty", err)
	}
	// Raw model values sort byte-wise (backslash 0x5C < b < l < |): a\b, ab,
	// alpha\nbeta, alpha|beta, a|b. Only the printed cells change.
	want := tableHeader +
		"| r | - | - | a\\\\b | - | 1 | - | - | - | - | low |\n" +
		"| r | - | - | ab | - | 1 | - | - | - | - | low |\n" +
		"| r | - | - | alpha beta | - | 1 | - | - | - | - | low |\n" +
		"| r | - | - | alpha\\|beta | - | 1 | - | - | - | - | low |\n" +
		"| r | - | - | a\\|b | - | 1 | - | - | - | - | low |\n"
	if out != want {
		t.Fatalf("stdout =\n%s\nwant:\n%s", out, want)
	}
	// Exactly one line per row: header, separator and 5 data rows.
	if got := strings.Count(out, "\n"); got != 7 {
		t.Fatalf("stdout has %d lines, want 7 (a newline value must not split a row)", got)
	}
	// The alpha|beta row keeps exactly 11 cells: 12 unescaped pipes, the
	// pipe inside the cell is escaped.
	rows := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if got := tableUnescapedPipes(rows[5]); got != 12 {
		t.Fatalf("row %q has %d unescaped pipes, want 12 (11 cells)", rows[5], got)
	}
	// Models that differ only by a | (ab vs a|b) stay in separate groups:
	// both rows are present, unchanged in grouping.
	for _, wantRow := range []string{
		"| r | - | - | ab | - | 1 | - | - | - | - | low |",
		"| r | - | - | a\\|b | - | 1 | - | - | - | - | low |",
	} {
		if !strings.Contains(out, wantRow+"\n") {
			t.Fatalf("row %q missing (groups must not merge across |):\n%s", wantRow, out)
		}
	}
}

// tableUnescapedPipes counts the pipes that are not preceded by a backslash.
func tableUnescapedPipes(row string) int {
	count := 0
	for i := 0; i < len(row); i++ {
		if row[i] == '\\' {
			i++ // skip the escaped character
			continue
		}
		if row[i] == '|' {
			count++
		}
	}
	return count
}

// TestTable_ValidatorCarriesPrecisionAndMissed: every review role shows its
// labels, so a new one cannot hide what it found behind a dash.
func TestTable_ValidatorCarriesPrecisionAndMissed(t *testing.T) {
	line := tableJSON(t, map[string]any{"project": "p", "role": "validator", "findings_real": 4, "findings_false": 1, "missed": map[string]any{"P2": 2}})
	file := tableWriteFile(t, t.TempDir(), "export.jsonl", line+"\n")
	code, out, err := tableRun(t, []string{file})
	if code != 0 || err != "" {
		t.Fatalf("exit = %d, stderr = %q", code, err)
	}
	if !strings.Contains(out, "| validator | - | - | - | - | 1 | - | - | 80% (5) | 2 | low |") {
		t.Fatalf("stdout =\n%s", out)
	}
}
