package doctor

import (
	"fmt"
	"os"
	"runtime"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// doctorCopiesDir warns only about the default placement, before any copies are made.
func doctorCopiesDir(ctx *core.Config, env platform.Env, s *Say) {
	if runtime.GOOS == "windows" || core.Cfg(ctx, "copies_dir", "", env) != "" {
		return
	}
	tmp := env.Get("TMPDIR")
	if tmp == "" {
		tmp = os.TempDir()
	}
	info, err := os.Stat(tmp)
	if err != nil || info.Mode().Perm()&0002 == 0 {
		return
	}
	mode := uint32(info.Mode().Perm())
	if info.Mode()&os.ModeSticky != 0 {
		mode |= 01000
	}
	if info.Mode()&os.ModeSetgid != 0 {
		mode |= 02000
	}
	if info.Mode()&os.ModeSetuid != 0 {
		mode |= 04000
	}
	s.Warning(fmt.Sprintf("TMPDIR %s is world-writable (mode %o): mutation copies there can fail a project's trusted-path checks; set copies_dir to a private directory, for example ~/.cache/soho/copies", tmp, mode))
}
