package metrics

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
)

// markFixture is a workspace whose state dir already holds a settled
// metrics.jsonl: metrics=off by default (no config entry), the report path
// never needs to exist, and no herdr binary is on PATH.
type markFixture struct {
	root  string
	skill string
	ws    string
	cwd   string
	env   platform.Env
	ctx   *core.Config
}

func newMarkFixture(t *testing.T) *markFixture {
	t.Helper()
	root := t.TempDir()
	// The state/skill paths are compared after symlink resolution, like the
	// skill-refusal fixtures do for /tmp on macOS.
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	skill := filepath.Join(root, "skill")
	ws := filepath.Join(root, "state", "ws")
	cwd := filepath.Join(root, "proj")
	for _, dir := range []string{skill, ws, cwd} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("# herdr-soho\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{
		"HOME":               filepath.Join(root, "home"),
		"USERPROFILE":        filepath.Join(root, "home"),
		"XDG_CONFIG_HOME":    filepath.Join(root, "xdg"),
		"PATH":               filepath.Join(root, "bin"),
		"HERDR_WORKSPACE_ID": "ws",
		"SOHO_DIR":           filepath.Join(root, "state"),
		"SOHO_SKILL_DIR":     skill,
	}
	return &markFixture{root: root, skill: skill, ws: ws, cwd: cwd, env: env, ctx: &core.Config{Entries: map[string]core.ConfigEntry{}}}
}

// settled review line the mark command must match: no label, the report
// field, a findings count, a verdict and verdict_effective, like the wait
// settles it with metrics=on.
const markSettledReviewLine = `{"ts":"2026-10-01T20:00:00Z","agent":"rev","report":"review-20261001T191548.md","role":"reviewer","kind":"codex","model":"gpt-5","effort":"max","family":"openai","type":"review","duration_s":312,"session":"20260930T210000","arrival":"queued","items":{"done":3,"partial":0,"skipped":0},"verdict":"pass","findings":2,"severity":{"P0":0,"P1":0,"P2":2,"P3":0},"verdict_effective":"fail"}`
const markSettledImplementerLine = `{"ts":"2026-10-01T20:00:00Z","agent":"build","report":"build-20261001T191548.md","role":"implementer","kind":"claude","model":"claude-opus","effort":"high","family":"anthropic","type":"backend","duration_s":900,"items":{"done":2,"partial":1,"skipped":0}}`

func (f *markFixture) seedLine(t *testing.T, line string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.ws, "metrics.jsonl"), []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *markFixture) lines(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.ws, "metrics.jsonl"))
	if err != nil {
		t.Fatalf("metrics.jsonl: %v", err)
	}
	return strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
}

// run drives CmdMark the way cli.Run does: the ExitError panic is the exit
// code and the message goes to stderr with the soho prefix.
func (f *markFixture) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(f.cwd); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldCwd)
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &errOut
	defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
	code := 0
	func() {
		defer func() {
			value := recover()
			if value == nil {
				return
			}
			exitErr, ok := value.(*platform.ExitError)
			if !ok {
				panic(value)
			}
			if exitErr.Msg != "" {
				_, _ = fmt.Fprintln(platform.Stderr, "soho: "+exitErr.Msg)
			}
			code = exitErr.Code
		}()
		code = CmdMark(args, CommandContext{Config: f.ctx, Env: f.env, Cwd: f.cwd, FrictionLog: filepath.Join(f.ws, "friction.log")})
	}()
	return code, out.String(), errOut.String()
}

var markTsRE = regexp.MustCompile(`^\{"ts":"(20\d{2}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z)","label":"mark","report":"(review|build)-20261001T191548\.md"`)

func (f *markFixture) parseMark(t *testing.T, raw string) *jsonjs.Object {
	t.Helper()
	value, err := jsonjs.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("mark line is not JSON: %v: %q", err, raw)
	}
	obj, ok := value.(*jsonjs.Object)
	if !ok {
		t.Fatalf("mark line is not an object: %q", raw)
	}
	return obj
}

