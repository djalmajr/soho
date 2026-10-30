package core

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/platform"
	herdrtext "github.com/djalmajr/soho/internal/text"
	"github.com/djalmajr/soho/internal/transport"
)

var taskContractHeading = regexp.MustCompile(`^(goal|owned files|owned|scope|forbidden|non-goals|constraints|report|expected result|acceptance criteria|acceptance|decisions already made|context|sources)[ \t]*$`)
var taskBriefPrefix = regexp.MustCompile(`^Brief[ \t]*(—|:|-)[ \t]*`)
var atomicWriteTask = platform.AtomicWrite

func BriefTask(file string) string {
	raw, err := platform.ReadTextFile(file)
	if err == nil {
		for _, line := range strings.Split(raw, "\n") {
			if trimJSWhitespace(line) == "" {
				continue
			}
			line = strings.ReplaceAll(line, "\r", "")
			if !strings.HasPrefix(line, "# ") {
				break
			}
			title := strings.TrimPrefix(line, "# ")
			if taskContractHeading.MatchString(herdrtext.ASCIILower(title)) {
				title = ""
			}
			title = taskBriefPrefix.ReplaceAllString(title, "")
			if title != "" {
				return title
			}
			break
		}
	}
	return strings.TrimSuffix(filepath.Base(file), ".md")
}

func PaneTaskTitle(dir, agent string, title *string, env platform.Env) {
	line := RosterLine(dir, agent)
	if line == "" {
		return
	}
	f := strings.Split(line, "\t")
	if len(f) < 2 || f[1] == "" {
		return
	}
	_ = herdr.New(env).Window.Title(nil, f[1], title, transport.Call{})
}

func MarkTaskDone(dir, agent string, env platform.Env) error {
	file := filepath.Join(dir, "task-"+agent)
	text, err := platform.ReadTextFile(file)
	if err != nil {
		return nil
	}
	text = strings.TrimRight(text, "\n")
	if strings.HasSuffix(text, " ✓") {
		return nil
	}
	text += " ✓\n"
	if err := atomicWriteTask(file, text); err != nil {
		return err
	}
	title := strings.TrimSuffix(text, "\n")
	PaneTaskTitle(dir, agent, &title, env)
	return nil
}
