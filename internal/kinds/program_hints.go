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
// (KnownKinds/KindExe). Unknown or empty hints return the empty kind; the
// helper never parses a shell command, inspects later argv, infers a
// model, name or role, or keeps a copied alias table. Canonicalizing a
// program name does not establish process identity.
func KindFromProgramHints(hints []string) string {
	for _, hint := range hints {
		if kind := kindFromProgramHint(hint); kind != "" {
			return kind
		}
	}
	return ""
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
	return ""
}