func TestMarkFindingContract(t *testing.T) { // mutation: a wrong report name or an out-of-order mark line breaks the export contract
	t.Run("mark_: --finding 1=real --finding 2=false --missed P2 appends exactly the contract line", func(t *testing.T) {
		f := newMarkFixture(t)
		// metrics is off (no config entry): marking is an explicit action and
		// the settled line already exists.
		f.seedLine(t, markSettledReviewLine)
		// The path does not need to exist; only the file name is used.
		code, out, errOut := f.run(t, "/nonexistent/nowhere/review-20261001T191548.md", "--finding", "1=real", "--finding", "2=false", "--missed", "P2")
		if code != 0 || errOut != "" {
			t.Fatalf("code=%d stderr=%q", code, errOut)
		}
		lines := f.lines(t)
		if len(lines) != 2 || lines[0] != markSettledReviewLine {
			t.Fatalf("metrics.jsonl lines=%q", lines)
		}
		mark := lines[1]
		if out != mark+"\n" {
			t.Fatalf("stdout is not the written line: out=%q mark=%q", out, mark)
		}
		match := markTsRE.FindStringSubmatch(mark)
		if match == nil {
			t.Fatalf("mark line does not start with ts,label,report: %q", mark)
		}
		if _, err := time.Parse(time.RFC3339, match[1]); err != nil || !strings.HasSuffix(match[1], "Z") {
			t.Fatalf("ts is not RFC3339 UTC: %q", match[1])
		}
		if match[2] != "review" {
			t.Fatalf("report field wrong: %q", mark)
		}
		want := `{"ts":"` + match[1] + `","label":"mark","report":"review-20261001T191548.md","findings":{"1":"real","2":"false"},"missed":{"P2":1}}`
		if mark != want {
			t.Fatalf("mark line differs from the contract: got=%q want=%q", mark, want)
		}
	})
	t.Run("mark_: findings keys come out numeric-ordered whatever the command order", func(t *testing.T) {
		f := newMarkFixture(t)
		f.seedLine(t, markSettledReviewLine)
		code, out, errOut := f.run(t, "review-20261001T191548.md", "--finding", "2=false", "--finding", "1=real")
		if code != 0 || errOut != "" {
			t.Fatalf("code=%d stderr=%q", code, errOut)
		}
		if !strings.Contains(out, `"findings":{"1":"real","2":"false"}`) {
			t.Fatalf("findings not numeric-ordered: %q", out)
		}
	})
	t.Run("mark_: missed repeats sum per severity and only received keys appear", func(t *testing.T) {
		f := newMarkFixture(t)
		f.seedLine(t, markSettledReviewLine)
		code, out, errOut := f.run(t, "review-20261001T191548.md", "--missed", "P2", "--missed", "P0", "--missed", "P2")
		if code != 0 || errOut != "" {
			t.Fatalf("code=%d stderr=%q", code, errOut)
		}
		line := f.parseMark(t, strings.TrimSpace(out))
		missedValue, ok := line.Get("missed")
		if !ok {
			t.Fatalf("missed missing: %q", out)
		}
		missed, ok := missedValue.(*jsonjs.Object)
		if !ok || len(missed.Keys()) != 2 {
			t.Fatalf("missed wrong: %q", out)
		}
		p0, ok := missed.Get("P0")
		if !ok || p0 != 1.0 {
			t.Fatalf("missed P0 wrong: %q", out)
		}
		p2, ok := missed.Get("P2")
		if !ok || p2 != 2.0 {
			t.Fatalf("missed P2 wrong: %q", out)
		}
		if _, ok := line.Get("findings"); ok {
			t.Fatalf("findings must be absent when not received: %q", out)
		}
		if _, ok := line.Get("amendment"); ok {
			t.Fatalf("amendment must be absent when not received: %q", out)
		}
	})
	t.Run("mark_: an amendment line accepts --amendment and a later mark keeps the earlier one", func(t *testing.T) {
		f := newMarkFixture(t)
		settled := strings.Replace(markSettledReviewLine, `"verdict_effective":"fail"`, `"verdict_effective":"fail","amendment":true`, 1)
		f.seedLine(t, settled)
		code, out, errOut := f.run(t, "review-20261001T191548.md", "--amendment", "brief", "--missed", "P0")
		if code != 0 || errOut != "" {
			t.Fatalf("code=%d stderr=%q", code, errOut)
		}
		first := strings.TrimSpace(out)
		if !strings.Contains(first, `"missed":{"P0":1},"amendment":"brief"}`) {
			t.Fatalf("amendment mark wrong: %q", first)
		}
		// The later mark of the same report appends: readers apply the lines
		// in file order, and for the same finding the last one wins.
		code, out, errOut = f.run(t, "review-20261001T191548.md", "--finding", "1=false")
		if code != 0 || errOut != "" {
			t.Fatalf("second mark code=%d stderr=%q", code, errOut)
		}
		lines := f.lines(t)
		if len(lines) != 3 {
			t.Fatalf("want 3 lines (settled + 2 marks), got %d: %q", len(lines), lines)
		}
		if lines[1] != first {
			t.Fatalf("the earlier mark was altered: %q", lines[1])
		}
		if !strings.Contains(lines[2], `"findings":{"1":"false"}`) {
			t.Fatalf("the later mark is wrong: %q", lines[2])
		}
	})
}

