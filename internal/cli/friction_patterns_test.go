package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// DJA-684 fatia C: friction patterns. The message template masks quoted
// strings to <q>, absolute paths to <path> and standalone numbers to <n>,
// in that order; the key is a "[<slug>] " prefix, else the first eight hex
// characters of the sha256 of the masked template.

func sha8Key(t *testing.T, s string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:8]
}

func TestFrictionPatternTemplate(t *testing.T) {
	partialTail := "a partial item is not a pass; read them before commit, push or release"
	cases := []struct {
		name        string
		message     string
		wantKey     string // empty: the sha256-8 of wantPattern
		wantPattern string
	}{
		{"the brief example", "report of 'review-5' marks 3 item(s) partial: " + partialTail,
			"", "report of <q> marks <n> item(s) partial: " + partialTail},
		{"a different agent name, number and count land on the same template", "report of 'impl' marks 12 item(s) partial: " + partialTail,
			"", "report of <q> marks <n> item(s) partial: " + partialTail},
		{"double quotes", "it said \"hello world\" twice", "", "it said <q> twice"},
		{"backticks", "`go test` failed here", "", "<q> failed here"},
		{"an unclosed quote is not a pair", "it said 'oops", "", "it said 'oops"},
		{"apostrophes in words are not quotes", "can't open config, don't retry", "", "can't open config, don't retry"},
		{"different messages with apostrophes stay apart", "can't delete file, don't retry", "", "can't delete file, don't retry"},
		{"a quoted name beside an apostrophe", "can't open 'config.toml' now", "", "can't open <q> now"},
		{"a quoted name with an apostrophe inside", "worker 'don't panic' ended", "", "worker <q> ended"},
		{"two quoted names", "from 'a' to 'b' now", "", "from <q> to <q> now"},
		{"a unix path", "brief /abs/a/brief.md cites 'p1'", "", "brief <path> cites <q>"},
		{"a home path", "ran ~/bin/tool now", "", "ran <path> now"},
		{"a windows drive path", `failed at C:\tools\x.exe`, "", "failed at <path>"},
		{"a path with a space, quoted, is masked whole", `failed at "/abs dir with space/x.md"`, "", "failed at <q>"},
		{"a slash inside a word is not a path start", "linked a/b and c-d /abs/x", "", "linked a/b and c-d <path>"},
		{"a relative path that starts with a dot", "brief .soho/w6X/briefs-src/impl.md line 3 cites 'x'", "", "brief <path> line <n> cites <q>"},
		{"a dot and one separator is a path", "brief ./impl.md line 3", "", "brief <path> line <n>"},
		{"two separators make a relative path", "see internal/cli/friction.go here", "", "see <path> here"},
		{"a relative windows path", `brief .soho\w6X\impl.md line 3`, "", "brief <path> line <n>"},
		{"one separator without a dot is a word", "either and/or works", "", "either and/or works"},
		{"a number glued to a unit is masked", "took 45s to settle", "", "took <n>s to settle"},
		{"a number inside a word keeps its digits", "digest sha256 ok", "", "digest sha256 ok"},
		{"a ticket number inside a word keeps its digits", "card DJA-684 open", "", "card DJA-684 open"},
		{"the plural placeholder survives", "3 item(s) pending", "", "<n> item(s) pending"},
		{"control characters are removed before masking", "bad\x01\x02text 1", "", "badtext <n>"},
		{"the slug prefix names the key", "[rev-partial] reviewed the report 1", "rev-partial", "reviewed the report <n>"},
		{"a slug that does not match the rule is not a key", "[Bad-Key] some text 1", "", "[Bad-Key] some text <n>"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			key, pattern := frictionTemplate(c.message)
			wantKey := c.wantKey
			if wantKey == "" {
				wantKey = sha8Key(t, c.wantPattern)
			}
			if pattern != c.wantPattern {
				t.Fatalf("template:\n got:  %q\nwant: %q", pattern, c.wantPattern)
			}
			if key != wantKey {
				t.Fatalf("key:\n got:  %q\nwant: %q", key, wantKey)
			}
		})
	}
}

