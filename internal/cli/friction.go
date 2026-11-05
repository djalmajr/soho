package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/stats"
)

type frictionSummaryRow struct {
	count   int
	level   string
	command string
}

func fieldOr(fields []string, i int, fallback string) string {
	if i < len(fields) {
		return fields[i]
	}
	return fallback
}

func frictionLineMatches(since time.Time, hasSince bool, level, command, agent string, fields []string) bool {
	if hasSince {
		ts, ok := stats.ParseSince(fields[0])
		if !ok || ts.Before(since) {
			return false
		}
	}
	if level == "error" {
		if !strings.HasPrefix(fields[1], "error(") {
			return false
		}
	} else if level != "" && fields[1] != level {
		return false
	}
	if command != "" && fields[2] != command {
		return false
	}
	if agent != "" && !strings.Contains(fields[3], "'"+agent+"'") {
		return false
	}
	return true
}

func cmdFriction(argv []string, ctx *core.Config, env platform.Env, cwd string) {
	var (
		sinceRaw, level, command, agent string
		summary, patterns, jsonOut      bool
		filterFlag                      string
	)
	sub, brief, key := "", "", ""
	keySet := false
	words := []string{}
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch {
		case sub == "" && (a == "--since" || a == "--level" || a == "--command" || a == "--agent"):
			if i+1 >= len(argv) || strings.HasPrefix(argv[i+1], "--") {
				platform.DieFriction("friction: "+a+" expects a value", 2)
			}
			switch a {
			case "--since":
				sinceRaw = argv[i+1]
			case "--level":
				level = argv[i+1]
			case "--command":
				command = argv[i+1]
			case "--agent":
				agent = argv[i+1]
			}
			if filterFlag == "" {
				filterFlag = a
			}
			i++
		case sub == "" && a == "--summary":
			summary = true
			if filterFlag == "" {
				filterFlag = a
			}
		case sub == "" && a == "--patterns":
			patterns = true
			if filterFlag == "" {
				filterFlag = a
			}
		case sub == "" && a == "--json":
			jsonOut = true
			if filterFlag == "" {
				filterFlag = a
			}
		case a == "--brief":
			if i+1 >= len(argv) || strings.HasPrefix(argv[i+1], "--") {
				platform.DieFriction("friction add: --brief expects a path", 2)
			}
			brief = argv[i+1]
			i++
		case a == "--key":
			if i+1 >= len(argv) || strings.HasPrefix(argv[i+1], "--") {
				platform.DieFriction("friction add: --key expects a value", 2)
			}
			key = argv[i+1]
			keySet = true
			i++
		case strings.HasPrefix(a, "--"):
			platform.DieFriction("friction: unknown option "+a, 2)
		case sub == "" && len(words) == 0:
			sub = a
		default:
			words = append(words, a)
		}
	}
	if sub == "" && patterns && summary {
		platform.DieFriction("friction: --patterns and --summary cannot be combined", 2)
	}
	if sub == "" && jsonOut && !patterns {
		platform.DieFriction("friction: --json needs --patterns", 2)
	}
	if keySet && sub != "add" {
		platform.DieFriction("friction: unknown option --key", 2)
	}
	if sub == "fixed" && brief != "" {
		platform.DieFriction("friction: unknown option --brief", 2)
	}
	if sub != "" && (sinceRaw != "" || level != "" || command != "" || agent != "" || summary || patterns || jsonOut) {
		platform.DieFriction("friction: unknown option "+filterFlag, 2)
	}
	sd := core.StateDir(ctx, env, cwd)
	logPath := filepath.Join(sd, "friction.log")
	if sub == "" {
		filtered := sinceRaw != "" || level != "" || command != "" || agent != "" || summary || patterns || jsonOut
		if !filtered {
			st, err := os.Stat(logPath)
			if err != nil || st.Size() == 0 {
				fmt.Fprintf(platform.Stdout, "no friction recorded under %s\n", logPath)
				return
			}
			fmt.Fprintf(platform.Stdout, "friction log (%s): timestamp, level, command, message\n", logPath)
			if text, readErr := platform.ReadTextFile(logPath); readErr == nil {
				_, _ = platform.Stdout.Write([]byte(text))
			}
			return
		}
		var since time.Time
		hasSince := sinceRaw != ""
		if hasSince {
			var ok bool
			since, ok = stats.ParseSince(sinceRaw)
			if !ok {
				platform.DieFriction("friction: --since expects AAAA-MM-DD or an ISO date (got '"+sinceRaw+"')", 2)
			}
		}
		if level != "" && level != "warning" && level != "note" && level != "error" {
			platform.DieFriction("friction: --level expects warning, note or error (got '"+level+"')", 2)
		}
		text, readErr := platform.ReadTextFile(logPath)
		if readErr != nil {
			if !patterns {
				fmt.Fprintf(platform.Stdout, "no friction matches under %s\n", logPath)
				return
			}
			text = ""
		}
		if patterns {
			rows := frictionPatterns(text, since, hasSince, level, command, agent)
			if jsonOut {
				if len(rows) == 0 {
					rows = []frictionPatternRow{}
				}
				b, _ := json.Marshal(rows)
				fmt.Fprintf(platform.Stdout, "%s\n", b)
				return
			}
			if len(rows) == 0 {
				return
			}
			fmt.Fprintln(platform.Stdout, "count  after  key  last_seen  fixed_by  pattern")
			for _, row := range rows {
				fmt.Fprintf(platform.Stdout, "%d  %s  %s  %s  %s  %s\n", row.Count, row.After, row.Key, row.LastSeen, row.FixedBy, row.Pattern)
			}
			return
		}
		lines := strings.Split(text, "\n")
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		kept := []string{}
		groups := map[[2]string]int{}
		for _, line := range lines {
			if line == "" {
				continue
			}
			fields := strings.SplitN(line, "\t", 4)
			if len(fields) < 4 {
				if !summary {
					continue
				}
				groups[[2]string{"?", fieldOr(fields, 2, "?")}]++
				continue
			}
			if !frictionLineMatches(since, hasSince, level, command, agent, fields) {
				continue
			}
			if summary {
				groups[[2]string{fields[1], fields[2]}]++
			} else {
				kept = append(kept, line)
			}
		}
		if summary {
			rows := make([]frictionSummaryRow, 0, len(groups))
			for key, count := range groups {
				rows = append(rows, frictionSummaryRow{count: count, level: key[0], command: key[1]})
			}
			sort.Slice(rows, func(i, j int) bool {
				if rows[i].count != rows[j].count {
					return rows[i].count > rows[j].count
				}
				if rows[i].level != rows[j].level {
					return rows[i].level < rows[j].level
				}
				return rows[i].command < rows[j].command
			})
			if len(rows) == 0 {
				fmt.Fprintf(platform.Stdout, "no friction matches under %s\n", logPath)
				return
			}
			fmt.Fprintln(platform.Stdout, "count  level  command")
			for _, row := range rows {
				fmt.Fprintf(platform.Stdout, "%d  %s  %s\n", row.count, row.level, row.command)
			}
			return
		}
		if len(kept) == 0 {
			fmt.Fprintf(platform.Stdout, "no friction matches under %s\n", logPath)
			return
		}
		fmt.Fprintf(platform.Stdout, "friction log (%s): timestamp, level, command, message\n", logPath)
		for _, line := range kept {
			fmt.Fprintln(platform.Stdout, line)
		}
		return
	}
	if sub != "add" && sub != "fixed" {
		platform.DieFriction(fmt.Sprintf("friction: unknown subcommand '%s'", sub), 2)
	}
	if sub == "fixed" {
		if len(words) == 0 {
			platform.DieFriction("friction fixed: give the key and what resolved it", 2)
		}
		fixedKey := words[0]
		fixedText := strings.Join(words[1:], " ")
		if fixedText == "" {
			platform.DieFriction("friction fixed: give what resolved it", 2)
		}
		if !frictionKeyExists(logPath, fixedKey) {
			platform.DieFriction("friction: no friction pattern with key "+fixedKey, 2)
		}
		line := fmt.Sprintf("%s\tfixed\tfriction\t%s\n", core.FrictionISO(platform.Now()), core.FrictionSafe(fixedKey+" "+fixedText))
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			platform.DieFriction(fmt.Sprintf("friction fixed: could not write %s (%v)", logPath, err), 4)
		}
		_, writeErr := f.WriteString(line)
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			platform.DieFriction(fmt.Sprintf("friction fixed: could not write %s", logPath), 4)
		}
		fmt.Fprintln(platform.Stdout, "recorded")
		return
	}
	if keySet && !frictionSlugRe.MatchString(key) {
		platform.DieFriction(fmt.Sprintf("friction add: --key must match ^[a-z0-9][a-z0-9-]{0,40}$ (got '%s')", key), 2)
	}
	message := strings.Join(words, " ")
	if message == "" {
		platform.DieFriction("friction add: give the text to record", 2)
	}
	if brief != "" {
		message += " (brief: " + brief + ")"
	}
	if keySet {
		message = "[" + key + "] " + message
	}
	line := fmt.Sprintf("%s\tnote\tfriction\t%s\n", core.FrictionISO(platform.Now()), core.FrictionSafe(message))
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		platform.DieFriction(fmt.Sprintf("friction add: could not write %s (%v)", logPath, err), 4)
	}
	_, writeErr := f.WriteString(line)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		platform.DieFriction(fmt.Sprintf("friction add: could not write %s", logPath), 4)
	}
	fmt.Fprintln(platform.Stdout, "recorded")
}

