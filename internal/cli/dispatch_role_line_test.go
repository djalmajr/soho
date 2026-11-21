package cli

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/djalmajr/soho/internal/platform"
)

// SOH-472: a no-commit line left inside Owned files still counts as an edit
// request, but the refusal now names the exact line and tells the reader
// where that phrase belongs. The same line under Report is not a signal.
func TestDispatchReadOnlyRefusalNamesOwnedLine(t *testing.T) {
	const head = "Type: review\n## Owned files\n\n- None.\n\n"
	const tail = "\n## Report\nDone.\n"
	f := roleDispatchFixture(t)
	writeRoleFixture(t, f.brief, head+"Sem commit."+tail)
	code, out, msg := f.run(t, "worker", f.brief, "--role", "validator", "--no-wait", "--compact")
	want := "asks for edits (Owned files: line 6: 'Sem commit.'; that line belongs in Forbidden or Report, not in the file list) but 'worker' was given the read-only role 'validator'"
	if code != 2 || !strings.Contains(msg, want) {
		t.Fatalf("reported brief: code=%d out=%s msg=%q want=%q", code, out, msg, want)
	}
	assertRoleNoSend(t, f)

	writeRoleFixture(t, f.brief, "Type: review\n## Owned files\n\n- None.\n\n\n## Report\nSem commit.\nDone.\n")
	code, out, msg = f.run(t, "worker", f.brief, "--role", "validator", "--no-wait")
	if code != 0 || strings.Contains(msg, "asks for edits") {
		t.Fatalf("closing line moved under Report: code=%d out=%s msg=%q", code, out, msg)
	}
}

// SOH-472 F1: the two consumers show the same signal for the fixed classes.
func TestReadOnlyClosingPhraseConsumers(t *testing.T) {
	const hint = "; that line belongs in Forbidden or Report, not in the file list"
	re := regexp.MustCompile(`asks for edits \((.*?)\) but`)
	for _, tc := range []struct {
		name, item string
		hint       bool
	}{
		{name: "backticked-path", item: "- `No commit`"},
		{name: "numbered-period", item: "1. Sem commit.", hint: true},
		{name: "numbered-paren", item: "2) do not commit", hint: true},
		{name: "curly-quote", item: "- “No commit.”", hint: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := roleDispatchFixture(t)
			writeRoleFixture(t, f.brief, "Type: review\n## Owned files\n"+tc.item+"\n")
			drc, _, derr := f.run(t, "worker", f.brief, "--role", "validator", "--no-wait")
			oldOut, oldErr := platform.Stdout, platform.Stderr
			var out, stderr bytes.Buffer
			lrc := func() int {
				platform.Stdout, platform.Stderr = &out, &stderr
				defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
				return Run([]string{"lint", f.brief, "--role", "validator"}, f.env)
			}()
			d, l := re.FindStringSubmatch(derr), re.FindStringSubmatch(stderr.String())
			if drc != 2 || lrc != 2 || len(d) != 2 || len(l) != 2 || d[1] != l[1] {
				t.Fatalf("dispatch=%d %q lint=%d %q out=%q", drc, d, lrc, l, out.String())
			}
			if !strings.Contains(d[1], "line 3:") || strings.Contains(d[1], hint) != tc.hint {
				t.Fatalf("signal=%q hint=%t want %t", d[1], strings.Contains(d[1], hint), tc.hint)
			}
			assertRoleNoSend(t, f)
		})
	}
}

