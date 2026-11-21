package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/reviewctx"
	"github.com/djalmajr/soho/internal/text"
)

const reviewContextUsage = "usage: review-context <base>[..<head>] [--cwd <dir>] [--out <file>]"

// reviewContextHasOut is also used by the NOWRITE classifier: without --out
// the command only reads committed Git objects, with --out it writes a file.
func reviewContextHasOut(args []string) bool {
	for _, arg := range args {
		if arg == "--out" {
			return true
		}
	}
	return false
}

func cmdReviewContext(args []string, env platform.Env, cwd string) int {
	fail := func(err error) int {
		fmt.Fprintf(platform.Stderr, "soho: review-context: %s\n", text.QuoteControls(err.Error()))
		return 2
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "--") {
		return fail(fmt.Errorf("%s", reviewContextUsage))
	}
	rangeArg, out, repo := args[0], "", cwd
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--cwd", "--out":
			if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "--") {
				return fail(fmt.Errorf("%s", reviewContextUsage))
			}
			if args[i] == "--out" {
				out = args[i+1]
			} else {
				repo = args[i+1]
			}
			i++
		default:
			return fail(fmt.Errorf("unknown option %s", args[i]))
		}
	}
	parts := strings.Split(rangeArg, "..")
	if len(parts) > 2 || parts[0] == "" || len(parts) == 2 && parts[1] == "" {
		return fail(fmt.Errorf("%s", reviewContextUsage))
	}
	base, head := parts[0], "HEAD"
	if len(parts) == 2 {
		head = parts[1]
	}
	if !filepath.IsAbs(repo) {
		absolute, err := filepath.Abs(repo)
		if err != nil {
			return fail(err)
		}
		repo = absolute
	}
	if out != "" {
		if err := reviewContextCheckOut(out); err != nil {
			return fail(err)
		}
	}
	pack, err := reviewctx.Pack(reviewctx.NewRunner(env, repo), repo, base, head)
	if err != nil {
		return fail(err)
	}
	if out == "" {
		_, err := io.WriteString(platform.Stdout, pack)
		if err != nil {
			return fail(err)
		}
		return 0
	}
	if err := writeReviewContextOut(out, pack); err != nil {
		return fail(err)
	}
	return 0
}

// reviewContextCheckOut refuses every existing entry at the destination,
// including a dangling link or a directory, before the pack is built or any
// temporary file is created. The link in writeReviewContextOut repeats the
// refusal atomically against a destination created after this check.
func reviewContextCheckOut(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("refusing to replace existing entry %s", path)
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

// writeReviewContextOut writes a private temporary file in the destination
// directory and hard-links it into place. Link creation is the atomic
// no-replace step: an entry created between the check and the link makes it
// fail without touching that entry.
func writeReviewContextOut(path, content string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".soho-review-context-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := io.WriteString(tmp, content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Link(name, path)
}

// reviewSnapshotOrContext shares the existing read-only command dispatch line
// so this command needs no separate configuration or transport setup.
func reviewSnapshotOrContext(command string, args []string, ctx *core.Config, env platform.Env, cwd string) int {
	if command == "review-context" {
		return cmdReviewContext(args[1:], env, cwd)
	}
	return cmdReviewSnapshot(args[1:], ctx, env, cwd)
}

// nowriteBareOrReviewContext is the classifier for the bare read commands
// that share a case with review-context: the latter also allows its --cwd
// option, but refuses the writing --out form.
func nowriteBareOrReviewContext(args []string) bool {
	if args[0] == "review-context" {
		return !reviewContextHasOut(args[1:])
	}
	return len(args) == 1
}
