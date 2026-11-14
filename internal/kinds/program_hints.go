package kinds

import (
	"path/filepath"
	"strings"
)

// backslash is the Windows path separator, written as an escape so the
// normalization never depends on raw-string quoting.
const backslash = "\u005C"

// KindFromProgramHints resolves the canonical kind from live program hints
// (for example a status app name and the argv[0] basename the transport
// already exposes): the first recognized hint in source order wins. Each
// hint takes the executable basename after normalizing both path
// separators, folds case, removes at most one .exe, .cmd or .bat suffix,
// and matches the catalog's kind and executable names exactly
// (KnownKinds/KindExe). When that fails, the same normalized name is
// looked up exactly in one small table of reporter-app names
// (reporterAppKinds), for a program whose self-reported app name is not
// its kind. Unknown or empty hints return the empty kind; the helper
// never parses a shell command, inspects later argv, infers a model, name
// or role, or matches by prefix, substring, glob or pattern.
// Canonicalizing a name, including through that table, does not establish
// process identity: a reporter app is self-reported by the terminal's
// program.
func KindFromProgramHints(hints []string) string {
	for _, hint := range hints {
		if kind := kindFromProgramHint(hint); kind != "" {
			return kind
		}
	}
	return ""
}

// reporterAppKinds maps an exact normalized reporter-app name to its
// catalog kind. Claude Code reports the app name claude-code while its
// executable is a version-named file, so neither equals the kind. Every
// value must be a member of KnownKinds; the table is read-only package
// state and nothing mutates it. Add an entry only for an app name that a
// terminal server was observed to report.
var reporterAppKinds = map[string]string{
	"claude-code": "claude",
}

func kindFromProgramHint(hint string) string {
	name := filepath.Base(strings.ReplaceAll(hint, backslash, "/"))
	name = strings.ToLower(name)
	for _, suffix := range []string{".exe", ".cmd", ".bat"} {
		if strings.HasSuffix(name, suffix) {
			name = strings.TrimSuffix(name, suffix)
			break
		}
	}
	for _, kind := range KnownKinds {
		if name == kind || name == KindExe(kind) {
			return kind
		}
	}
	return reporterAppKinds[name]
}
