package job

import (
	"regexp"

	"github.com/djalmajr/soho/internal/text"
)

var (
	// urlUserinfoRE is the userinfo of a URL (scheme://user:pass@host and
	// scheme://token@host). The userinfo runs to the last '@' before the
	// first '/', so a password holding an '@' is covered whole.
	urlUserinfoRE = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*)://[^/\s]+@`)
	// githubTokenRE is a GitHub token: ghp_, gho_, ghs_, ghu_ or ghr_ and
	// github_pat_, each followed by 20 or more word characters.
	githubTokenRE = regexp.MustCompile(`(?:gh[posur]_|github_pat_)[A-Za-z0-9_]{20,}`)
)

// redactDetail removes credentials from the diagnostic a failed subprocess
// step keeps for the job's friction log: the URL userinfo, the GitHub
// tokens, then the shared text.RedactSecrets patterns. It runs before the
// detail is cut, so the cut never leaves half a token behind.
func redactDetail(s string) string {
	s = urlUserinfoRE.ReplaceAllString(s, "$1://[redacted]@")
	s = githubTokenRE.ReplaceAllString(s, "[redacted]")
	return text.RedactSecrets(s)
}
