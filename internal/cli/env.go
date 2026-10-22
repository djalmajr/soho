package cli

import (
	"fmt"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/kinds"
	"github.com/djalmajr/soho/internal/platform"
	"os/exec"
	"runtime"
	"strings"
)

const envVersionTimeoutMs = 10000

func envRun(exe string, args []string, env platform.Env) platform.RunResult {
	return platform.RunCli(exe, args, platform.RunOptions{Env: env, TimeoutMs: envVersionTimeoutMs})
}
func versionOr(result platform.RunResult, fallback string) string {
	if result.NotFound || result.TimedOut || result.Error != "" || result.Status == nil || *result.Status != 0 {
		return fallback
	}
	return strings.TrimRight(result.Stdout, "\n")
}
func kindVersion(result platform.RunResult) string {
	if result.NotFound || result.TimedOut || result.Error != "" || result.Status == nil || *result.Status != 0 {
		return "?"
	}
	return strings.Split(result.Stdout, "\n")[0]
}
func uname(args ...string) string {
	for _, command := range []string{"/usr/bin/uname", "/bin/uname"} {
		out, err := exec.Command(command, args...).Output()
		if err == nil {
			return strings.TrimSpace(string(out))
		}
	}
	return "unknown"
}
func cmdEnv(ctx *core.Config, env platform.Env, cwd string) {
	lines := []string{}
	lines = append(lines, "soho: "+versionOr(envRun("git", []string{"-C", platform.SkillDir(env), "log", "-1", "--format=%h %cs"}, env), strings.TrimPrefix(strings.TrimSpace(versionText(version, vcsRevision())), "soho ")))
	lines = append(lines, "herdr: "+versionOr(envRun("herdr", []string{"--version"}, env), "unknown"))
	osType := uname("-s")
	if osType == "unknown" {
		osType = map[string]string{"darwin": "Darwin", "linux": "Linux", "windows": "Windows_NT"}[runtime.GOOS]
	}
	osRelease := uname("-r")
	osMachine := uname("-m")
	if osMachine == "unknown" {
		osMachine = runtime.GOARCH
	}
	lines = append(lines, fmt.Sprintf("os: %s %s (%s)", osType, osRelease, osMachine))
	lines = append(lines, "runtime: go "+runtime.Version())
	skillDir, skillOrigin := platform.SkillDirSource(env)
	lines = append(lines, "skill dir: "+skillDir+" ("+skillOrigin+")")
	for _, kind := range kinds.KnownKinds {
		exe := kinds.KindExe(kind)
		if _, ok := platform.FindExecutable(exe, env, platform.Current()); !ok {
			continue
		}
		lines = append(lines, "kind "+kind+": "+kindVersion(envRun(exe, []string{"--version"}, env)))
	}
	lines = append(lines, fmt.Sprintf("config: layout=%s reuse_workers=%s approvals=%s auto_approve=%s brief_lint=%s", core.Cfg(ctx, "layout", "split", env), core.Cfg(ctx, "reuse_workers", "on", env), core.Cfg(ctx, "approvals", "auto", env), core.Cfg(ctx, "auto_approve", "off", env), core.Cfg(ctx, "brief_lint", "warn", env)))
	_, _ = fmt.Fprintln(platform.Stdout, strings.Join(lines, "\n"))
	_ = cwd
}
