package dispatch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnseenPathWarningsSkipALineWithTheAbsolutePath(t *testing.T) {
	root := t.TempDir()
	worker := t.TempDir()
	for _, name := range []string{"x.png", "y.png"} {
		p := filepath.Join(root, "design", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	x := filepath.Join(root, "design", "x.png")
	y := filepath.Join(root, "design", "y.png")
	for _, tc := range []struct {
		name, body string
		warns      bool
	}{
		{"absolute in parentheses", "See design/x.png (" + x + ") for the layout.\n", false},
		{"absolute in backticks", "See `design/x.png` (`" + x + "`).\n", false},
		{"relative only", "See design/x.png for the layout.\n", true},
		{"another file's absolute", "See design/x.png (" + y + ").\n", true},
		{"a longer path sharing the prefix", "See design/x.png (" + x + ".bak).\n", true},
		{"absolute on another line", "See design/x.png.\nThe file is " + x + ".\n", true},
		{"another root ending in the same path", "See design/x.png (/elsewhere" + x + ").\n", true},
		{"a longer path with a non-ASCII letter", "See design/x.png (" + x + "é).\n", true},
		{"a combining mark after the path", "See design/x.png (" + x + "\u0301).\n", true},
		{"absolute after a colon", "See design/x.png: " + x + "\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := unseenPathWarnings("brief.md", tc.body, worker, []string{root})
			warned := false
			for _, w := range got {
				if strings.Contains(w, "cites 'design/x.png'") {
					warned = true
				}
			}
			if warned != tc.warns {
				t.Fatalf("warned=%t want %t: %q", warned, tc.warns, got)
			}
		})
	}
}

// newGitWorktreeFixture builds a real temporary Git main checkout with a
// linked worktree: the worker branch is created before the main-only files
// are added, the git-ignored soho state exists only in the main checkout,
// and the shared file is committed before the branch, so both checkouts
// hold it. The fixture never touches the repository under test.
func newGitWorktreeFixture(t *testing.T) (main, worker string) {
	t.Helper()
	base := t.TempDir()
	main = filepath.Join(base, "main")
	worker = filepath.Join(base, "worker")
	if err := os.MkdirAll(main, 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = main
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	write := func(name, content string) {
		t.Helper()
		p := filepath.Join(main, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.invalid")
	run("config", "user.name", "Test")
	write(".gitignore", ".soho/\n")
	write("shared.txt", "shared\n")
	run("add", "-A")
	run("commit", "-qm", "fixture")
	// The worker branch and its linked worktree exist before the main-only
	// files, so they hold the shared file and nothing main-only.
	run("worktree", "add", "-q", "-b", "worker", worker)
	write("main-only.txt", "main only\n")
	run("add", "main-only.txt")
	run("commit", "-qm", "main only")
	// Untracked main-only files whose names carry test/temp/example words:
	// the marker must not infer intent from them.
	write("temp/example-data.txt", "data\n")
	write("docs/test-example.md", "example doc\n")
	write("example/notes.md", "notes\n")
	// Git-ignored soho state, on disk in the main checkout only.
	write(filepath.Join(".soho", "ws", "briefs", "state-brief.md"), "state\n")
	return main, worker
}

// The worker tree is the linked worktree: `.git` is a file there, so the
// relative path `.git/config` is absent in the worker while the main
// checkout holds the real `.git/config` file.
func TestUnseenPathWarningsConditionalMessage(t *testing.T) {
	main, worker := newGitWorktreeFixture(t)
	roots := []string{main, main}
	conditional := func(candidate string) string {
		return "brief brief.md line 1 cites '" + candidate + "', which the worker in " + worker +
			" does not find at that relative path; a matching path exists at " + filepath.Join(main, candidate) +
			" under checkout " + main + ", outside the worker tree; use it only if this brief intends that file"
	}
	for _, tc := range []struct {
		name  string
		body  string
		roots []string
		exact string
		warns bool
	}{
		{
			name:  "a main-only project path warns with the conditional message",
			body:  "Read `main-only.txt`.\n",
			roots: roots,
			exact: conditional("main-only.txt"),
			warns: true,
		},
		{
			name:  "a bare main-only path warns with the conditional message",
			body:  "Read main-only.txt for the layout.\n",
			roots: roots,
			exact: conditional("main-only.txt"),
			warns: true,
		},
		{
			name:  "a main-only .soho state file keeps its absolute match",
			body:  "Read `.soho/ws/briefs/state-brief.md`.\n",
			roots: roots,
			exact: conditional(".soho/ws/briefs/state-brief.md"),
			warns: true,
		},
		{
			name:  "a path present in the worker stays silent",
			body:  "Read `shared.txt`.\n",
			roots: roots,
			warns: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := unseenPathWarnings("brief.md", tc.body, worker, tc.roots)
			if tc.warns {
				if len(got) != 1 || got[0] != tc.exact {
					t.Fatalf("got %d warning(s):\n%q\nwant exactly:\n%q", len(got), got, tc.exact)
				}
				return
			}
			if len(got) != 0 {
				t.Fatalf("unexpected warnings: %q", got)
			}
		})
	}
	t.Run("the matched root is recorded, not always the first root", func(t *testing.T) {
		second := t.TempDir()
		p := filepath.Join(second, "second-only.txt")
		if err := os.WriteFile(p, []byte("second\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got := unseenPathWarnings("brief.md", "Read `second-only.txt`.\n", worker, []string{main, second})
		if len(got) != 1 || !strings.Contains(got[0], "cites 'second-only.txt'") ||
			!strings.Contains(got[0], "a matching path exists at "+p+" under checkout "+second) {
			t.Fatalf("got %d warning(s):\n%q\nwant the second root named as the matched checkout", len(got), got)
		}
	})
}

func TestUnseenPathWarningsExampleMarker(t *testing.T) {
	main, worker := newGitWorktreeFixture(t)
	roots := []string{main, main}
	cites := func(got []string, candidate string) []string {
		var hits []string
		for _, w := range got {
			if strings.Contains(w, "cites '"+candidate+"'") {
				hits = append(hits, w)
			}
		}
		return hits
	}
	for _, tc := range []struct {
		name  string
		body  string
		warns bool
	}{
		{"a single-backtick span preceded by example: is silent", "Read example: `.git/config` for the config.\n", false},
		{"the same unmarked literal citation warns", "Read `.git/config` for the config.\n", true},
		{"the mixed-case marker works", "Read Example: `.git/config` for the config.\n", false},
		{"the uppercase marker works", "Read EXAMPLE: `.git/config` for the config.\n", false},
		{"a space after the colon works", "Read example:   `.git/config` for the config.\n", false},
		{"a tab after the colon works", "Read example:\t`.git/config` for the config.\n", false},
		{"an opening parenthesis delimiter works", "Read (example: `.git/config`) for the config.\n", false},
		{"an opening bracket delimiter works", "Read [example: `.git/config`] for the config.\n", false},
		{"an opening brace delimiter works", "Read {example: `.git/config`} for the config.\n", false},
		{"the marker at the line start works", "example: `.git/config` is the linked worktree's Git config.\n", false},
		{"multiple marked spans on one line are all silent", "Read example: `.git/config` and example: `main-only.txt`.\n", false},
		{"an escaped marker is ordinary text", "Read \\example: `.git/config` for the config.\n", true},
		{"a word-attached marker is ordinary text", "Read myexample: `.git/config` for the config.\n", true},
		{"a hyphen-attached marker is ordinary text", "Read not-example: `.git/config` for the config.\n", true},
		{"a punctuation-attached marker is ordinary text", "Read foo.example: `.git/config` for the config.\n", true},
		{"a missing colon is ordinary text", "Read example `.git/config` for the config.\n", true},
		{"a bare path after the marker is checked", "Read example: .git/config for the config.\n", true},
		{"an unclosed span is not a marker", "Read example: `.git/config for the config.\n", false},
		{"an empty span is not a marker", "Read example: `` and `main-only.txt`.\n", false},
		{"a double-backtick form does not gain an exemption", "Read example: ``main-only.txt`` for the config.\n", true},
		{"a triple-backtick form does not gain an exemption", "Read example: ```main-only.txt``` for the config.\n", true},
		{"an unclosed marker leaves a later bare path checked", "example: `x see main-only.txt\n", true},
		{"a path with a temp and an example word is checked", "Read `temp/example-data.txt` for the data.\n", true},
		{"a path with a test and an example word is checked", "Read `docs/test-example.md` for the doc.\n", true},
		{"a path starting with example is checked", "Read example/notes.md for the notes.\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := unseenPathWarnings("brief.md", tc.body, worker, roots)
			switch tc.name {
			case "a path with a temp and an example word is checked":
				if n := len(cites(got, "temp/example-data.txt")); n != 1 {
					t.Fatalf("warnings: %q", got)
				}
			case "a path with a test and an example word is checked":
				if n := len(cites(got, "docs/test-example.md")); n != 1 {
					t.Fatalf("warnings: %q", got)
				}
			case "a path starting with example is checked":
				if n := len(cites(got, "example/notes.md")); n != 1 {
					t.Fatalf("warnings: %q", got)
				}
			default:
				warned := len(cites(got, ".git/config")) > 0 || len(cites(got, "main-only.txt")) > 0
				if warned != tc.warns {
					t.Fatalf("warned=%t want %t: %q", warned, tc.warns, got)
				}
			}
		})
	}
}

func TestUnseenPathWarningsExampleMaskSemantics(t *testing.T) {
	main, worker := newGitWorktreeFixture(t)
	roots := []string{main, main}
	absMainOnly := filepath.Join(main, "main-only.txt")
	// The marked example holds a path with spaces; its suffix `docs/x`
	// also exists in the main checkout, so a mask that leaves the span
	// text visible to the bare-token extraction leaks a candidate.
	spaced := filepath.Join(main, "see the docs", "x")
	if err := os.MkdirAll(filepath.Dir(spaced), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spaced, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(main, "docs", "x"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body string
		warns      bool
	}{
		{
			name:  "a marked absolute example does not silence a real relative citation on the same line",
			body:  "Read `main-only.txt` (example: `" + absMainOnly + "`).\n",
			warns: true,
		},
		{
			name:  "a marked path with spaces leaks no bare suffix candidate",
			body:  "Read example: `" + spaced + "` for the layout.\n",
			warns: false,
		},
		{
			name:  "an identical marked and unmarked path warns once",
			body:  "example: `main-only.txt` and `main-only.txt`.\n",
			warns: true,
		},
		{
			name:  "an ordinary unmarked same-line absolute citation still silences",
			body:  "Read `main-only.txt` (" + absMainOnly + ").\n",
			warns: false,
		},
		{
			name:  "unicode text around the marker keeps the positions",
			body:  "Café example: `main-only.txt` and `main-only.txt`.\n",
			warns: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := unseenPathWarnings("brief.md", tc.body, worker, roots)
			var hits []string
			for _, w := range got {
				if strings.Contains(w, "cites 'main-only.txt'") {
					hits = append(hits, w)
				}
			}
			if tc.name == "a marked path with spaces leaks no bare suffix candidate" {
				if len(got) != 0 {
					t.Fatalf("unexpected warnings: %q", got)
				}
				return
			}
			if tc.warns {
				if len(hits) != 1 {
					t.Fatalf("want exactly one 'main-only.txt' warning, got %d: %q", len(hits), got)
				}
				return
			}
			if len(got) != 0 {
				t.Fatalf("unexpected warnings: %q", got)
			}
		})
	}
}

// runUnseenPathWarningsNoPanic wraps one scanner call in a recover so a
// hostile line fails the test as an assertion instead of aborting the
// whole test process: the F1 defect used to panic the package run on a
// harmless line.
func runUnseenPathWarningsNoPanic(t *testing.T, brief, body, worker string, roots []string) []string {
	t.Helper()
	var got []string
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("unseenPathWarnings panicked on %q: %v", body, r)
			}
		}()
		got = unseenPathWarnings(brief, body, worker, roots)
	}()
	return got
}

// F1 regression: a line that ends exactly in the example word — with or
// without an ASCII or Unicode whitespace prefix — used to panic the
// scanner at the colon read. These harmless lines must complete and flow
// through the ordinary policy: no marker, no exemption side effects.
func TestUnseenPathWarningsExampleWordAtLineEnd(t *testing.T) {
	main, worker := newGitWorktreeFixture(t)
	roots := []string{main, main}
	for _, tc := range []struct {
		name, body string
	}{
		{"a line that is exactly the word", "example\n"},
		{"a line ending in the word", "Read an example\n"},
		{"an uppercase line ending in the word", "EXAMPLE\n"},
		{"a unicode line ending in the word", "Café example\n"},
		{"a unicode-whitespace prefix ending in the word", "\u00A0example\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := runUnseenPathWarningsNoPanic(t, "brief.md", tc.body, worker, roots)
			if len(got) != 0 {
				t.Fatalf("the harmless line must not warn: %q", got)
			}
		})
	}
	t.Run("a real citation before a line-ending word still warns once", func(t *testing.T) {
		got := runUnseenPathWarningsNoPanic(t, "brief.md", "Read `main-only.txt` then read an example\n", worker, roots)
		if len(got) != 1 || !strings.Contains(got[0], "cites 'main-only.txt'") ||
			!strings.Contains(got[0], "under checkout "+main) {
			t.Fatalf("want exactly one conditional 'main-only.txt' warning, got %d: %q", len(got), got)
		}
	})
}

