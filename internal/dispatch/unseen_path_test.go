package dispatch

import (
	"os"
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
