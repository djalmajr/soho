package spawn

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/djalmajr/soho/internal/kinds"
	"github.com/djalmajr/soho/internal/platform"
)

// startShellNotFoundRE is a shell "command not found" line naming the
// word the shell could not run: the zsh form "zsh: command not found:
// w" and the bash form "bash: w: command not found", as a whole line.
var startShellNotFoundRE = regexp.MustCompile(`^(?:zsh: command not found: (\S+)|bash: (\S+): command not found)$`)

// lastShellNotFoundWord is the word named by the last shell
// "command not found" line of the screen, or "". The last line wins:
// the command this spawn typed is the newest thing the shell rejected,
// and an earlier line is a previous occupant's.
func lastShellNotFoundWord(scr string) string {
	word := ""
	for _, line := range strings.Split(scr, "\n") {
		m := startShellNotFoundRE.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		word = m[1]
		if word == "" {
			word = m[2]
		}
	}
	return word
}

// startDiagnoseShellNotFound names, on its own stderr line, the two
// command-not-found causes a failed spawn can have produced itself,
// read from the pane's screen: the kind's executable with its start
// consumed (an interactive prompt when the shell opens — oh-my-zsh's
// update question, for example — read the first key the spawn typed),
// or the executable missing from the shell's PATH. It prints the line
// before the failure's DieFriction and changes nothing else: the
// friction message, the exit 4, the pane close and its three outcomes
// stay exactly as they are.
func startDiagnoseShellNotFound(scr, kind string) {
	word := lastShellNotFoundWord(scr)
	if word == "" {
		return
	}
	exe := kinds.KindExe(kind)
	switch {
	case word == exe:
		_, _ = fmt.Fprintf(platform.Stderr, "soho: spawn: the pane's shell cannot find '%s' (command not found): it is not on that shell's PATH\n", exe)
	case strings.HasSuffix(exe, word):
		_, _ = fmt.Fprintf(platform.Stderr, "soho: spawn: the pane's shell consumed the start of the agent command ('%s' instead of '%s'): an interactive prompt when the shell opens (for example oh-my-zsh's update question) read a key; answer or turn off that prompt on this machine, then spawn again\n", word, exe)
	}
}