const usageTail = " — " + usageLine

func TestMarkRefusals(t *testing.T) { // mutation: dropping a guard lets a mark land on the wrong line
	cases := []struct {
		name    string
		settled string
		args    []string
		stderr  string
	}{
		{
			name:    "a report without a settled line",
			settled: markSettledReviewLine,
			args:    []string{"other-20261001T191548.md", "--missed", "P2"},
			stderr:  "no metrics line for other-20261001T191548.md (was metrics on when it settled?)",
		},
		{
			name:    "n above the line's findings",
			settled: markSettledReviewLine,
			args:    []string{"review-20261001T191548.md", "--finding", "3=real"},
			stderr:  "--finding 3 is outside 1..2 (the line's findings)",
		},
		{
			name:    "n zero is not in 1..findings",
			settled: markSettledReviewLine,
			args:    []string{"review-20261001T191548.md", "--finding", "0=real"},
			stderr:  "--finding expects <n>=real|false: '0=real'",
		},
		{
			name:    "the same n twice",
			settled: markSettledReviewLine,
			args:    []string{"review-20261001T191548.md", "--finding", "1=real", "--finding", "1=false"},
			stderr:  "--finding 1 is given more than once",
		},
		{
			name:    "--missed on a line without a verdict",
			settled: markSettledImplementerLine,
			args:    []string{"build-20261001T191548.md", "--missed", "P2"},
			stderr:  "--missed only applies to a review line (one with a verdict); this line is not a review",
		},
		{
			name:    "--finding on a line without findings",
			settled: markSettledImplementerLine,
			args:    []string{"build-20261001T191548.md", "--finding", "1=real"},
			stderr:  "--finding only applies to a review line with a findings count; this line has none",
		},
		{
			name:    "--finding on a review line with zero findings",
			settled: strings.Replace(markSettledReviewLine, `"findings":2,`, `"findings":0,`, 1),
			args:    []string{"review-20261001T191548.md", "--finding", "1=real"},
			stderr:  "--finding only applies to a review line with a findings count; this line has none",
		},
		{
			name:    "--amendment on a line without amendment true",
			settled: markSettledReviewLine,
			args:    []string{"review-20261001T191548.md", "--amendment", "brief"},
			stderr:  `--amendment only applies to a line the task report lists as an amendment ("amendment": true)`,
		},
		{
			name:    "no mark option",
			settled: markSettledReviewLine,
			args:    []string{"review-20261001T191548.md"},
			stderr:  "nothing to mark" + usageTail,
		},
		{
			name:    "no report",
			settled: markSettledReviewLine,
			args:    []string{"--missed", "P2"},
			stderr:  "<report> is required" + usageTail,
		},
		{
			name:    "an invalid finding value",
			settled: markSettledReviewLine,
			args:    []string{"review-20261001T191548.md", "--finding", "1=maybe"},
			stderr:  "--finding expects <n>=real|false: '1=maybe'",
		},
		{
			name:    "a finding value without =",
			settled: markSettledReviewLine,
			args:    []string{"review-20261001T191548.md", "--finding", "1real"},
			stderr:  "--finding expects <n>=real|false: '1real'",
		},
		{
			name:    "an invalid missed severity",
			settled: markSettledReviewLine,
			args:    []string{"review-20261001T191548.md", "--missed", "P5"},
			stderr:  "--missed expects P0|P1|P2|P3: 'P5'",
		},
		{
			name:    "an invalid amendment value",
			settled: markSettledReviewLine,
			args:    []string{"review-20261001T191548.md", "--amendment", "user"},
			stderr:  "--amendment expects implementer|brief: 'user'",
		},
		{
			name:    "an unknown option",
			settled: markSettledReviewLine,
			args:    []string{"review-20261001T191548.md", "--bogus"},
			stderr:  "unknown option --bogus" + usageTail,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newMarkFixture(t)
			f.seedLine(t, tc.settled)
			code, out, errOut := f.run(t, tc.args...)
			want := "soho: " + "metrics mark: " + tc.stderr + "\n"
			if code != 2 || out != "" || errOut != want {
				t.Fatalf("code=%d out=%q stderr=%q, want code 2, no stdout, stderr=%q", code, out, errOut, want)
			}
			if lines := f.lines(t); len(lines) != 1 || lines[0] != tc.settled {
				t.Fatalf("metrics.jsonl changed on a refusal: %q", lines)
			}
		})
	}
}

