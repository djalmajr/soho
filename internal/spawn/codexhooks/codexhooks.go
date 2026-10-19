// Package codexhooks recognizes and answers codex's hooks review screen
// (DJA-434) for spawn, dispatch, send and wait.
package codexhooks

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/text"
)

// codex (0.163.0) opens on a hooks review screen whenever the user's codex
// hooks are new or changed (DJA-434). herdr reports the agent ready while it
// is on screen, and the Enter of a typed prompt confirms the default option,
// "1. Review hooks", which opens the hooks table instead of the prompt. Two
// screens are recognized, each only as the active dialog — its footer is
// the last non-empty line of the visible screen — and with its title and
// its options or notice above that footer, so a brief or a reply that
// quotes the screen (codex's composer then comes below it) does not match:
//
//   - the menu: "Hooks need review" + "2. Trust all and continue" +
//     "enter confirm · esc skip";
//   - the table: "<n> hooks need review before they can run" + "t trust all".

var (
	codexHooksMenuOptionRE  = regexp.MustCompile(`^(?:› ?)?[0-9]+\. trust all and continue$`)
	codexHooksTableNoticeRE = regexp.MustCompile(`^(?:⚠ ?)?(?:[0-9]+ )?hooks? needs? review before (?:they|it) can run\.?$`)
	codexHooksCountRE       = regexp.MustCompile(`([0-9]+) hooks? (?:are|is) new or changed|([0-9]+) hooks? needs? review before`)
)

// codexHooksLines returns the screen's lines lowercased (ASCII), with the
// whitespace runs collapsed and a box border trimmed, for the matchers.
func codexHooksLines(screen string) []string {
	var out []string
	for _, line := range strings.Split(screen, "\n") {
		norm := strings.Join(strings.Fields(text.ASCIILower(line)), " ")
		norm = strings.TrimSpace(strings.Trim(norm, "│┃|"))
		if norm != "" {
			out = append(out, norm)
		}
	}
	return out
}

// IsMenu reports whether the screen is codex's active "Hooks need review"
// menu: the "enter confirm" footer is the last non-empty line of the
// visible screen, with the title line and the "Trust all and continue"
// option above it. A transcript that quotes the whole menu has codex's
// composer below it, so the quote does not match.
func IsMenu(screen string) bool {
	lines := codexHooksLines(screen)
	if len(lines) == 0 || !strings.HasPrefix(lines[len(lines)-1], "enter confirm") {
		return false
	}
	title, option := false, false
	for _, line := range lines[:len(lines)-1] {
		switch {
		case line == "hooks need review":
			title = true
		case codexHooksMenuOptionRE.MatchString(line):
			option = true
		}
	}
	return title && option
}

// IsTable reports whether the screen is codex's active hooks table: the
// "t trust all" footer is the last non-empty line of the visible screen,
// with the "hooks need review before they can run" notice above it.
func IsTable(screen string) bool {
	lines := codexHooksLines(screen)
	if len(lines) == 0 || !strings.HasPrefix(lines[len(lines)-1], "t trust all") {
		return false
	}
	for _, line := range lines[:len(lines)-1] {
		if codexHooksTableNoticeRE.MatchString(line) {
			return true
		}
	}
	return false
}

// IsReview reports whether the screen is either hooks review
// screen.
func IsReview(screen string) bool {
	return IsMenu(screen) || IsTable(screen)
}

// Count is the "<n>" of "<n> hooks are new or changed" (the menu)
// or "<n> hooks need review" (the table), or "the" when the screen shows no
// number.
func Count(screen string) string {
	for _, line := range codexHooksLines(screen) {
		if m := codexHooksCountRE.FindStringSubmatch(line); m != nil {
			if m[1] != "" {
				return m[1]
			}
			return m[2]
		}
	}
	return "the"
}

// codexHooksMenuOnTrustAll reports whether the menu's selected line — the
// one marked with › — is the "Trust all and continue" option.
func codexHooksMenuOnTrustAll(screen string) bool {
	return strings.Contains(codexHooksSelected(screen), "trust all and continue")
}

// codexHooksSelected is the menu line marked with ›, or "".
func codexHooksSelected(screen string) string {
	for _, line := range codexHooksLines(screen) {
		if strings.HasPrefix(line, "›") && codexHooksMenuOptionLine(line) {
			return line
		}
	}
	return ""
}

