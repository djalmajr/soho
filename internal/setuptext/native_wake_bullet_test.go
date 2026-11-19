package setuptext

// SOH-450: the native Core bullet reaches hc standard v7 — the
// orchestrator's prompt never blocks. The exact v6 bullet (wake arming next
// to the foreground wait it kept) and the exact pre-wake bullet are replaced
// in place in a migrated legacy block, an absent one is added, an edited
// bullet is left as it is, and a second run changes nothing.

import (
	"strings"
	"testing"
)

// v6NativeBulletText is the v6 bullet (the wake arming, with the foreground
// wait kept), pinned independently of the implementation's constant.
const v6NativeBulletText = "- **Native Core** (`SOHO_TRANSPORT=hc`): workers and long runs are child\n  terminals of the orchestrator, never background tasks of the assistant's\n  harness. Builds, tests and scripts run as the child's own program\n  (`hc run --bg --outlive-parent --name <project>-<role> -- sh -c '<cmd>'`):\n  its exit note wakes the orchestrator. A soho worker stays alive after its\n  report and sends no note, so the orchestrator keeps its turn open and\n  waits (`soho wait`) while it depends on one, or arms\n  `soho wait <agent> --wake` and ends its turn: the note wakes it, and it\n  runs `soho wait <agent>` once.\n"

// preWakeNativeBulletText is the bullet of the generation before the wake
// arming, pinned the same way.
const preWakeNativeBulletText = "- **Native Core** (`SOHO_TRANSPORT=hc`): workers and long runs are child\n  terminals of the orchestrator, never background tasks of the assistant's\n  harness. Builds, tests and scripts run as the child's own program\n  (`hc run --bg --outlive-parent --name <project>-<role> -- sh -c '<cmd>'`):\n  its exit note wakes the orchestrator. A soho worker stays alive after its\n  report and sends no note, so the orchestrator keeps its turn open and\n  waits (`soho wait`) while it depends on one.\n"

func legacyForm(block string) string { return strings.ReplaceAll(block, "soho", "herdr-soho") }

func TestNativeBulletNeverBlocks(t *testing.T) {
	if strings.Contains(v6NativeBulletText, "The orchestrator's prompt never") || !strings.Contains(v6NativeBulletText, "--wake") {
		t.Fatal("the v6 fixture is not the v6 bullet")
	}
	if strings.Contains(preWakeNativeBulletText, "--wake") || !strings.HasSuffix(preWakeNativeBulletText, "waits (`soho wait`) while it depends on one.\n") {
		t.Fatal("the pre-wake fixture is not the pre-wake bullet")
	}
	if strings.Replace(v6NativeBulletText, "so the orchestrator keeps its turn open and\n  waits (`soho wait`) while it depends on one, or arms\n  `soho wait <agent> --wake` and ends its turn: the note wakes it, and it\n  runs `soho wait <agent>` once.\n", "", 1) != strings.Replace(preWakeNativeBulletText, "so the orchestrator keeps its turn open and\n  waits (`soho wait`) while it depends on one.\n", "", 1) {
		t.Fatal("the two previous fixtures are not the same bullet up to the ending")
	}
	if !strings.Contains(nativeBulletText, "its exit note wakes the orchestrator. The orchestrator's prompt never\n  blocks:") {
		t.Fatal("the current bullet does not open with the never-blocks rule")
	}
	if !strings.Contains(nativeBulletText, "dispatch with `--no-wait`, arm `soho wait <agent> --wake` and end the") ||
		!strings.Contains(nativeBulletText, "No\n  foreground `soho wait --timeout` or `hc wait`, in blocks or in a loop.\n") {
		t.Fatal("the current bullet does not replace the foreground wait with dispatch --no-wait plus the wake")
	}
	if !strings.Contains(sohoBlock, nativeBulletText) {
		t.Fatal("the generated block does not carry the current bullet")
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
	withV6 := strings.Replace(sohoBlock, nativeBulletText, v6NativeBulletText, 1)
	withPreWake := strings.Replace(sohoBlock, nativeBulletText, preWakeNativeBulletText, 1)
	withoutBullet := strings.Replace(sohoBlock, nativeBulletText, "", 1)
	if withV6 == sohoBlock || withPreWake == sohoBlock || withoutBullet == sohoBlock {
		t.Fatal("fixtures were not built")
	}
	// The in-place upgrade path runs on a migrated legacy block: a
	// current-marker block is replaced as a whole, which would hide a
	// missing previous generation.
	for _, tc := range []struct{ generation, block string }{
		{"the exact v6 bullet", withV6},
		{"the exact pre-wake bullet", withPreWake},
	} {
		t.Run(tc.generation+" is replaced in place", func(t *testing.T) {
			in := "before\n" + legacyForm(tc.block) + "after\n"
			got := SetupBlockResult(&in)
			if got == nil {
				t.Fatal("result: refused")
			}
			if *got != "before\n"+sohoBlock+"after\n" {
				t.Fatalf("result:\n%q", *got)
			}
			if second := SetupBlockResult(got); second == nil || *second != *got {
				t.Fatal("a second setup run is not a no-op")
			}
		})
	}
	t.Run("an absent bullet is added before the Quota item", func(t *testing.T) {
		in := "before\n" + legacyForm(withoutBullet) + "after\n"
		got := SetupBlockResult(&in)
		if got == nil {
			t.Fatal("result: refused")
		}
		if *got != "before\n"+sohoBlock+"after\n" {
			t.Fatalf("result:\n%q", *got)
		}
		if second := SetupBlockResult(got); second == nil || *second != *got {
			t.Fatal("a second setup run is not a no-op")
		}
	})
	t.Run("an edited bullet is left alone and not doubled", func(t *testing.T) {
		edit := func(block, old, new string) string {
			out := strings.Replace(block, old, new, 1)
			if out == block {
				t.Fatalf("edit %q not applied", old)
			}
			return out
		}
		for _, tc := range []struct {
			name, input, kept string
			v7WasAdded        bool
		}{
			{"v6 changed word", edit(withV6, "keeps its turn open and", "keeps its turn open, always, and"), "keeps its turn open, always, and", false},
			{"v6 extra continuation line", edit(withV6, "once.\n", "once.\n  Our house rule applies.\n"), "Our house rule applies.", false},
			{"pre-wake changed word", edit(withPreWake, "while it depends on one.", "while it depends on one, always."), "while it depends on one, always.", false},
			{"pre-wake extra continuation line", edit(withPreWake, "one.\n", "one.\n  Our house rule applies.\n"), "Our house rule applies.", false},
			{"an edited current bullet", edit(sohoBlock, "its exit note wakes the orchestrator.", "its exit note wakes us."), "its exit note wakes us.", true},
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
			if *got == "before\n"+sohoBlock+"after\n" {
				t.Fatalf("%s: the edited block was replaced as a whole", tc.name)
			}
			if !tc.v7WasAdded && strings.Contains(*got, "dispatch with `--no-wait`") {
				t.Fatalf("%s: the v7 bullet was spliced next to the edited one", tc.name)
			}
		}
	})
	t.Run("a block with the current markers is replaced as a whole", func(t *testing.T) {
		for _, block := range []string{withV6, withPreWake} {
			in := "before\n" + block + "after\n"
			got := SetupBlockResult(&in)
			if got == nil {
				t.Fatal("result: refused")
			}
			if *got != "before\n"+sohoBlock+"after\n" {
				t.Fatalf("result:\n%q", *got)
			}
		}
	})
}