func TestReadOnlyEditSignalsOwnedLineDetail(t *testing.T) {
	for _, tc := range []struct {
		name, text, want string
		closing          bool
	}{
		{name: "reported", text: "Sem commit.", want: "Owned files: line 2: 'Sem commit.'", closing: true},
		{name: "path", text: "- src/app.go", want: "Owned files: line 2: '- src/app.go'"},
		// QuoteControls keeps printable text, accents included.
		{name: "accent-insensitive", text: "- **NÃO COMMITE**", want: "Owned files: line 2: '- **NÃO COMMITE**'", closing: true},
		{name: "unaccented-nao-commite", text: "- nao commite", want: "Owned files: line 2: '- nao commite'", closing: true},
		{name: "no-commit", text: "No commit", want: "Owned files: line 2: 'No commit'", closing: true},
		{name: "do-not-commit", text: "Do not commit", want: "Owned files: line 2: 'Do not commit'", closing: true},
		{name: "dont-commit", text: "- don't commit", want: "Owned files: line 2: '- don't commit'", closing: true},
		{name: "never-commit", text: "+ Never commit", want: "Owned files: line 2: '+ Never commit'", closing: true},
		{name: "not-a-phrase", text: "No commit/push.", want: "Owned files: line 2: 'No commit/push.'"},
		// SOH-472 F1: a backtick span marks a path item, whatever prose it
		// happens to contain; numbered markers and outer quotes are prose.
		{name: "backticked-root-path", text: "- `No commit`", want: "Owned files: line 2: '- `No commit`'"},
		{name: "backticked-punctuated-path", text: "- `Never commit!`", want: "Owned files: line 2: '- `Never commit!`'"},
		{name: "numbered-path", text: "1. `No commit`", want: "Owned files: line 2: '1. `No commit`'"},
		{name: "backtick-plus-phrase", text: "- `src/x` Sem commit.", want: "Owned files: line 2: '- `src/x` Sem commit.'"},
		{name: "numbered-period", text: "1. Sem commit.", want: "Owned files: line 2: '1. Sem commit.'", closing: true},
		{name: "numbered-paren", text: "2) do not commit", want: "Owned files: line 2: '2) do not commit'", closing: true},
		{name: "numbered-emphasis", text: "3. **NÃO COMMITE.**", want: "Owned files: line 2: '3. **NÃO COMMITE.**'", closing: true},
		{name: "curly-double-quote", text: "- “No commit.”", want: "Owned files: line 2: '- “No commit.”'", closing: true},
		{name: "curly-single-quote", text: "‘Never commit!’", want: "Owned files: line 2: '‘Never commit!’'", closing: true},
		{name: "guillemets", text: "«Do not commit»", want: "Owned files: line 2: '«Do not commit»'", closing: true},
		{name: "ascii-double-quote", text: "\"No commit\"", want: "Owned files: line 2: '\"No commit\"'", closing: true},
		{name: "parenthesized", text: "(Never commit!)", want: "Owned files: line 2: '(Never commit!)'", closing: true},
		{name: "shouted", text: "- SEM COMMIT!", want: "Owned files: line 2: '- SEM COMMIT!'", closing: true},
		{name: "semicolon-and-colon", text: "- No commit;", want: "Owned files: line 2: '- No commit;'", closing: true},
		{name: "curly-apostrophe-not-phrase", text: "- Don’t commit.", want: "Owned files: line 2: '- Don’t commit.'"},
		// Unpaired decoration stays, so these are not closing phrases.
		{name: "unpaired-paren", text: "No commit)", want: "Owned files: line 2: 'No commit)'"},
		{name: "unpaired-emphasis", text: "**No commit", want: "Owned files: line 2: '**No commit'"},
		{name: "control-escaped", text: "- a\tb", want: "Owned files: line 2: '- a\\tb'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			signals, suggested, remedy := readOnlyEditSignals("## Owned files\n" + tc.text + "\n")
			want := tc.want
			if tc.closing {
				want += "; that line belongs in Forbidden or Report, not in the file list"
			}
			if len(signals) != 1 || signals[0] != want || suggested != "implementer" || remedy != "or rename the 'Owned files' title, or drop the edit sections from the brief" {
				t.Fatalf("signals=%q suggested=%q remedy=%q; want signal %q", signals, suggested, remedy, want)
			}
		})
	}
	// The first counted item wins, even when a real path follows it.
	signals, _, _ := readOnlyEditSignals("## Owned files\nSem commit.\n- src/app.go\n")
	if len(signals) != 1 || !strings.HasPrefix(signals[0], "Owned files: line 2: 'Sem commit.'") {
		t.Fatalf("first counted item was not reported: %q", signals)
	}
	// A Type line alone keeps the earlier text, including the remedy.
	signals, suggested, remedy := readOnlyEditSignals("Type: backend\n## Owned files\nNone.\n## Report\nDone.\n")
	if len(signals) != 1 || signals[0] != "Type: backend" || suggested != "implementer" || remedy != "or drop the 'Type: backend' line and the edit sections from the brief" {
		t.Fatalf("Type-only signals=%q suggested=%q remedy=%q", signals, suggested, remedy)
	}
	// Hostile item text is escaped and cut, with the ellipsis marking the cut.
	long := strings.Repeat("a", 120)
	signals, _, _ = readOnlyEditSignals("## Owned files\n- " + long + "\x1b[31m\n")
	if len(signals) != 1 || !strings.HasPrefix(signals[0], "Owned files: line 2: '- "+strings.Repeat("a", 78)+"…'") || strings.Contains(signals[0], "\x1b") {
		t.Fatalf("hostile text not escaped and cut: %q", signals)
	}
	// The cut counts characters: a multibyte character at the boundary
	// stays whole (78 ASCII after "- ", then é as the 80th character).
	signals, _, _ = readOnlyEditSignals("## Owned files\n- " + strings.Repeat("b", 77) + "éé" + strings.Repeat("c", 10) + "\n")
	if len(signals) != 1 || !strings.HasPrefix(signals[0], "Owned files: line 2: '- "+strings.Repeat("b", 77)+"é…'") || !utf8.ValidString(signals[0]) {
		t.Fatalf("multibyte boundary not kept whole: %q", signals)
	}
}