// friction pattern model (DJA-684 fatia C). The log format is untouched;
// the pattern of a line is computed at read time from its message: control
// characters are removed first (the same printable filter soho validate
// uses), a leading "[<slug>] " names the key and the rest is the template,
// otherwise the key is the first eight hex characters of the sha256 of the
// masked template. Masking turns quoted strings into <q>, absolute paths
// into <path> and standalone numbers into <n>, in that order.
var (
	frictionSlugRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)
	frictionKeyPrefixRe = regexp.MustCompile(`^\[(\S+)\] (.*)$`)
)

func frictionTemplate(message string) (string, string) {
	msg := printable(message)
	if m := frictionKeyPrefixRe.FindStringSubmatch(msg); m != nil && frictionSlugRe.MatchString(m[1]) {
		return m[1], maskFrictionTemplate(m[2])
	}
	tpl := maskFrictionTemplate(msg)
	sum := sha256.Sum256([]byte(tpl))
	return hex.EncodeToString(sum[:])[:8], tpl
}

func maskFrictionTemplate(s string) string {
	s = maskFrictionQuotes(s)
	s = maskFrictionPaths(s)
	return maskFrictionNumbers(s)
}

// maskFrictionQuotes replaces closed quoted strings with <q>. A single quote
// inside a word is an apostrophe: it neither opens nor closes one, so can't and
// don't in one message do not swallow the words between them.
func maskFrictionQuotes(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		if c == '"' || c == '`' || (c == '\'' && (i == 0 || !isWordByte(s[i-1]))) {
			if end := frictionQuoteEnd(s, i+1, c); end >= 0 {
				b.WriteString("<q>")
				i = end + 1
				continue
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

func frictionQuoteEnd(s string, from int, quote byte) int {
	for j := from; j < len(s); j++ {
		if s[j] != quote {
			continue
		}
		if quote == '\'' && j+1 < len(s) && isWordByte(s[j+1]) {
			continue
		}
		return j
	}
	return -1
}

func isWordByte(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// maskFrictionPaths replaces paths with <path>, from the path start up to the
// next space: absolute ones (/…, ~/…, C:\…), and relative ones that are clearly
// paths (isFrictionRelativePath). A path starts only where the previous
// character is not a word character, so the middle of a/b keeps its slash;
// quoted paths are already <q> when this runs.
func maskFrictionPaths(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if isFrictionPathStart(s, i) {
			j := i
			for j < len(s) && s[j] != ' ' && s[j] != '\t' {
				j++
			}
			b.WriteString("<path>")
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// isFrictionRelativePath reports whether the token starting at i is a relative
// path: it starts with a dot and has a separator, or it has two separators. A
// single one is not enough: and/or is a word, and so is a/b.
func isFrictionRelativePath(s string, i int) bool {
	if i > 0 && s[i-1] != ' ' && s[i-1] != '\t' {
		return false
	}
	end := i
	for end < len(s) && s[end] != ' ' && s[end] != '\t' {
		end++
	}
	token := s[i:end]
	separators := strings.Count(token, "/") + strings.Count(token, "\\")
	return separators >= 2 || (separators == 1 && strings.HasPrefix(token, "."))
}

func isFrictionPathStart(s string, i int) bool {
	if i > 0 && isWordByte(s[i-1]) {
		return false
	}
	if isFrictionRelativePath(s, i) {
		return true
	}
	switch {
	case s[i] == '/':
		return true
	case strings.HasPrefix(s[i:], "~/"):
		return true
	case i+2 < len(s) &&
		((s[i] >= 'a' && s[i] <= 'z') || (s[i] >= 'A' && s[i] <= 'Z')) &&
		s[i+1] == ':' && (s[i+2] == '\\' || s[i+2] == '/'):
		return true
	}
	return false
}

// maskFrictionNumbers replaces runs of digits with <n> unless the run sits
// inside a word: a run preceded by a letter or a hyphen (sha256, DJA-684)
// keeps its digits, while a number glued to a unit (45s) is masked.
func maskFrictionNumbers(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] >= '0' && s[i] <= '9' {
			j := i
			for j < len(s) && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			if i > 0 && (isWordByte(s[i-1]) || s[i-1] == '-') {
				b.WriteString(s[i:j])
			} else {
				b.WriteString("<n>")
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

type frictionPatternRow struct {
	Count    int    `json:"count"`
	After    string `json:"after"`
	Key      string `json:"key"`
	LastSeen string `json:"last_seen"`
	FixedBy  string `json:"fixed_by"`
	Pattern  string `json:"pattern"`
}

// frictionPatterns groups the log lines by pattern: fixed lines are not
// occurrences and are read whatever the caller's since/level/command/agent
// filter is (they are the key's metadata, so after and fixed_by stay
// meaningful with a filter that would hide them). For every key, after
// counts the filtered occurrences that come after the last fixed line of
// that key (file order, so the last fixed wins and two fixed lines in the
// same second keep the file order), and fixed_by is its text.
func frictionPatterns(text string, since time.Time, hasSince bool, level, command, agent string) []frictionPatternRow {
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	type patternState struct {
		count    int
		first    int
		lastSeen string
		template string
		occ      []int
	}
	states := map[string]*patternState{}
	lastFixed := map[string]int{}
	fixedText := map[string]string{}
	for i, line := range lines {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 4)
		if len(fields) < 4 {
			continue
		}
		if fields[1] == "fixed" {
			parts := strings.SplitN(fields[3], " ", 2)
			lastFixed[parts[0]] = i
			if len(parts) == 2 {
				fixedText[parts[0]] = parts[1]
			}
			continue
		}
		if !frictionLineMatches(since, hasSince, level, command, agent, fields) {
			continue
		}
		key, template := frictionTemplate(fields[3])
		st := states[key]
		if st == nil {
			st = &patternState{first: i, template: template}
			states[key] = st
		}
		st.count++
		st.lastSeen = fields[0]
		st.occ = append(st.occ, i)
	}
	keys := make([]string, 0, len(states))
	for k := range states {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := states[keys[i]], states[keys[j]]
		if a.count != b.count {
			return a.count > b.count
		}
		return a.first < b.first
	})
	rows := make([]frictionPatternRow, 0, len(keys))
	for _, k := range keys {
		st := states[k]
		row := frictionPatternRow{Count: st.count, After: "-", Key: printable(k), LastSeen: printable(st.lastSeen), FixedBy: "-", Pattern: st.template}
		if fi, ok := lastFixed[k]; ok {
			after := 0
			for _, idx := range st.occ {
				if idx > fi {
					after++
				}
			}
			row.After = strconv.Itoa(after)
			row.FixedBy = printable(fixedText[k])
		}
		rows = append(rows, row)
	}
	return rows
}

// frictionKeyExists reports whether the key names a pattern of the log: an
// occurrence line whose key, as the grouping computes it, is this one. A fixed
// line is metadata and names no pattern.
func frictionKeyExists(logPath, key string) bool {
	text, err := platform.ReadTextFile(logPath)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 4)
		if len(fields) < 4 {
			continue
		}
		if fields[1] == "fixed" {
			continue
		}
		pk, _ := frictionTemplate(fields[3])
		if pk == key {
			return true
		}
	}
	return false
}
