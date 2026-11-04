// Command imagecheck verifies the base container image and its build
// context: it scans the curated context and the saved image for secrets,
// credential files and denied tokens, compares two saved builds layer by
// layer, and smoke-tests the installed tool inventory. Standard library
// only; docker is an external CLI invoked by argv, never through a shell,
// and a matched secret or denied token is never printed.
package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatches one subcommand through the masked writers. Exit
// codes: 0 pass, 1 check failed (findings, differences or inventory
// mismatch), 2 usage, 4 I/O or external command failure.
func run(args []string, stdout, stderr io.Writer) int {
	return withMaskedOutput(stdout, stderr, func(out, errOut io.Writer) int {
		return dispatch(args, out, errOut)
	})
}

// withMaskedOutput wraps stdout and stderr in a ruleMaskWriter, calls
// fn with the wrapped writers and flushes both before fn's result is
// returned: every message fn writes — usage errors, unknown commands
// and parse failures included — and any final message without a
// newline, passes through the built-in rule mask, so a string that
// matches a rule in an argument never reaches the output. A multi-line
// match is masked whole only when its message is one Write ending in a
// line break, which is how this package writes every message (see
// ruleMaskWriter). A failed flush is ignored, the same class as the
// write errors this package ignores elsewhere.
func withMaskedOutput(stdout, stderr io.Writer, fn func(out, errOut io.Writer) int) int {
	out := &ruleMaskWriter{w: stdout}
	errOut := &ruleMaskWriter{w: stderr}
	defer func() {
		out.Flush()
		errOut.Flush()
	}()
	return fn(out, errOut)
}

// dispatch runs the named subcommand on out and errOut; it must
// receive writers that already carry every wrap the run needs.
func dispatch(args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		usage(errOut)
		return 2
	}
	switch args[0] {
	case "context":
		return runContext(args[1:], out, errOut)
	case "image":
		return runImage(args[1:], out, errOut)
	case "compare":
		return runCompare(args[1:], out, errOut)
	case "smoke":
		return runSmoke(args[1:], out, errOut)
	case "-h", "--help", "help":
		usage(out)
		return 0
	default:
		fmt.Fprintf(errOut, "imagecheck: unknown command %q\n", args[0])
		usage(errOut)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `usage:
  imagecheck context <dir> --allow <prefix>... [--deny-file <path>] [--accept-file <path>] [--accept '<rule> <offset> <length> sha256:<hex> <path>']...
  imagecheck image <docker-save.tar> [--deny-file <path>] [--accept-file <path>] [--accept '<rule> <offset> <length> sha256:<hex> <path>']...
  imagecheck compare <a.tar> <b.tar>
  imagecheck smoke --image <ref> --spec <path> [--dockerfile <path>] [--build-arg NAME=VALUE]... [--docker <exe>] [--timeout <duration>]
`)
}