// Two messages that differ only by name, number and path must yield the same
// key (scenario 1).
func TestFrictionPatternSameKeyAcrossVariants(t *testing.T) {
	keyA, tplA := frictionTemplate("report of 'review-5' marks 3 item(s) partial: done")
	keyB, tplB := frictionTemplate("report of 'review-3' marks 1 item(s) partial: done")
	if keyA != keyB || tplA != tplB {
		t.Fatalf("variants must share key and template: %q/%q vs %q/%q", keyA, tplA, keyB, tplB)
	}
	if tplA != "report of <q> marks <n> item(s) partial: done" {
		t.Fatalf("template: %q", tplA)
	}
}

const frictionPatternsLog = "2026-09-30T10:00:00\twarning\twait\treport of 'review-5' marks 3 item(s) partial: tail\n" +
	"2026-09-30T11:00:00\twarning\twait\treport of 'review-5' marks 3 item(s) partial: tail\n" +
	"2026-09-30T12:00:00\tfixed\tfriction\tde06c5d6 the first fix\n" +
	"2026-09-30T12:30:00\twarning\tdispatch\treport of 'impl' marks 12 item(s) partial: tail\n" +
	"2026-10-01T09:00:00\tfixed\tfriction\tde06c5d6 the second fix\n" +
	"2026-10-01T09:30:00\twarning\twait\treport of 'review-5' marks 1 item(s) partial: tail\n" +
	"2026-10-01T10:00:00\tnote\tfriction\t[rev-note] reviewed the report\n"

