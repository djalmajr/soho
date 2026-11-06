package core

import (
	"os"
	"path/filepath"
	"testing"
)

// SOH-233 / DJA-659: post-fix consumer proof for the delimiter correction
// in the frozen sources (roster_rename.go b8c114f1..., roster_atomic.go
// 89d77f95...). The conditional rename compares identity over the complete
// decoded row, so a CRLF delimiter and a genuine data CR are distinct, and
// the atomic publication preserves every unrelated raw byte. The expected
// rows, bytes and history lines below are literal fixture data, never
// derived from writer output. This is proof of the authorized fix, not a
// TDD red: the fix predates this slice and the sources are frozen, so the
// mutation discriminators are proposed and not executed here.
//
// The consumers are the existing public roster APIs only: RosterRenameIf,
// RosterLine and RosterRenameHint over synthetic agents.tsv files.
// RosterAppend cannot stage an unterminated final data CR (it terminates
// every line), so it is not exercised by these fixtures.

const (
	dja659OldName = "old"
	dja659NewName = "fresh"
)

// dja659Header is the 15-column production roster header.
const dja659Header = "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tburst\targs\teffort"

// dja659IdleRow is the unrelated row that must survive byte-identical.
const dja659IdleRow = "idle\tp-idle\tgrok\tworker\txai\t1\t/tmp/cwd\tnow\tgrok-4.7\ttask\tworker\tbuild\t\t\tlow"

// dja659OldRow is the row the consumers rename: 18 fields. Beyond the 15
// header columns sit empty columns, a space-padded field and unknown
// columns, so lossy trimming, whitespace normalization or column
// projection all break full-row identity. Pane and the opaque started
// tuple are literal, never time derived.
const dja659OldRow = "old\tp-old\tgrok\tworker\txai\t1\t/tmp/cwd\tst-659\tgrok-4.7\ttask\tworker\tbuild\t\t\thigh\t\t future \tpad"

// dja659FreshRow is the independent expected row after the rename: the
// name column replaced, every other byte retained.
const dja659FreshRow = "fresh\tp-old\tgrok\tworker\txai\t1\t/tmp/cwd\tst-659\tgrok-4.7\ttask\tworker\tbuild\t\t\thigh\t\t future \tpad"

// dja659History is the literal mapping line the writer must append.
const dja659History = `# soho-rename {"v":1,"from":"old","to":"fresh","pane":"p-old","started":"st-659"}`

func dja659WriteRoster(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "agents.tsv"), []byte(content), 0o666); err != nil {
		t.Fatal(err)
	}
}