// F2 regression: the closing backtick of a marked span must be a single
// delimiter. A longer closing run is a malformed citation: it is not
// masked and stays visible to the ordinary extraction policy.
func TestUnseenPathWarningsMalformedClosingBacktickRun(t *testing.T) {
	main, worker := newGitWorktreeFixture(t)
	roots := []string{main, main}
	for _, tc := range []struct {
		name, body string
	}{
		{"a single-open double-close form is not masked", "Read example: `main-only.txt`` for the config.\n"},
		{"a single-open triple-close form is not masked", "Read example: `main-only.txt``` for the config.\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := runUnseenPathWarningsNoPanic(t, "brief.md", tc.body, worker, roots)
			var hits []string
			for _, w := range got {
				if strings.Contains(w, "cites 'main-only.txt'") {
					hits = append(hits, w)
				}
			}
			if len(hits) != 1 || !strings.Contains(hits[0], "under checkout "+main) {
				t.Fatalf("want exactly one conditional 'main-only.txt' warning (the malformed citation must stay visible), got %d: %q", len(hits), got)
			}
		})
	}
	t.Run("a valid single-open single-close example stays silent", func(t *testing.T) {
		got := runUnseenPathWarningsNoPanic(t, "brief.md", "Read example: `main-only.txt` for the config.\n", worker, roots)
		if len(got) != 0 {
			t.Fatalf("the valid example must stay silent: %q", got)
		}
	})
}