func TestFrictionPatternsCommand(t *testing.T) {
	const patternHeader = "count  after  key  last_seen  fixed_by  pattern"
	const partialA = "report of <q> marks <n> item(s) partial: tail"
	t.Run("descending count, the last fixed wins, after counts only what came after", func(t *testing.T) {
		env, _ := frictionFilterFixture(t, frictionPatternsLog)
		code, out, stderr := runFriction(t, env, "--patterns")
		want := patternHeader + "\n" +
			"4  1  de06c5d6  2026-10-01T09:30:00  the second fix  " + partialA + "\n" +
			"1  -  rev-note  2026-10-01T10:00:00  -  reviewed the report\n"
		if code != 0 || stderr != "" || out != want {
			t.Fatalf("code=%d stderr=%q out=%q want:\n%s", code, stderr, out, want)
		}
	})
	t.Run("--json returns the same rows", func(t *testing.T) {
		env, _ := frictionFilterFixture(t, frictionPatternsLog)
		code, out, stderr := runFriction(t, env, "--patterns", "--json")
		if code != 0 || stderr != "" {
			t.Fatalf("code=%d stderr=%q out=%q", code, stderr, out)
		}
		var rows []map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rows); err != nil {
			t.Fatalf("json: %v out=%q", err, out)
		}
		if len(rows) != 2 {
			t.Fatalf("rows: %v", rows)
		}
		wantKeys := map[string]bool{"count": true, "after": true, "key": true, "last_seen": true, "fixed_by": true, "pattern": true}
		for k := range rows[0] {
			delete(wantKeys, k)
		}
		if len(wantKeys) != 0 {
			t.Fatalf("missing json keys: %v rows=%v", wantKeys, rows)
		}
		if rows[0]["count"] != float64(4) || rows[0]["after"] != "1" || rows[0]["key"] != "de06c5d6" ||
			rows[0]["last_seen"] != "2026-10-01T09:30:00" || rows[0]["fixed_by"] != "the second fix" || rows[0]["pattern"] != partialA {
			t.Fatalf("row 0: %v", rows[0])
		}
		if rows[1]["count"] != float64(1) || rows[1]["after"] != "-" || rows[1]["key"] != "rev-note" ||
			rows[1]["last_seen"] != "2026-10-01T10:00:00" || rows[1]["fixed_by"] != "-" || rows[1]["pattern"] != "reviewed the report" {
			t.Fatalf("row 1: %v", rows[1])
		}
	})
	t.Run("ties keep the file order", func(t *testing.T) {
		env, _ := frictionFilterFixture(t, "2026-09-30T10:00:00\tnote\tfriction\t[zzz] z text\n"+
			"2026-09-30T11:00:00\tnote\tfriction\t[aaa] a text\n")
		code, out, _ := runFriction(t, env, "--patterns")
		want := patternHeader + "\n" +
			"1  -  zzz  2026-09-30T10:00:00  -  z text\n" +
			"1  -  aaa  2026-09-30T11:00:00  -  a text\n"
		if code != 0 || out != want {
			t.Fatalf("code=%d out=%q want:\n%s", code, out, want)
		}
	})
	t.Run("--since filters before counting", func(t *testing.T) {
		env, _ := frictionFilterFixture(t, frictionPatternsLog)
		code, out, _ := runFriction(t, env, "--patterns", "--since", "2026-09-30T11:00:00")
		if code != 0 || !strings.Contains(out, "3  1  de06c5d6  2026-10-01T09:30:00  the second fix") {
			t.Fatalf("code=%d out=%q", code, out)
		}
		if !strings.Contains(out, "1  -  rev-note") {
			t.Fatalf("rev-note row missing: out=%q", out)
		}
	})
	t.Run("--command filters before counting", func(t *testing.T) {
		env, _ := frictionFilterFixture(t, frictionPatternsLog)
		code, out, _ := runFriction(t, env, "--patterns", "--command", "wait")
		if code != 0 || !strings.Contains(out, "3  1  de06c5d6") || strings.Contains(out, "rev-note") {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("malformed lines are ignored, like the separate summary count", func(t *testing.T) {
		env, _ := frictionFilterFixture(t, frictionFilterLog)
		code, out, stderr := runFriction(t, env, "--patterns")
		if code != 0 || stderr != "" {
			t.Fatalf("code=%d stderr=%q out=%q", code, stderr, out)
		}
		if strings.Contains(out, "\t?") || strings.Contains(out, " ? ") {
			t.Fatalf("malformed lines leaked into the patterns: out=%q", out)
		}
		quota := sha8Key(t, "quota: agent <q> lane=build")
		if !strings.Contains(out, "1  -  "+quota+"  2026-09-30T10:00:00  -  quota: agent <q> lane=build\n") {
			t.Fatalf("quota row missing: out=%q", out)
		}
	})
	t.Run("a missing log exits 0 with no lines and --json returns an empty list", func(t *testing.T) {
		env, _ := frictionFilterFixture(t, "")
		code, out, stderr := runFriction(t, env, "--patterns")
		if code != 0 || stderr != "" || out != "" {
			t.Fatalf("text: code=%d stderr=%q out=%q", code, stderr, out)
		}
		code, out, stderr = runFriction(t, env, "--patterns", "--json")
		if code != 0 || stderr != "" || out != "[]\n" {
			t.Fatalf("json: code=%d stderr=%q out=%q", code, stderr, out)
		}
	})
	t.Run("--patterns and --summary is a usage error", func(t *testing.T) {
		env, _ := frictionFilterFixture(t, frictionPatternsLog)
		code, out, stderr := runFriction(t, env, "--patterns", "--summary")
		if code != 2 || out != "" || !strings.Contains(stderr, "--patterns and --summary cannot be combined") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("control characters in the timestamp or in fixed_by never reach the output", func(t *testing.T) {
		env, _ := frictionFilterFixture(t, "2026-09-30T10:00:00\x1b[0m\twarning\twait\tmarked now 1\n"+
			"2026-09-30T10:00:01\tfixed\tfriction\t"+sha8Key(t, "marked now <n>")+" the \x1b[31mfix\n")
		code, out, stderr := runFriction(t, env, "--patterns")
		if code != 0 || stderr != "" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if strings.Contains(out, "\x1b") || !strings.Contains(out, "fix") {
			t.Fatalf("a control character reached the output: %q", out)
		}
		code, out, stderr = runFriction(t, env, "--patterns", "--json")
		if code != 0 || stderr != "" || strings.Contains(out, "\x1b") || strings.Contains(out, "\\u001b") {
			t.Fatalf("a control character reached the JSON: code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("control characters never reach the output", func(t *testing.T) {
		env, _ := frictionFilterFixture(t, "2026-09-30T10:00:00\twarning\twait\tmarked \x01\x02now 1\n")
		code, out, stderr := runFriction(t, env, "--patterns")
		if code != 0 || stderr != "" || strings.Contains(out, "\x01") {
			t.Fatalf("code=%d stderr=%q out=%q", code, stderr, out)
		}
		if !strings.Contains(out, "1  -  "+sha8Key(t, "marked now <n>")+"  2026-09-30T10:00:00  -  marked now <n>\n") {
			t.Fatalf("pattern row missing: out=%q", out)
		}
	})
	t.Run("two fixed lines in the same second keep the file order", func(t *testing.T) {
		env, _ := frictionFilterFixture(t, "2026-09-30T10:00:00\twarning\twait\treport of 'review-5' marks 3 item(s) partial: tail\n"+
			"2026-09-30T12:00:00\tfixed\tfriction\tde06c5d6 first text\n"+
			"2026-09-30T12:00:00\tfixed\tfriction\tde06c5d6 second text\n")
		code, out, _ := runFriction(t, env, "--patterns")
		if code != 0 || !strings.Contains(out, "1  0  de06c5d6  2026-09-30T10:00:00  second text  "+partialA+"\n") {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
}

func TestFrictionFixedCommand(t *testing.T) {
	t.Run("fixed records, after counts only what came after, the last fixed wins", func(t *testing.T) {
		env, logPath := frictionFilterFixture(t, "2026-09-30T10:00:00\twarning\twait\treport of 'review-5' marks 3 item(s) partial: tail\n"+
			"2026-09-30T11:00:00\twarning\twait\treport of 'review-5' marks 3 item(s) partial: tail\n")
		code, out, stderr := runFriction(t, env, "fixed", "de06c5d6", "the", "lint", "rule")
		if code != 0 || out != "recorded\n" || stderr != "" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		data, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		last := strings.SplitN(lines[len(lines)-1], "\t", 4)
		if len(last) != 4 || last[1] != "fixed" || last[2] != "friction" || last[3] != "de06c5d6 the lint rule" {
			t.Fatalf("fixed line: %q", lines[len(lines)-1])
		}
		if err := os.WriteFile(logPath, append([]byte(string(data)), []byte("2026-09-30T12:00:00\twarning\twait\treport of 'review-5' marks 1 item(s) partial: tail\n")...), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, _ = runFriction(t, env, "--patterns")
		if code != 0 || !strings.Contains(out, "3  1  de06c5d6  2026-09-30T12:00:00  the lint rule") {
			t.Fatalf("after first fixed: code=%d out=%q", code, out)
		}
		code, out, stderr = runFriction(t, env, "fixed", "de06c5d6", "the", "strict", "rule")
		if code != 0 || out != "recorded\n" || stderr != "" {
			t.Fatalf("second fixed: code=%d out=%q stderr=%q", code, out, stderr)
		}
		data, _ = os.ReadFile(logPath)
		if strings.Count(string(data), "\tfixed\tfriction\tde06c5d6") != 2 {
			t.Fatalf("the earlier fixed line must stay in the log: %q", data)
		}
		code, out, _ = runFriction(t, env, "--patterns")
		if code != 0 || !strings.Contains(out, "3  0  de06c5d6  2026-09-30T12:00:00  the strict rule") {
			t.Fatalf("after second fixed: code=%d out=%q", code, out)
		}
	})
	t.Run("fixed with an unknown key exits 2 and writes nothing", func(t *testing.T) {
		env, logPath := frictionFilterFixture(t, frictionPatternsLog)
		code, out, stderr := runFriction(t, env, "fixed", "nosuch", "the", "fix")
		if code != 2 || out != "" || !strings.Contains(stderr, "no friction pattern with key nosuch") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		data, _ := os.ReadFile(logPath)
		if strings.Contains(string(data), "\tfixed\tfriction\tnosuch ") {
			t.Fatalf("the fixed line must not be written: %q", data)
		}
	})
	t.Run("fixed with an empty text exits 2 and writes nothing", func(t *testing.T) {
		env, logPath := frictionFilterFixture(t, frictionPatternsLog)
		code, out, stderr := runFriction(t, env, "fixed", "de06c5d6")
		if code != 2 || out != "" || stderr == "" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		data, _ := os.ReadFile(logPath)
		if strings.Count(string(data), "\tfixed\tfriction\tde06c5d6") != 2 {
			t.Fatalf("the log must keep only the two fixture fixed lines: %q", data)
		}
	})
	t.Run("fixed with no key exits 2", func(t *testing.T) {
		env, logPath := frictionFilterFixture(t, frictionPatternsLog)
		code, out, stderr := runFriction(t, env, "fixed")
		if code != 2 || out != "" || stderr == "" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		data, _ := os.ReadFile(logPath)
		if strings.Count(string(data), "\tfixed\tfriction	") != 2 {
			t.Fatalf("the log must keep only the two fixture fixed lines: %q", data)
		}
	})
	t.Run("fixed a slug key written by add works", func(t *testing.T) {
		env, logPath := frictionFilterFixture(t, frictionPatternsLog)
		code, out, stderr := runFriction(t, env, "fixed", "rev-note", "the", "rule")
		if code != 0 || out != "recorded\n" || stderr != "" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		data, _ := os.ReadFile(logPath)
		if !strings.Contains(string(data), "\tfixed\tfriction\trev-note the rule\n") {
			t.Fatalf("fixed line missing: %q", data)
		}
	})
	t.Run("fixed on a missing log exits 2", func(t *testing.T) {
		env, _ := frictionFilterFixture(t, "")
		code, out, stderr := runFriction(t, env, "fixed", "rev-note", "x")
		if code != 2 || out != "" || !strings.Contains(stderr, "no friction pattern with key rev-note") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
}

// frictionRecordedLines counts what a command recorded on purpose: a refusal
// also adds its error(exit 2) line, as every refused command does.
func frictionRecordedLines(t *testing.T, logPath string) int {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "\tnote\t") + strings.Count(string(data), "\tfixed\t")
}

func TestFrictionKeyPresence(t *testing.T) {
	line := "2026-09-30T10:00:00\twarning\twait\tmarked now 1\n"
	key := sha8Key(t, "marked now <n>")
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"add with an empty key", []string{"add", "a", "note", "--key", ""}},
		{"fixed with a key option", []string{"fixed", key, "the fix", "--key", "ok-key"}},
		{"fixed with an empty key option", []string{"fixed", key, "the fix", "--key", ""}},
		{"a read with a key option", []string{"--patterns", "--key", "ok-key"}},
		{"a plain read with an empty key option", []string{"--key", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, logPath := frictionFilterFixture(t, line)
			before := frictionRecordedLines(t, logPath)
			code, out, _ := runFriction(t, env, tc.args...)
			if code != 2 {
				t.Fatalf("code=%d out=%q, want 2", code, out)
			}
			if after := frictionRecordedLines(t, logPath); after != before {
				t.Fatalf("recorded lines went from %d to %d", before, after)
			}
		})
	}
}

func TestFrictionFixedNamesOnlyAnOccurrence(t *testing.T) {
	t.Run("a bare bracketed note names no slug key", func(t *testing.T) {
		env, logPath := frictionFilterFixture(t, "2026-09-30T10:00:00\tnote\tfriction\t[foo]\n")
		before := frictionRecordedLines(t, logPath)
		code, _, _ := runFriction(t, env, "fixed", "foo", "the fix")
		if after := frictionRecordedLines(t, logPath); code != 2 || after != before {
			t.Fatalf("code=%d recorded lines %d to %d", code, before, after)
		}
	})
	t.Run("a key computed only from a fixed line names nothing", func(t *testing.T) {
		metadata := "deadbeef the old fix"
		env, logPath := frictionFilterFixture(t, "2026-09-30T10:00:00\tfixed\tfriction\t"+metadata+"\n")
		before := frictionRecordedLines(t, logPath)
		key := sha8Key(t, metadata)
		code, _, _ := runFriction(t, env, "fixed", key, "the fix")
		if after := frictionRecordedLines(t, logPath); code != 2 || after != before {
			t.Fatalf("key %s: code=%d recorded lines %d to %d", key, code, before, after)
		}
	})
	t.Run("a slug and a computed key from occurrences still work", func(t *testing.T) {
		env, _ := frictionFilterFixture(t, "2026-09-30T10:00:00\tnote\tfriction\t[foo] seen once\n2026-09-30T10:00:01\twarning\twait\tmarked now 1\n")
		if code, out, _ := runFriction(t, env, "fixed", "foo", "the fix"); code != 0 {
			t.Fatalf("slug: code=%d out=%q", code, out)
		}
		if code, out, _ := runFriction(t, env, "fixed", sha8Key(t, "marked now <n>"), "the fix"); code != 0 {
			t.Fatalf("computed: code=%d out=%q", code, out)
		}
	})
}

func TestFrictionAddKeyCommand(t *testing.T) {
	t.Run("add with a valid key writes the slug prefix", func(t *testing.T) {
		env, logPath := frictionFilterFixture(t, "")
		code, out, stderr := runFriction(t, env, "add", "a", "note", "--key", "ok-key")
		if code != 0 || out != "recorded\n" || stderr != "" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		data, _ := os.ReadFile(logPath)
		lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		last := strings.SplitN(lines[len(lines)-1], "\t", 4)
		if len(last) != 4 || last[1] != "note" || last[2] != "friction" || last[3] != "[ok-key] a note" {
			t.Fatalf("add line: %q", lines[len(lines)-1])
		}
		code, out, _ = runFriction(t, env, "--patterns")
		if code != 0 || !strings.Contains(out, "1  -  ok-key  "+last[0]+"  -  a note\n") {
			t.Fatalf("pattern of the add line: code=%d out=%q", code, out)
		}
	})
	t.Run("add without a key is unchanged", func(t *testing.T) {
		env, logPath := frictionFilterFixture(t, "")
		code, out, _ := runFriction(t, env, "add", "plain")
		if code != 0 || out != "recorded\n" {
			t.Fatalf("code=%d out=%q", code, out)
		}
		data, _ := os.ReadFile(logPath)
		if !strings.Contains(string(data), "\tfriction\tplain\n") {
			t.Fatalf("add line: %q", data)
		}
	})
	for _, bad := range []string{"BAD", "-lead", "_x", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaabb"} {
		t.Run("add with an invalid key exits 2 and writes nothing: "+bad, func(t *testing.T) {
			env, logPath := frictionFilterFixture(t, "")
			code, out, stderr := runFriction(t, env, "add", "a", "note", "--key", bad)
			if code != 2 || out != "" || stderr == "" {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
			data, _ := os.ReadFile(logPath)
			if strings.Contains(string(data), "] a note") {
				t.Fatalf("the note must not be written: %q", data)
			}
		})
	}
	t.Run("--key with no value exits 2", func(t *testing.T) {
		env, _ := frictionFilterFixture(t, "")
		code, out, stderr := runFriction(t, env, "add", "a", "note", "--key")
		if code != 2 || out != "" || !strings.Contains(stderr, "--key expects a value") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
}
