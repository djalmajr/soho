package spawn

import (
	"strings"
	"sync"

	"github.com/djalmajr/soho/internal/platform"
)

var codexHelp = func(env platform.Env, cwd string) platform.RunResult {
	return platform.RunCli("codex", []string{"--help"}, platform.RunOptions{Env: env, Cwd: cwd, TimeoutMs: 10000})
}

var codexNoDaemonOnce sync.Once
var codexNoDaemonSupported bool

// CodexSupportsNoDaemon checks only CLI help, at most once per soho process.
func CodexSupportsNoDaemon(env platform.Env, cwd string) bool {
	codexNoDaemonOnce.Do(func() {
		result := codexHelp(env, cwd)
		// A help that hit the deadline can still exit 0 (it handled the
		// signal): only an error-free, finished run counts.
		codexNoDaemonSupported = result.Status != nil && *result.Status == 0 && result.Error == "" && !result.TimedOut && strings.Contains(result.Stdout, "--no-daemon")
	})
	return codexNoDaemonSupported
}
