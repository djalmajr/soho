package provider

// The shared composer input-region rules (ComposerRegion) and the known
// dialog UI classifier (DialogUI). The screens pin the rules on the real
// provider/peer captures: the rc.11 codex visible screen (peer testdata),
// the rc.14 claude visible screen (the real end screen with the empty box)
// and pi's input box. An unrecognized region or an unknown screen must
// stay not-positive-proof: the caller keeps the delivery uncertain.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// piBoxSeparator is pi's input-box border line (all '─').
const piBoxSeparator = "──────────────────────────────────────────────────────────"

// claudeBoxSeparator is Claude Code's input-box border line (all '─').
const claudeBoxSeparator = "──────────────────────────────────────────────────────────────────────────"

func TestComposerIndentedWrappedPath(t *testing.T) {
	for _, directory := range []string{`C:\Users\worker\briefs\`, `/tmp/worker/workspace/briefs/`} {
		width := 16 + len(directory)
		name := "implementer-" + strings.Repeat("x", width-2-len("implementer-")-1) + "-"
		path := directory + name + "20261023T181332.brief.md"
		t.Run(path, func(t *testing.T) {
			wrapped := "› Read the file " + directory + "\n  " + name + "\n  20261023T181332.brief.md in full."
			for _, tc := range []struct {
				name, screen string
				want         bool
			}{
				{"exact", wrapped + "\n\nfooter", true},
				{"rejected", strings.Replace(wrapped, ".md in full.", ".md.rejected in full.", 1), false},
				{"history", strings.TrimPrefix(wrapped, "› ") + "\n\n› unrelated draft\n\nfooter", false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					lines, ok := ComposerRegion("codex", tc.screen)
					if !ok || ComposerHoldsPathFor("codex", lines, path) != tc.want {
						t.Fatalf("region=%q recognized=%v want holds=%v path=%q", lines, ok, tc.want, path)
					}
				})
			}
		})
	}
	t.Run("suffix-on-next-line", func(t *testing.T) {
		path := "/tmp/brief.md"
		first := "› Read " + path
		screen := first + "\n  .rejected\n\n" + strings.Repeat("-", len([]rune(first)))
		lines, ok := ComposerRegion("codex", screen)
		if !ok || ComposerHoldsPathFor("codex", lines, path) || !ComposerHoldsPathFor("codex", lines, path+".rejected") {
			t.Fatalf("region=%q recognized=%v path=%q", lines, ok, path)
		}
	})
	t.Run("claude-literal-continuation", func(t *testing.T) {
		path := "/tmp/briefs/worker.md"
		screen := claudeBoxSeparator + "\n❯ Read /tmp/briefs/\n│ worker.md in full.\n" + claudeBoxSeparator
		lines, ok := ComposerRegion("claude", screen)
		if !ok || ComposerHoldsPathFor("claude", lines, path) {
			t.Fatalf("region=%q recognized=%v path=%q", lines, ok, path)
		}
	})
}

func TestComposerSoftWrapSpaces(t *testing.T) {
	for _, tc := range []struct {
		name, screen, path string
		want               bool
	}{
		{"real-space", "› Read C:\\Users\\Some \n  Name\\brief.md in full.", `C:\Users\Some Name\brief.md`, true},
		{"missing-space", "› Read C:\\Users\\Some \n  Name\\brief.md in full.", `C:\Users\SomeName\brief.md`, false},
		{"prose", "› Read /tmp/brief.md \n  in full and execute it.", "/tmp/brief.md", true},
		{"extra-indent-is-text", "  › Read C:\\Users\\Some\n     Name\\brief.md in full.", `C:\Users\SomeName\brief.md`, false},
		{"extra-indent-preserved", "  › Read C:\\Users\\Some\n     Name\\brief.md in full.", `C:\Users\Some  Name\brief.md`, true},
		{"unicode-footer", "› Read /tmp/worker-\n  brief.md", "/tmp/worker-brief.md", true},
		{"mixed-breaks", "› Read /tmp/Some\n  Name/worker-\n  brief.md", "/tmp/Some Name/worker-brief.md", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			footer := strings.Repeat("-", 60) + "\n" + strings.Repeat("-", 60)
			if tc.name == "unicode-footer" {
				footer = "界"
			}
			lines, ok := ComposerRegion("codex", tc.screen+"\n\n"+footer)
			if !ok || ComposerHoldsPathFor("codex", lines, tc.path) != tc.want {
				t.Fatalf("screen=%q region=%q recognized=%v path=%q want=%v", tc.screen, lines, ok, tc.path, tc.want)
			}
		})
	}
}

// codexRealWrapScreen mirrors the rc12 history, activity indicator and two-line
// footer. Width is only a fixture layout choice, not path-matching evidence.
func codexRealWrapScreen(composer string, width int) string {
	warnings := "⚠ 2 warnings · f2 to view"
	history := "• Ran a command …"
	return history +
		"\n  └ output\n\n  New activity · ↓ Back to bottom · esc\n\n" + composer +
		"\n\n  GPT-6.1-Sol high · Context 20% · ← for agents\n" +
		strings.Repeat(" ", width-len([]rune(warnings))) + warnings
}

func TestComposerRealCodexWrap(t *testing.T) {
	const width = 96
	directory := `C:\Users\worker\briefs\` + strings.Repeat("x", width-16-len(`C:\Users\worker\briefs\`))
	name := strings.Repeat("y", width-3) + "-"
	path := directory + name + "20261023.brief.md"
	wrapped := "› Read the file " + directory + "\n  " + name + "\n  20261023.brief.md in full."
	soft := "› Read C:\\Users\\Some \n  Name\\brief.md in full."
	for _, tc := range []struct {
		name, screen, path string
		want               bool
	}{
		{"windows-three-lines", codexRealWrapScreen(wrapped, width), path, true},
		{"real-space", codexRealWrapScreen(soft, width), `C:\Users\Some Name\brief.md`, true},
		{"missing-space", codexRealWrapScreen(soft, width), `C:\Users\SomeName\brief.md`, false},
		{"single-widest-line", "› Read C:\\Users\\Some\n  Name\\b.md\n\n·", `C:\Users\SomeName\b.md`, true},
		{"cjk-history", "界\n" + codexRealWrapScreen(wrapped, width), path, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			region, ok := ComposerRegion("codex", tc.screen)
			if !ok || ComposerHoldsPathFor("codex", region, tc.path) != tc.want {
				t.Fatalf("screen=%q region=%q recognized=%v path=%q want=%v", tc.screen, region, ok, tc.path, tc.want)
			}
		})
	}
}

func TestComposerAcceptedWrapAmbiguity(t *testing.T) {
	for _, path := range []string{"/tmp/SomeName/brief.md", "/tmp/Some Name/brief.md"} {
		for _, prefix := range []string{"› Read ", "› \u1160 Read "} {
			screen := prefix + "/tmp/Some\n  Name/brief.md\n\n----------------\n----------------"
			region, ok := ComposerRegion("codex", screen)
			if !ok || !ComposerHoldsPathFor("codex", region, path) {
				t.Fatalf("accepted ambiguity changed: screen=%q region=%q recognized=%v path=%q", screen, region, ok, path)
			}
		}
	}
}

func TestReviewR4PiIsolation(t *testing.T) {
	for _, tc := range []struct {
		name, kind, body, path string
	}{
		{"pi-literal-prompt", "pi", "› Read /tmp/Some\n  Name/brief.md in full.", "/tmp/SomeName/brief.md"},
		{"pi-literal-prompt-spaced-path", "pi", "› Read /tmp/Some\n  Name/brief.md in full.", "/tmp/Some Name/brief.md"},
		{"pi-normal", "pi", "Read /tmp/Some\n  Name/brief.md in full.", "/tmp/SomeName/brief.md"},
		{"claude-guard", "claude", "❯ › Read /tmp/Some\n  Name/brief.md in full.", "/tmp/SomeName/brief.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			screen := piBoxSeparator + "\n" + tc.body + "\n" + piBoxSeparator
			region, ok := ComposerRegion(tc.kind, screen)
			baseline := ComposerHoldsPath(region, tc.path)
			got := ComposerHoldsPathFor(tc.kind, region, tc.path)
			if !ok || baseline || got != baseline {
				t.Fatalf("kind=%s screen=%q region=%q recognized=%v baseline=%v current=%v path=%q", tc.kind, screen, region, ok, baseline, got, tc.path)
			}
		})
	}
}

func TestComposerKindIsolation(t *testing.T) {
	lines := []string{"› Read /tmp/Some", "  Name/brief.md"}
	for _, kind := range []string{"codex", "pi", "claude", "opencode", "", "unknown"} {
		for _, path := range []string{"/tmp/SomeName/brief.md", "/tmp/Some Name/brief.md"} {
			if got := ComposerHoldsPathFor(kind, lines, path); got != (kind == "codex") {
				t.Fatalf("kind=%q path=%q held=%v", kind, path, got)
			}
		}
		// Every kind retains the original joins when no layout removal is needed.
		plain := []string{"Read /tmp/Some", "Name/brief.md"}
		if !ComposerHoldsPathFor(kind, plain, "/tmp/SomeName/brief.md") {
			t.Fatalf("kind=%q lost the original empty join", kind)
		}
	}
	if ComposerHoldsPath(lines, "/tmp/SomeName/brief.md") {
		t.Fatal("the kind-free predicate must keep literal input unchanged")
	}
}

func TestComposerWrapLimit(t *testing.T) {
	// Eight input lines exhaust recognition's eight non-empty-line window.
	// Test the path predicate's fallback directly as well for larger regions.
	for _, n := range []int{8, 9} {
		lines := []string{"› Read /tmp/"}
		for i := 1; i < n; i++ {
			lines = append(lines, "  x")
		}
		path := "/tmp/" + strings.Repeat("x", n-1)
		if got := ComposerHoldsPathFor("codex", lines, path); got != (n == 8) {
			t.Fatalf("lines=%d path=%q held=%v", n, path, got)
		}
		if n == 9 && !ComposerHoldsPathFor("codex", lines, strings.Join(lines, "")) {
			t.Fatal("over-limit input lost the original empty-join behavior")
		}
	}
}

// rc14ClaudeScreen is the real rc.14 screen (the end), verbatim: the taken
// message's end line in the history, the empty box (a bare ❯) between the
// two '─' border lines, and the footer below.
const rc14ClaudeScreen = `  > Não precisa responder.
  [herdr-soho:peer] #debf6313 end of message
  ⎿  1 skill available
✻ Fluttering… (9s · ↓ 135 tokens)
  ⎿  Tip: Try the new fullscreen renderer — flicker-free output, mouse
     support, auto-copy on select · /tui fullscreen
──────────────────────────────────────────────────────────────────────────
❯
──────────────────────────────────────────────────────────────────────────
  [Opus 5.5] ██████░░░░ 67% [5h:8% 2h56m] [7d:54% 3d10h] [main*]
  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents · 1 feed…
                                  ✔ Update installed · Restart to update
`

// codexEvidenceScreen loads the real codex visible screen captured a few
// seconds after a send to a working codex (the peer testdata copy): the
// taken message sits in the history, above the composer.
func codexEvidenceScreen(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "peer", "testdata", "evidence-rc11-codex-history.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestComposerRegion(t *testing.T) {
	path := "/tmp/herdr-soho/ws/briefs/worker-20261003T152648.md"
	t.Run("the real codex screen excludes the separated status footer", func(t *testing.T) {
		lines, ok := ComposerRegion("codex", codexEvidenceScreen(t))
		if !ok {
			t.Fatal("the real screen's '› Ask Codex to do anything' line must form the region")
		}
		if lines[0] != "› Ask Codex to do anything" {
			t.Fatalf("region starts at %q", lines[0])
		}
		if len(lines) != 1 {
			t.Fatalf("the blank separator must exclude the status footer: %+v", lines)
		}
	})
	t.Run("a wrapped codex path stays in the input before the footer", func(t *testing.T) {
		screen := "› Read the file " + path[:len(path)-13] + "\n" + path[len(path)-13:] + " in full and execute it.\n\n  GPT-6.1-Sol medium · Context 20%\n"
		lines, ok := ComposerRegion("codex", screen)
		if !ok || len(lines) != 2 || !ComposerHoldsPathFor("codex", lines, path) {
			t.Fatalf("wrapped input was lost: region=%q ok=%v", lines, ok)
		}
	})
	t.Run("the typed path inside the codex composer is in the region", func(t *testing.T) {
		screen := "history line\n› Read the file " + path + " in full and execute it.\n\n  GPT-6.1-Sol medium · Context 20%\n"
		lines, ok := ComposerRegion("codex", screen)
		if !ok {
			t.Fatalf("the '›' composer line must form the region:\n%s", screen)
		}
		joined := strings.Join(lines, "")
		if !strings.Contains(joined, path) {
			t.Fatalf("the region must hold the typed path: %+v", lines)
		}
	})
	t.Run("the path in the history above the empty codex composer is not in the region", func(t *testing.T) {
		screen := "history line\nRead the file " + path + " in full and execute it.\n• done\n\n› Ask Codex to do anything\n\n  GPT-6.1-Sol medium · Context 20%\n"
		lines, ok := ComposerRegion("codex", screen)
		if !ok {
			t.Fatalf("the '›' composer line must form the region:\n%s", screen)
		}
		if strings.Contains(strings.Join(lines, ""), path) || strings.Contains(strings.Join(lines, " "), path) {
			t.Fatalf("the region above the empty composer must not hold the history path: %+v", lines)
		}
	})
	t.Run("an indented codex composer line counts", func(t *testing.T) {
		lines, ok := ComposerRegion("codex", "history\n  › Ask Codex to do anything\nfooter\n")
		if !ok || len(lines) != 2 || lines[0] != "  › Ask Codex to do anything" {
			t.Fatalf("region=%+v ok=%v", lines, ok)
		}
	})
	t.Run("a bare '›' line counts", func(t *testing.T) {
		if _, ok := ComposerRegion("codex", "history\n›\nfooter\n"); !ok {
			t.Fatal("a bare '›' line must form the region")
		}
	})
	t.Run("the last composer line inside the window wins", func(t *testing.T) {
		lines, ok := ComposerRegion("codex", "history\n› older prompt\n› newer prompt\nfooter\n")
		if !ok || lines[0] != "› newer prompt" {
			t.Fatalf("region=%+v ok=%v", lines, ok)
		}
	})
	t.Run("the eighth non-empty line from the end still counts", func(t *testing.T) {
		if _, ok := ComposerRegion("codex", "› Ask Codex to do anything\n"+strings.Repeat("filler\n", 7)); !ok {
			t.Fatal("a '›' line that is the 8th non-empty line from the end must form the region")
		}
	})
	t.Run("a composer line beyond the last 8 non-empty lines does not count", func(t *testing.T) {
		if _, ok := ComposerRegion("codex", "› Ask Codex to do anything\n"+strings.Repeat("filler\n", 8)); ok {
			t.Fatal("a '›' line below the last 8 non-empty lines must not form the region")
		}
	})
	t.Run("'›' without the space is not a composer line", func(t *testing.T) {
		if _, ok := ComposerRegion("codex", "history\n›x\nfooter\n"); ok {
			t.Fatal("'›x' must not form the region")
		}
	})
	t.Run("the real codex screen without the composer line has no region", func(t *testing.T) {
		lines := strings.Split(codexEvidenceScreen(t), "\n")
		for i, line := range lines {
			if strings.HasPrefix(strings.TrimLeft(line, " \t"), "›") {
				lines[i] = "empty"
			}
		}
		if _, ok := ComposerRegion("codex", strings.Join(lines, "\n")); ok {
			t.Fatal("without a '›' line the region must not form")
		}
	})
	t.Run("CRLF codex screen", func(t *testing.T) {
		lines, ok := ComposerRegion("codex", "history\r\n› Ask Codex to do anything\r\nfooter\r\n")
		if !ok || len(lines) != 2 || lines[0] != "› Ask Codex to do anything" {
			t.Fatalf("region=%+v ok=%v", lines, ok)
		}
	})
	t.Run("the real rc.14 claude screen: the empty box between the borders", func(t *testing.T) {
		lines, ok := ComposerRegion("claude", rc14ClaudeScreen)
		if !ok {
			t.Fatal("the real screen's '─' pair must form the claude region")
		}
		if len(lines) != 1 || strings.TrimSpace(lines[0]) != "❯" {
			t.Fatalf("the empty box holds only the ❯ line: %+v", lines)
		}
	})
	t.Run("the typed path inside the claude box is in the region", func(t *testing.T) {
		screen := "history line\n" + claudeBoxSeparator + "\n❯ Read the file " + path + " in full and execute it.\n" + claudeBoxSeparator + "\n  [Opus 5.5] 67% [main*]\n"
		lines, ok := ComposerRegion("claude", screen)
		if !ok {
			t.Fatalf("the claude box must form the region:\n%s", screen)
		}
		joined := strings.Join(lines, "")
		if !strings.Contains(joined, path) {
			t.Fatalf("the region must hold the typed path: %+v", lines)
		}
	})
	t.Run("the path in the history above the claude box is not in the region", func(t *testing.T) {
		screen := "Read the file " + path + " in full and execute it.\n✻ Fluttering… (9s · ↓ 135 tokens)\n" + claudeBoxSeparator + "\n❯\n" + claudeBoxSeparator + "\n  [Opus 5.5] 67% [main*]\n"
		lines, ok := ComposerRegion("claude", screen)
		if !ok {
			t.Fatalf("the claude box must form the region:\n%s", screen)
		}
		if strings.Contains(strings.Join(lines, ""), path) {
			t.Fatalf("the region must not hold the history path: %+v", lines)
		}
	})
	t.Run("a '─' pair without a ❯ inside is not the box", func(t *testing.T) {
		screen := "history\n" + claudeBoxSeparator + "\ntyped draft\n" + claudeBoxSeparator + "\nfooter\n"
		if _, ok := ComposerRegion("claude", screen); ok {
			t.Fatal("a border pair that fails the composer check must not form the region")
		}
	})
	t.Run("a ❯ below the bottom border puts the composer under the pair", func(t *testing.T) {
		screen := "history\n" + claudeBoxSeparator + "\n❯\n" + claudeBoxSeparator + "\n❯ draft under the pair\nfooter\n"
		if _, ok := ComposerRegion("claude", screen); ok {
			t.Fatal("a ❯ below the bottom border must not form the region")
		}
	})
	t.Run("borders beyond the last 12 non-empty lines are not box borders", func(t *testing.T) {
		screen := claudeBoxSeparator + "\n❯\n" + claudeBoxSeparator + "\n" + strings.Repeat("filler\n", 12)
		if _, ok := ComposerRegion("claude", screen); ok {
			t.Fatal("a border pair higher than the last 12 non-empty lines must not form the region")
		}
	})
	t.Run("one border line forms no region", func(t *testing.T) {
		if _, ok := ComposerRegion("claude", "history\n"+claudeBoxSeparator+"\n❯\nfooter\n"); ok {
			t.Fatal("one border line must not form the region")
		}
	})
	t.Run("the pi input box between its two borders", func(t *testing.T) {
		screen := "history line\n" + piBoxSeparator + "\nRead the file " + path + " in full and execute it.\n" + piBoxSeparator + "\n~/repo (main)\n"
		lines, ok := ComposerRegion("pi", screen)
		if !ok {
			t.Fatalf("pi's two borders must form the region:\n%s", screen)
		}
		if len(lines) != 1 || !strings.Contains(lines[0], path) {
			t.Fatalf("the pi box holds the typed line: %+v", lines)
		}
	})
	t.Run("the path wrapped over pi box lines rejoins", func(t *testing.T) {
		screen := "history line\n" + piBoxSeparator + "\nRead the file " + path[:len(path)-13] + "\n" + path[len(path)-13:] + " in full and execute it.\n" + piBoxSeparator + "\n~/repo (main)\n"
		lines, ok := ComposerRegion("pi", screen)
		if !ok {
			t.Fatal("pi's two borders must form the region")
		}
		if !strings.Contains(strings.Join(lines, ""), path) {
			t.Fatalf("the joined box lines must hold the wrapped path: %+v", lines)
		}
	})
	t.Run("a pi-shaped screen is not a pi region for another kind", func(t *testing.T) {
		screen := "history line\n" + piBoxSeparator + "\nRead the file " + path + "\n" + piBoxSeparator + "\n~/repo (main)\n"
		if _, ok := ComposerRegion("codex", screen); ok {
			t.Fatal("the pi borders are not a codex composer")
		}
		if _, ok := ComposerRegion("claude", screen); ok {
			t.Fatal("the pi borders are not the claude box (no ❯ inside)")
		}
	})
	t.Run("an unrecognized kind has no region", func(t *testing.T) {
		for _, kind := range []string{"", "cursor", "grok", "opencode"} {
			if _, ok := ComposerRegion(kind, codexEvidenceScreen(t)); ok {
				t.Fatalf("kind %q has no recognized composer region", kind)
			}
		}
	})
	t.Run("a screen without any recognized composer has no region", func(t *testing.T) {
		for _, kind := range []string{"pi", "claude", "codex"} {
			if _, ok := ComposerRegion(kind, "plain idle screen\n"); ok {
				t.Fatalf("kind %q: a plain screen must not form the region", kind)
			}
		}
	})
}

// TestComposerHoldsPathBoundary pins the exact-path rule: the composed path
// must appear as a whole path. A longer path (".md.rejected") or a longer
// prefix never passes, a wrap mid-word rejoins, and a boundary rune after
// the path (prose, quotes, glyphs) ends it.
func TestComposerHoldsPathBoundary(t *testing.T) {
	path := "/tmp/herdr-soho/ws/briefs/worker-20261003T152648.md"
	longer := path + ".rejected"
	t.Run("the exact path at the region end holds", func(t *testing.T) {
		if !ComposerHoldsPath([]string{path}, path) {
			t.Fatal("the exact path alone must hold")
		}
	})
	t.Run("the exact path followed by prose holds", func(t *testing.T) {
		lines := []string{"> Read the file " + path + " in full and execute it."}
		if !ComposerHoldsPath(lines, path) {
			t.Fatalf("the prose after the path must end it: %+v", lines)
		}
	})
	t.Run("a longer .md.rejected path is not the composed path", func(t *testing.T) {
		lines := []string{"> Read the file " + longer + " in full and execute it."}
		if ComposerHoldsPath(lines, path) {
			t.Fatalf("the longer path must not hold for the shorter one: %+v", lines)
		}
		if !ComposerHoldsPath(lines, longer) {
			t.Fatal("the longer path must hold for itself")
		}
	})
	t.Run("the path embedded in a longer path fails", func(t *testing.T) {
		lines := []string{"> Read the file xx" + path + " in full and execute it."}
		if ComposerHoldsPath(lines, path) {
			t.Fatalf("a longer prefix path must not hold: %+v", lines)
		}
	})
	t.Run("the wrapped mid-word path rejoins and holds", func(t *testing.T) {
		lines := []string{"> Read the file " + path[:len(path)-13], path[len(path)-13:] + " in full and execute it."}
		if !ComposerHoldsPath(lines, path) {
			t.Fatalf("the wrapped path must rejoin: %+v", lines)
		}
	})
	t.Run("the wrapped longer path fails", func(t *testing.T) {
		lines := []string{"> Read the file " + longer[:len(longer)-9], longer[len(longer)-9:] + " in full and execute it."}
		if ComposerHoldsPath(lines, path) {
			t.Fatalf("the wrapped longer path must not hold: %+v", lines)
		}
	})
	t.Run("the path inside quotes holds", func(t *testing.T) {
		lines := []string{"> the brief \"" + path + "\" is on disk"}
		if !ComposerHoldsPath(lines, path) {
			t.Fatalf("quotes must end the path: %+v", lines)
		}
	})
	t.Run("an empty composed path holds nothing", func(t *testing.T) {
		if ComposerHoldsPath([]string{"> anything"}, "") {
			t.Fatal("an empty path must not hold")
		}
	})
	t.Run("ScreenShowsPath is the inclusive visibility check", func(t *testing.T) {
		if !ScreenShowsPath("history\n› Read the file "+path+" in full and execute it.\n", path) {
			t.Fatal("the screen shows the exact path")
		}
		if !ScreenShowsPath("history\nRead the file "+longer+" in full and execute it.\n› Ask Codex\n", path) {
			t.Fatal("a longer path shows the path text: the delivery stays uncertain, it is not proof")
		}
		if ScreenShowsPath("plain idle screen\n", path) {
			t.Fatal("a screen without the path shows nothing")
		}
	})
	t.Run("a longer path split across lines is not held", func(t *testing.T) {
		lines := []string{"> Read the file " + path, ".rejected in full and execute it."}
		if ComposerHoldsPath(lines, path) {
			t.Fatalf("a wrap at the path end continuing on the next line must not hold: %+v", lines)
		}
	})
	t.Run("a wrap at a space inside prose rejoins the path", func(t *testing.T) {
		lines := []string{"> Read the file " + path + " in full and", "execute it."}
		if !ComposerHoldsPath(lines, path) {
			t.Fatalf("the path on the first line with the prose wrapped must hold: %+v", lines)
		}
	})
	t.Run("LineHoldsPath on one line", func(t *testing.T) {
		if !LineHoldsPath("Read the file "+path+" in full and execute it.", path) {
			t.Fatal("the line holds the exact path")
		}
		if LineHoldsPath("Read the file "+longer+" in full and execute it.", path) {
			t.Fatal("the longer path must not hold for the shorter one")
		}
	})
	t.Run("ComposerRegionBounds agrees with ComposerRegion", func(t *testing.T) {
		screen := "history line\n" + claudeBoxSeparator + "\n❯ Read the file " + path + " in full and execute it.\n" + claudeBoxSeparator + "\n  [Opus 5.5] 67% [main*]\n"
		start, end, ok := ComposerRegionBounds("claude", screen)
		if !ok {
			t.Fatal("the claude box must form the bounds")
		}
		lines := strings.Split(strings.ReplaceAll(screen, "\r\n", "\n"), "\n")
		region, okRegion := ComposerRegion("claude", screen)
		if !okRegion || len(region) != end-start || region[0] != lines[start] {
			t.Fatalf("bounds=%d..%d region=%+v", start, end, region)
		}
	})
}

func TestDialogUI(t *testing.T) {
	t.Run("codex question lines", func(t *testing.T) {
		for _, line := range []string{"enter to submit answer", "ENTER TO SUBMIT ALL"} {
			if got := DialogUI("codex", "• working\n"+line+"\n"); got != "question" {
				t.Fatalf("%q must classify as question, got %q", line, got)
			}
		}
	})
	t.Run("codex without a question line is no known UI", func(t *testing.T) {
		if got := DialogUI("codex", codexEvidenceScreen(t)); got != "" {
			t.Fatalf("the working codex screen carries no known UI, got %q", got)
		}
	})
	t.Run("claude question lines", func(t *testing.T) {
		screen := "❯ option A\n❯ option B\n  to navigate · enter to select\n"
		if got := DialogUI("claude", screen); got != "question" {
			t.Fatalf("the claude selection screen must classify as question, got %q", got)
		}
	})
	t.Run("claude permission lines are the approval UI", func(t *testing.T) {
		screen := "Do you want to run `rm -rf dist`?\n  to navigate · enter to select\n"
		if got := DialogUI("claude", screen); got != "approval" {
			t.Fatalf("the claude permission screen must classify as approval, got %q", got)
		}
		if got := DialogUI("claude", "Do you want to continue?\n"); got != "approval" {
			t.Fatalf("the lone claude permission line must classify as approval, got %q", got)
		}
	})
	t.Run("opencode question lines", func(t *testing.T) {
		screen := "Which file?\n  a) alpha\n  esc dismiss · enter submit\n"
		if got := DialogUI("opencode", screen); got != "question" {
			t.Fatalf("the opencode question screen must classify as question, got %q", got)
		}
		if got := DialogUI("opencode", "permission required\n"); got != "approval" {
			t.Fatalf("the opencode permission screen must classify as approval, got %q", got)
		}
	})
	t.Run("an unrecognized kind or screen is no known UI", func(t *testing.T) {
		if got := DialogUI("pi", "enter to submit answer\n"); got != "" {
			t.Fatalf("the pi screen carries no known UI from the codex classifier, got %q", got)
		}
		if got := DialogUI("codex", "plain idle screen\n"); got != "" {
			t.Fatalf("a plain codex screen carries no known UI, got %q", got)
		}
	})
}