func codexHooksMenuOptionLine(line string) bool {
	rest := strings.TrimSpace(strings.TrimPrefix(line, "›"))
	return len(rest) > 2 && rest[0] >= '0' && rest[0] <= '9' && strings.Contains(rest, ". ")
}

// IO is how the approval reaches the agent: the key sender and
// the visible-screen reader of the caller (herdr agent send-keys/read for a
// local agent, the peer's machine-aware calls for a send), and the pause
// between the re-reads that wait for codex to redraw.
type IO struct {
	SendKey func(key string) bool
	Read    func() string
	Pause   time.Duration
}

// codexHooksRedrawReads bounds the re-reads after each key: codex redraws
// right away, and a screen that has not changed after them is taken as is.
const codexHooksRedrawReads = 4

// codexHooksMaxDowns bounds the down keys that move the menu's › to the
// "Trust all and continue" option (the option is the second of three).
const codexHooksMaxDowns = 3

// Approve answers codex's hooks review screen with "trust all"
// (hooks_review=trust). On the menu it sends down and re-reads the visible
// screen until the › line is "Trust all and continue" — at most 3 downs —
// and only then sends enter: an enter is never sent without the › seen on
// that option. On the table it sends t and, when the table is still on
// screen, esc. The final screen is re-read; ok is true only when no hooks
// review screen is visible any more. The caller prints the screen and takes
// its blocked path when ok is false.
func Approve(screen string, io IO) (string, bool) {
	switch {
	case IsMenu(screen):
		for downs := 0; !codexHooksMenuOnTrustAll(screen); downs++ {
			if downs == codexHooksMaxDowns || !IsMenu(screen) {
				return screen, false
			}
			before := codexHooksSelected(screen)
			if !io.SendKey("down") {
				return screen, false
			}
			screen = codexHooksReread(screen, io, func(s string) bool {
				return codexHooksSelected(s) != before || !IsMenu(s)
			})
		}
		// The last screen read is checked again right before the enter: a
		// redraw that lost the menu but kept the "› 2. Trust all and
		// continue" line ends the loop above without the menu.
		if !IsMenu(screen) || !codexHooksMenuOnTrustAll(screen) {
			return screen, false
		}
		if !io.SendKey("enter") {
			return screen, false
		}
	case IsTable(screen):
		if !io.SendKey("t") {
			return screen, false
		}
		screen = codexHooksReread(screen, io, func(s string) bool { return !IsTable(s) })
		if IsTable(screen) && !io.SendKey("esc") {
			return screen, false
		}
	default:
		return screen, false
	}
	screen = codexHooksReread(screen, io, func(s string) bool { return !IsReview(s) })
	return screen, !IsReview(screen)
}

// codexHooksReread re-reads the visible screen until done holds, up to
// codexHooksRedrawReads reads; it returns the last screen read (the given
// one when every read came back empty).
func codexHooksReread(screen string, io IO, done func(string) bool) string {
	for i := 0; i < codexHooksRedrawReads; i++ {
		if i > 0 && io.Pause > 0 {
			time.Sleep(io.Pause)
		}
		if s := io.Read(); s != "" {
			screen = s
			if done(s) {
				return screen
			}
		}
	}
	return screen
}

// TrustedLine is the one stderr line of an approval that took.
func TrustedLine(command, agent, count string) string {
	return fmt.Sprintf("%s: trusted %s codex hook(s) for '%s' (hooks_review=trust); they were new or changed in the user's codex hooks", command, count, agent)
}

// BlockedLine is the warning of a hooks review screen the command
// leaves on screen: hooks_review=block, or an approval that did not take.
func BlockedLine(command, agent, mode string) string {
	if mode == "trust" {
		return fmt.Sprintf("%s: '%s' is on codex's hooks review screen and the approval did not take (hooks_review=trust); nothing else was typed. Read the pane and answer it yourself (2. Trust all and continue)", command, agent)
	}
	return fmt.Sprintf("%s: '%s' is on codex's hooks review screen (its user codex hooks are new or changed) and hooks_review=block; nothing was typed. Set hooks_review=trust to let soho approve them, or answer it in the pane yourself (2. Trust all and continue)", command, agent)
}
