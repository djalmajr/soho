package setuptext

// R329: the native Core bullet gains the wake arming (`soho wait <agent>
// --wake`). The exact bullet of the generation before is replaced in place
// in a migrated block, an absent one is added, and a bullet the project
// edited is left as it is, never doubled.

import (
	"strings"
	"testing"
)

// previousNativeBulletText is the bullet of the generation before the wake
// arming, pinned independently of the implementation's constant.
const previousNativeBulletText = "- **Native Core** (`SOHO_TRANSPORT=hc`): workers and long runs are child\n  terminals of the orchestrator, never background tasks of the assistant's\n  harness. Builds, tests and scripts run as the child's own program\n  (`hc run --bg --outlive-parent --name <project>-<role> -- sh -c '<cmd>'`):\n  its exit note wakes the orchestrator. A soho worker stays alive after its\n  report and sends no note, so the orchestrator keeps its turn open and\n  waits (`soho wait`) while it depends on one.\n"

// nativeWakeEnding is what the current bullet adds to the previous ending.
const nativeWakeEnding = "waits (`soho wait`) while it depends on one, or arms\n  `soho wait <agent> --wake` and ends its turn: the note wakes it, and it\n  runs `soho wait <agent>` once.\n"

func legacyForm(block string) string { return strings.ReplaceAll(block, "soho", "herdr-soho") }

func TestNativeBulletNamesTheWakeArming(t *testing.T) {
	if !strings.Contains(nativeBulletText, nativeWakeEnding) || !strings.Contains(sohoBlock, nativeBulletText) {
		t.Fatal("the pinned bullet or block lost the wake arming ending")
	}
	if strings.Contains(previousNativeBulletText, "--wake") {
		t.Fatal("the previous bullet fixture already names --wake")
	}
	if strings.Replace(nativeBulletText, nativeWakeEnding, "waits (`soho wait`) while it depends on one.\n", 1) != previousNativeBulletText {
		t.Fatal("the new bullet is not the previous one with only the ending changed")
	}
	// Wrapped like its neighbours: no line of the bullet passes 76 columns.
	for _, line := range strings.Split(nativeBulletText, "\n") {
		if len([]rune(line)) > 76 {
			t.Fatalf("bullet line is %d columns: %q", len([]rune(line)), line)
		}
	}
	if SetupBlock() != sohoBlock {
		t.Fatal("the generated block is not the pinned one")
	}
}

func TestNativeBulletMigration(t *testing.T) {
	withPrevious := strings.Replace(sohoBlock, nativeBulletText, previousNativeBulletText, 1)
	withoutBullet := strings.Replace(sohoBlock, nativeBulletText, "", 1)
	if withPrevious == sohoBlock || withoutBullet == sohoBlock {
		t.Fatal("fixtures were not built")
	}
	edit := func(old, new string) string {
		out := strings.Replace(withPrevious, old, new, 1)
		if out == withPrevious {
			t.Fatalf("edit %q not applied", old)
		}
		return out
	}
	t.Run("the exact previous bullet is replaced in place", func(t *testing.T) {
		for _, legacy := range []bool{false, true} {
			input := withPrevious
			if legacy {
				input = legacyForm(input)
			}
			in := "before\n" + input + "after\n"
			got := SetupBlockResult(&in)
			if got == nil || *got != "before\n"+sohoBlock+"after\n" {
				t.Fatalf("legacy=%v result: %v", legacy, got)
			}
			if second := SetupBlockResult(got); second == nil || *second != *got {
				t.Fatalf("legacy=%v: not idempotent", legacy)
			}
		}
	})
	t.Run("an absent bullet is added before the Quota item", func(t *testing.T) {
		in := "before\n" + legacyForm(withoutBullet) + "after\n"
		got := SetupBlockResult(&in)
		if got == nil || *got != "before\n"+sohoBlock+"after\n" {
			t.Fatalf("result: %v", got)
		}
	})
	t.Run("an edited bullet is left alone and not doubled", func(t *testing.T) {
		for _, tc := range []struct{ name, input, kept string }{
			{"changed word", edit("keeps its turn open and", "keeps its turn open, always, and"), "keeps its turn open, always, and"},
			{"extra continuation line", edit("while it depends on one.\n", "while it depends on one.\n  Our house rule applies.\n"), "Our house rule applies."},
			{"an edited current bullet", strings.Replace(sohoBlock, "its exit note wakes the orchestrator.", "its exit note wakes us.", 1), "its exit note wakes us."},
		} {
			input := legacyForm(tc.input)
			in := "before\n" + input + "after\n"
			got := SetupBlockResult(&in)
			if got == nil || !strings.Contains(*got, tc.kept) {
				t.Fatalf("%s: the edit was lost: %v", tc.name, got)
			}
			if n := strings.Count(*got, "- **Native Core**"); n != 1 {
				t.Fatalf("%s: %d Native Core items, want the edited one only", tc.name, n)
			}
			if strings.Contains(*got, "--wake") && tc.name != "an edited current bullet" {
				t.Fatalf("%s: the new bullet text was spliced next to the edited one", tc.name)
			}
		}
	})
	t.Run("a block with the current markers is replaced as a whole", func(t *testing.T) {
		in := "before\n" + withPrevious + "after\n"
		got := SetupBlockResult(&in)
		if got == nil || *got != "before\n"+sohoBlock+"after\n" {
			t.Fatalf("result: %v", got)
		}
	})
}