func TestMarkInsideTheSkill(t *testing.T) { // mutation: skipping the StateDir refusal writes the mark into the skill
	t.Run("mark_: from inside the skill exits 2 and writes nothing", func(t *testing.T) {
		f := newMarkFixture(t)
		f.seedLine(t, markSettledReviewLine)
		skillCwd := filepath.Join(f.skill, "work")
		if err := os.MkdirAll(skillCwd, 0o700); err != nil {
			t.Fatal(err)
		}
		env := f.env.Clone()
		env["SOHO_DIR"] = filepath.Join(f.skill, ".herdr-soho")
		oldCwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(skillCwd); err != nil {
			t.Fatal(err)
		}
		defer os.Chdir(oldCwd)
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var out, errOut bytes.Buffer
		platform.Stdout, platform.Stderr = &out, &errOut
		defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
		code := 0
		func() {
			defer func() {
				value := recover()
				if value == nil {
					return
				}
				exitErr, ok := value.(*platform.ExitError)
				if !ok {
					panic(value)
				}
				if exitErr.Msg != "" {
					_, _ = fmt.Fprintln(platform.Stderr, "soho: "+exitErr.Msg)
				}
				code = exitErr.Code
			}()
			code = CmdMark([]string{"review-20261001T191548.md", "--missed", "P2"}, CommandContext{Config: f.ctx, Env: env, Cwd: skillCwd, FrictionLog: ""})
		}()
		want := "soho: the state dir '" + filepath.Join(f.skill, ".herdr-soho") + "' would be inside the soho skill ('" + f.skill + "'); run soho from the project's directory (nothing was written)\n"
		if code != 2 || out.String() != "" || errOut.String() != want {
			t.Fatalf("code=%d out=%q stderr=%q, want %q", code, out.String(), errOut.String(), want)
		}
		if _, err := os.Stat(filepath.Join(f.skill, ".herdr-soho")); !os.IsNotExist(err) {
			t.Fatalf("the state dir was created inside the skill: %v", err)
		}
		if lines := f.lines(t); len(lines) != 1 || lines[0] != markSettledReviewLine {
			t.Fatalf("metrics.jsonl changed: %q", lines)
		}
	})
}

func TestMarkBriefTextNeverLeaks(t *testing.T) { // mutation: echoing the brief into the mark line leaks it into metrics.jsonl
	t.Run("mark_: the brief text never reaches metrics.jsonl", func(t *testing.T) {
		f := newMarkFixture(t)
		settled := strings.Replace(markSettledReviewLine, `"verdict_effective":"fail"`, `"verdict_effective":"fail","amendment":true`, 1)
		f.seedLine(t, settled)
		brief := "# Brief — metrics probe\n\nType: review\n\nSECRET_BRIEF_TOKEN_123 must never reach metrics.jsonl.\n"
		if err := os.WriteFile(filepath.Join(f.cwd, "brief.md"), []byte(brief), 0o600); err != nil {
			t.Fatal(err)
		}
		code, _, errOut := f.run(t, filepath.Join(f.cwd, "brief.md", "review-20261001T191548.md"), "--finding", "1=real", "--missed", "P2", "--amendment", "brief")
		if code != 0 || errOut != "" {
			t.Fatalf("code=%d stderr=%q", code, errOut)
		}
		raw := strings.Join(f.lines(t), "\n")
		if strings.Contains(raw, "SECRET_BRIEF_TOKEN_123") {
			t.Fatalf("the brief text reached metrics.jsonl: %q", raw)
		}
		if !strings.Contains(raw, `"report":"review-20261001T191548.md"`) {
			t.Fatalf("the mark line must carry the report file name: %q", raw)
		}
	})
}

