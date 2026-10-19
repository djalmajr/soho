package codexhooks

import (
	"reflect"
	"strings"
	"testing"
)

// The real codex 0.163.0 screens of the brief (DJA-434).
const menuScreen = "  Hooks need review\n" +
	"  3 hooks are new or changed.\n" +
	"  Hooks can run outside the sandbox after you trust them.\n" +
	"\n" +
	"\n" +
	"› 1. Review hooks\n" +
	"  2. Trust all and continue\n" +
	"  3. Continue without trusting (hooks won't run)\n" +
	"\n" +
	"  enter confirm · esc skip\n"

const tableScreen = "Hooks\n" +
	"Lifecycle hooks from config and enabled plugins.\n" +
	"⚠ 3 hooks need review before they can run.\n" +
	"SessionStart  4 installed  2 active  2 review\n" +
	"SessionEnd    1 installed  0 active  1 review\n" +
	"t trust all · enter review · esc close\n"

// composer is codex's normal prompt area, below any transcript.
const composer = "› Ask Codex to do anything\n" +
	"GPT-6.1-Sol low · Context 0% used · weekly 65% left · main\n" +
	"? for shortcuts\n"

func TestRecognitionNeedsTheFooterOnTheLastLine(t *testing.T) {
	for _, tc := range []struct {
		name        string
		screen      string
		menu, table bool
	}{
		{"the menu with blank lines after the footer", menuScreen + "\n\n   \n", true, false},
		{"the table with blank lines after the footer", tableScreen + "\n\n", false, true},
		{"the whole menu quoted above the composer", strings.Replace(menuScreen, "› 1.", "  1.", 1) + composer, false, false},
		{"the whole table quoted above the composer", tableScreen + composer, false, false},
		{"the menu footer above its title", "  enter confirm · esc skip\n" + strings.Replace(menuScreen, "  enter confirm · esc skip\n", "", 1), false, false},
	} {
		if got := IsMenu(tc.screen); got != tc.menu {
			t.Errorf("%s: IsMenu = %v, want %v", tc.name, got, tc.menu)
		}
		if got := IsTable(tc.screen); got != tc.table {
			t.Errorf("%s: IsTable = %v, want %v", tc.name, got, tc.table)
		}
	}
}

// scriptedIO serves the given reads in order (the last one repeats) and
// records the keys.
type scriptedIO struct {
	reads []string
	keys  []string
}

func (s *scriptedIO) io() IO {
	return IO{
		SendKey: func(key string) bool { s.keys = append(s.keys, key); return true },
		Read: func() string {
			read := s.reads[0]
			if len(s.reads) > 1 {
				s.reads = s.reads[1:]
			}
			return read
		},
	}
}

func TestApproveRedrawThatLosesTheMenuSendsNoEnter(t *testing.T) {
	// After the down the re-read shows only the selected line: the menu is
	// gone, so no enter goes and the approval fails.
	s := &scriptedIO{reads: []string{"› 2. Trust all and continue\n"}}
	_, ok := Approve(menuScreen, s.io())
	if ok {
		t.Fatalf("a lost menu was approved")
	}
	if want := []string{"down"}; !reflect.DeepEqual(s.keys, want) {
		t.Fatalf("keys = %q, want %q", s.keys, want)
	}
}
