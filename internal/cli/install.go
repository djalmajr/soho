package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/install"
	"github.com/djalmajr/soho/internal/platform"
)

// installTimeout bounds the whole install command (both downloads and the
// local replacement), on top of the core's per-request timeout.
const installTimeout = 10 * time.Minute

// verifyVersionTimeout bounds the opt-in post-install --version probe,
// the start of the installed binary included; tests replace it.
var verifyVersionTimeout = 10 * time.Second

// verifyVersionWaitDelay gives the child a grace period to exit after the
// timeout kill before the wait gives up on it. versionProbeOutputLimit
// caps the probe's captured output.
const (
	verifyVersionWaitDelay  = 5 * time.Second
	versionProbeOutputLimit = 4096
)

// userPathStoreFactory is the registry seam the --add-to-path flag resolves
// through; tests replace it with an in-memory store. Production is the
// platform-specific install.DefaultUserPathStore.
var userPathStoreFactory = install.DefaultUserPathStore

// cmdInstall is the portable installer: it routes to the reviewed
// internal/install core (the only checksum/download implementation) and
// owns what the core leaves to the root CLI — flag parsing and
// pre-effect refusal, environment resolution (--dir first, then
// SOHO_INSTALL_DIR, then the OS default), the bounded
// context/client, and the PATH advice. The default install never edits a
// shell profile or the registry and never executes the installed bytes;
// the optional flags opt in explicitly: --add-to-path appends the install
// dir to the Windows user PATH (old installer's -AddToPath behavior, raw
// advapi32, rejected before any effect elsewhere) and --verify-version
// runs the verified binary's --version once under a bounded probe.
func cmdInstall(args []string, env platform.Env) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	version := fs.String("version", "", "release tag to install (v-prefixed); empty installs the latest release")
	dir := fs.String("dir", "", "install directory; default: $SOHO_INSTALL_DIR, or $HOME/.local/bin on POSIX / %LOCALAPPDATA%/Programs/soho on Windows")
	addToPath := fs.Bool("add-to-path", false, "append the install directory to the Windows user PATH (opt-in, like the old -AddToPath; refused on other OSes before any effect)")
	verifyVersion := fs.Bool("verify-version", false, "run the installed binary's --version once and print the result (opt-in; the default install never executes it)")
	if err := fs.Parse(args); err != nil {
		platform.Die("install: "+err.Error(), 2)
	}
	if fs.NArg() > 0 {
		platform.Die(fmt.Sprintf("install: unexpected arguments: %v", fs.Args()), 2)
	}
	if *addToPath && runtime.GOOS != "windows" {
		platform.Die("install: --add-to-path is only supported on Windows", 2)
	}
	dirPath, err := install.ResolveInstallDir(*dir, env.Get)
	if err != nil {
		platform.Die(err.Error(), 2)
	}
	abs, err := filepath.Abs(dirPath)
	if err != nil {
		platform.Die("install: resolving install directory: "+err.Error(), 2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), installTimeout)
	defer cancel()
	res, err := install.Run(ctx, install.Options{
		Version:     *version,
		InstallDir:  abs,
		ReleaseBase: env.Get(install.EnvReleaseBase),
	})
	if err != nil {
		platform.Die(err.Error(), 1)
	}
	fmt.Fprintf(platform.Stdout, "installed %s\n", res.Destination)
	if line := movedAsideLine(res); line != "" {
		fmt.Fprintln(platform.Stdout, line)
	}
	fmt.Fprintf(platform.Stdout, "sha256 %s\n", res.Digest)
	fmt.Fprintf(platform.Stdout, "release %s\n", res.ReleaseURL)
	if !pathContains(env.Get("PATH"), abs) {
		fmt.Fprintf(platform.Stdout, "PATH: %s is not in PATH; add it to your shell profile (no automatic edits)\n", abs)
	}
	if *addToPath {
		store, err := userPathStoreFactory()
		if err != nil {
			platform.Die(err.Error(), 1)
		}
		changed, err := install.UpdateUserPathValue(store, abs)
		if err != nil {
			platform.Die(err.Error(), 1)
		}
		if changed {
			fmt.Fprintf(platform.Stdout, "user PATH: added %s\n", abs)
		} else {
			fmt.Fprintf(platform.Stdout, "user PATH: unchanged (already present)\n")
		}
	}
	if *verifyVersion {
		line, err := verifyInstalledVersion(res.Destination, env)
		if err != nil {
			platform.Die(err.Error(), 1)
		}
		fmt.Fprintf(platform.Stdout, "version check: %s\n", line)
	}
	return 0
}