func TestMarkOpenFailureExits4(t *testing.T) { // mutation: panicking on the open failure exits through the runtime, not 4
	f := newMarkFixture(t)
	f.seedLine(t, markSettledReviewLine)
	path := filepath.Join(f.ws, "metrics.jsonl")
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if probe, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0); err == nil {
		_ = probe.Close()
		t.Skip("this host can write a read-only file (running as root?)")
	}
	code, out, stderr := f.run(t, "review-20261001T191548.md", "--missed", "P2")
	if code != 4 || out != "" || !strings.Contains(stderr, "metrics mark: could not open metrics.jsonl to append the mark line: ") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if _, err := os.Stat(filepath.Join(f.ws, "agents.lock")); !os.IsNotExist(err) {
		t.Fatalf("the state lock was left behind: %v", err)
	}
	if lines := f.lines(t); len(lines) != 1 {
		t.Fatalf("metrics.jsonl changed: %q", lines)
	}
}

// TestMarkFindingIgnoresFindingMarks (DJA-684 slice B, scenario 10):
// mark --finding <n> keeps indexing the settled line's findings count
// whether or not the line carries the new finding_marks and items.origins
// keys.
func TestMarkFindingIgnoresFindingMarks(t *testing.T) {
	const withMarks = `{"ts":"2026-10-01T20:00:00Z","agent":"rev","report":"review-20261001T191548.md","role":"reviewer","kind":"codex","model":"gpt-5","effort":"max","family":"openai","type":"review","duration_s":312,"session":"20260930T210000","arrival":"queued","items":{"done":3,"partial":0,"skipped":0,"origins":{"brief":1,"impl":1,"unknown":0}},"verdict":"pass","findings":2,"severity":{"P0":0,"P1":0,"P2":2,"P3":0},"verdict_effective":"fail","finding_marks":{"brief":1,"impl":1,"unknown":0,"unmarked":1}}`
	const withoutMarks = `{"ts":"2026-10-01T20:00:00Z","agent":"rev","report":"review-20261001T191548.md","role":"reviewer","kind":"codex","model":"gpt-5","effort":"max","family":"openai","type":"review","duration_s":312,"session":"20260930T210000","arrival":"queued","items":{"done":3,"partial":0,"skipped":0},"verdict":"pass","findings":2,"severity":{"P0":0,"P1":0,"P2":2,"P3":0},"verdict_effective":"fail"}`
	normalize := func(t *testing.T, line string) string {
		t.Helper()
		match := markTsRE.FindStringSubmatch(line)
		if match == nil {
			t.Fatalf("mark line does not start with ts,label,report: %q", line)
		}
		return strings.Replace(line, match[1], "TS", 1)
	}
	markRun := func(t *testing.T, seeded string, args ...string) (int, string, string) {
		t.Helper()
		f := newMarkFixture(t)
		f.seedLine(t, seeded)
		return f.run(t, append([]string{"review-20261001T191548.md"}, args...)...)
	}
	t.Run("mark_: --finding 2=real appends the same mark line with finding_marks present", func(t *testing.T) {
		code1, out1, err1 := markRun(t, withMarks, "--finding", "2=real")
		code2, out2, err2 := markRun(t, withoutMarks, "--finding", "2=real")
		if code1 != 0 || err1 != "" {
			t.Fatalf("with marks: code=%d stderr=%q", code1, err1)
		}
		if code2 != 0 || err2 != "" {
			t.Fatalf("without marks: code=%d stderr=%q", code2, err2)
		}
		if normalize(t, out1) != normalize(t, out2) {
			t.Fatalf("the mark line differs with vs without the marks:\nwith=%q\nwithout=%q", out1, out2)
		}
		if !strings.Contains(out1, `"findings":{"2":"real"}`) {
			t.Fatalf("the mark does not point at finding 2: %q", out1)
		}
	})
	t.Run("mark_: --finding 3=real is refused on both lines (findings is 2)", func(t *testing.T) {
		code1, _, err1 := markRun(t, withMarks, "--finding", "3=real")
		code2, _, err2 := markRun(t, withoutMarks, "--finding", "3=real")
		if code1 != 2 || code2 != 2 {
			t.Fatalf("codes = %d and %d, want 2 and 2", code1, code2)
		}
		if !strings.Contains(err1, "outside 1..2") || !strings.Contains(err2, "outside 1..2") {
			t.Fatalf("refusals differ:\nwith=%q\nwithout=%q", err1, err2)
		}
	})
}
