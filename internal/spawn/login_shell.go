package spawn

import (
	"os"
	"path/filepath"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/startup"
)

const (
	loginShellNeedsShell = "spawn: login_shell=on needs SHELL to be an absolute path to zsh, bash, ksh or sh"
	loginShellOnWindows  = "spawn: login_shell=on is not supported on Windows"
	loginShellOpencode   = "spawn: login_shell=on is not supported for opencode workers"
)

// loginShellSupported lists the base names of the shells whose `-l -c` pair
// runs the profile and then the command.
var loginShellSupported = map[string]bool{"zsh": true, "bash": true, "ksh": true, "sh": true}

// resolveLoginShell returns the absolute path of the login shell a native
// worker starts through, or "" when it starts directly. Only login_shell=on
// on transport=hc has an effect: off, an invalid value (it applies as off, like
// trust_cwd) and the legacy transport return "" without a word. With the key
// on, a platform, kind or SHELL that cannot honor it ends the spawn with exit
// 2 here, before any terminal exists.
func resolveLoginShell(ctx *core.Config, env platform.Env, kind string) string {
	if env.Get("SOHO_TRANSPORT") != "hc" || core.Cfg(ctx, "login_shell", "off", env) != "on" {
		return ""
	}
	if startup.CurrentPlatform() == "win32" {
		core.DieFriction(loginShellOnWindows, 2, "", "spawn")
	}
	if kind == "opencode" {
		core.DieFriction(loginShellOpencode, 2, "", "spawn")
	}
	shell := env.Get("SHELL")
	if !filepath.IsAbs(shell) || !loginShellSupported[filepath.Base(shell)] {
		core.DieFriction(loginShellNeedsShell, 2, "", "spawn")
	}
	info, err := os.Stat(shell)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		core.DieFriction(loginShellNeedsShell, 2, "", "spawn")
	}
	return shell
}