func dja659ReadRoster(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "agents.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestDJA659DelimiterIdentity(t *testing.T) {
	t.Run("an unchanged row with a data CR at EOF renames and keeps that CR", func(t *testing.T) {
		dir := t.TempDir()
		// The last data field ends in a genuine CR and the file ends
		// there: an unterminated row with no trailing delimiter.
		dja659WriteRoster(t, dir, dja659Header+"\n"+dja659IdleRow+"\n"+dja659OldRow+"\r")
		if got := RosterRenameHint(dir, dja659OldName); got != "" {
			t.Fatalf("hint before the rename = %q, want empty (exact current name wins)", got)
		}
		// The decoded data row carries the trailing CR: it is data, not
		// a delimiter, so the expected row must include it.
		ok, err := RosterRenameIf(dir, dja659OldName, dja659NewName, dja659OldRow+"\r")
		if err != nil || !ok {
			t.Fatalf("renamed=%v err=%v", ok, err)
		}
		// Publication keeps the data CR and adds a CRLF delimiter after
		// it: the stored suffix is "pad\r\r\n", so later decoding still
		// yields the original data CR. The unrelated header and row
		// bytes stay exact, and the history line is the literal map.
		want := dja659Header + "\n" + dja659IdleRow + "\n" + dja659FreshRow + "\r\r\n" + dja659History + "\n"
		if got := dja659ReadRoster(t, dir); got != want {
			t.Fatalf("published bytes:\n%q\nwant:\n%q", got, want)
		}
		if got := RosterLine(dir, dja659NewName); got != dja659FreshRow+"\r" {
			t.Fatalf("RosterLine = %q, want %q", got, dja659FreshRow+"\r")
		}
		if got := RosterLine(dir, dja659OldName); got != "" {
			t.Fatalf("old name still readable after the rename: %q", got)
		}
		if got := RosterRenameHint(dir, dja659OldName); got != dja659NewName {
			t.Fatalf("hint after the rename = %q, want %q", got, dja659NewName)
		}
	})

	t.Run("an expectedRow missing the data CR refuses with byte-identical bytes", func(t *testing.T) {
		dir := t.TempDir()
		fixture := dja659Header + "\n" + dja659IdleRow + "\n" + dja659OldRow + "\r"
		dja659WriteRoster(t, dir, fixture)
		ok, err := RosterRenameIf(dir, dja659OldName, dja659NewName, dja659OldRow)
		if err != nil {
			t.Fatalf("refusal reported an error: %v", err)
		}
		if ok {
			t.Fatal("a lossy suffix comparison accepted an expectedRow that dropped the data CR")
		}
		// The registry and the history are byte-identical: nothing was
		// written.
		if got := dja659ReadRoster(t, dir); got != fixture {
			t.Fatalf("refusal changed roster bytes or history:\n%q\nwant:\n%q", got, fixture)
		}
	})

	t.Run("an expectedRow with an extra CR refuses with byte-identical bytes", func(t *testing.T) {
		dir := t.TempDir()
		fixture := dja659Header + "\n" + dja659IdleRow + "\n" + dja659OldRow + "\r"
		dja659WriteRoster(t, dir, fixture)
		ok, err := RosterRenameIf(dir, dja659OldName, dja659NewName, dja659OldRow+"\r\r")
		if err != nil {
			t.Fatalf("refusal reported an error: %v", err)
		}
		if ok {
			t.Fatal("a lossy suffix comparison accepted an expectedRow with an extra CR")
		}
		if got := dja659ReadRoster(t, dir); got != fixture {
			t.Fatalf("refusal changed roster bytes or history:\n%q\nwant:\n%q", got, fixture)
		}
	})

	t.Run("a CRLF-terminated source renames while unrelated CRLF bytes stay exact", func(t *testing.T) {
		dir := t.TempDir()
		comment := "# rows created on a windows host"
		fixture := dja659Header + "\r\n" + comment + "\r\n" + dja659IdleRow + "\r\n" + dja659OldRow + "\r\n"
		dja659WriteRoster(t, dir, fixture)
		// Here the CRLF is a delimiter: the decoded row carries no CR,
		// so the expected row is the bare row.
		ok, err := RosterRenameIf(dir, dja659OldName, dja659NewName, dja659OldRow)
		if err != nil || !ok {
			t.Fatalf("renamed=%v err=%v", ok, err)
		}
		// The CRLF header, the ordinary comment and the idle row keep
		// their exact CRLF bytes; only the name column of the renamed
		// row changes; the history line is the literal map.
		want := dja659Header + "\r\n" + comment + "\r\n" + dja659IdleRow + "\r\n" + dja659FreshRow + "\r\n" + dja659History + "\n"
		if got := dja659ReadRoster(t, dir); got != want {
			t.Fatalf("published bytes:\n%q\nwant:\n%q", got, want)
		}
		if got := RosterLine(dir, dja659NewName); got != dja659FreshRow {
			t.Fatalf("RosterLine = %q, want %q", got, dja659FreshRow)
		}
		if got := RosterLine(dir, "idle"); got != dja659IdleRow {
			t.Fatalf("unrelated row changed: %q", got)
		}
		if got := RosterRenameHint(dir, dja659OldName); got != dja659NewName {
			t.Fatalf("hint after the rename = %q, want %q", got, dja659NewName)
		}
	})
}