// verifyInstalledVersion runs the just-installed binary's --version once
// and returns its bounded first-line output. The destination's path and
// native header are validated through the shared platform.LauncherPath
// with SOHO_BIN pointed at it before anything executes (a bad
// path/header Dies 2). The child inherits the explicit env clone (never
// the process environment), runs with no shell, a bounded timeout and
// WaitDelay, and bounded captured output: both stdout and stderr go
// through draining writers, so chatty output is copied to completion
// while the capture itself stays bounded. Failures (nonzero exit,
// timeout) propagate clearly; the verified binary is never deleted.
func verifyInstalledVersion(dest string, env platform.Env) (string, error) {
	childEnv := env.Clone()
	childEnv["SOHO_BIN"] = dest
	resolved := platform.LauncherPath(childEnv)
	argv := make([]string, 0, len(childEnv))
	for k, v := range childEnv {
		argv = append(argv, k+"="+v)
	}
	sort.Strings(argv)
	ctx, cancel := context.WithTimeout(context.Background(), verifyVersionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, resolved, "--version")
	cmd.Env = argv
	cmd.WaitDelay = verifyVersionWaitDelay
	var out boundedOutput
	out.limit = versionProbeOutputLimit
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &limitedWriter{w: &stderr, n: versionProbeOutputLimit}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("install: version check: %w", err)
	}
	runErr := cmd.Wait()
	if runErr != nil {
		detail := ""
		if s := strings.TrimSpace(stderr.String()); s != "" {
			detail = ": " + s
		}
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("install: version check: timed out after %s; installed binary left at %s%s", verifyVersionTimeout, dest, detail)
		}
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return "", fmt.Errorf("install: version check: %s exited with code %d; installed binary left at %s%s", resolved, exitErr.ExitCode(), dest, detail)
		}
		return "", fmt.Errorf("install: version check: %w", runErr)
	}
	line := strings.TrimSpace(out.String())
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	return line, nil
}

// boundedOutput captures the first limit bytes of a stream and keeps
// draining (discarding the rest): the exec-managed pipe copy therefore
// never blocks a chatty child, while the probe's captured output stays
// bounded.
type boundedOutput struct {
	buf   bytes.Buffer
	limit int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	// The writer must ALWAYS report having consumed all of p (and never
	// error): a short write would make io.Copy stop draining the pipe,
	// close the read end and SIGPIPE a chatty child.
	total := len(p)
	if b.buf.Len() < b.limit {
		room := b.limit - b.buf.Len()
		if total > room {
			total = room
		}
		b.buf.Write(p[:total])
	}
	return len(p), nil
}

func (b *boundedOutput) String() string { return b.buf.String() }

// limitedWriter caps the bytes written to w at n and discards the rest
// (the probe's output must stay bounded without failing the child).
type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	if len(p) > l.n {
		_, err := l.w.Write(p[:l.n])
		l.n = 0
		return len(p), err
	}
	l.n -= len(p)
	return l.w.Write(p)
}

// pathContains reports whether dir is one of the PATH entries, split on
// the platform separator. Windows paths are case-insensitive (EqualFold);
// POSIX paths are case-sensitive (exact match).
func pathContains(path, dir string) bool {
	foldCase := runtime.GOOS == "windows"
	for _, entry := range filepath.SplitList(path) {
		if entry == "" {
			continue
		}
		if foldCase {
			if strings.EqualFold(strings.TrimRight(entry, `\/`), strings.TrimRight(dir, `\/`)) {
				return true
			}
		} else if entry == dir {
			return true
		}
	}
	return false
}

// movedAsideLine is the stdout line for an install that moved the running
// destination aside (Windows), and "" when the destination was replaced
// directly.
func movedAsideLine(res install.Result) string {
	if res.Previous == "" {
		return ""
	}
	return fmt.Sprintf("install: the running %s was moved to %s; the next install removes it", res.Destination, res.Previous)
}
