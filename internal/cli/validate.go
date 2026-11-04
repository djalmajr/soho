package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/eval"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/validation"
	"syscall"
)

// printable is the text filter of the validate command (DJA-695 fatia 3
// amendment): it removes only the control characters (unicode.IsControl —
// NUL, tabs, the ANSI escapes, the line breaks that would break a line)
// and nothing else — no truncation (a long command is shown whole) and no
// accent stripping (the plan and the log header show the command as it
// will run). It applies to everything that comes from the file or the
// command: the plan lines, the layer lines, the log's command header and
// the --json fields.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// Evidence-finalization seams (DJA-695 security amendment): the production
// values are the real functions; the tests swap them to make the evidence
// finalization fail deterministically (no full disk, no root) and fix the
// stamp clock to make a run's evidence path predictable.
var (
	validateLogTailWriter = appendValidateLogTail
	validateSummaryWriter = writeValidateSummary
	validateStampClock    = time.Now
	validateRunOwned      = eval.RunOwnedProcess
)

// validateDefaultTimeoutMs is the per-layer timeout of soho validate: 30
// minutes, the same bound the repository gates use.
const validateDefaultTimeoutMs = 30 * 60 * 1000

// maxValidateTimeoutMs is the largest whole-millisecond timeout: MaxInt64
// ns / 1e6 — the last value the duration multiplication
// (time.Duration(ms) * time.Millisecond) can represent without wrapping
// (DJA-695 F3). Anything above it is a usage error, not a wrapped
// deadline.
const maxValidateTimeoutMs = 9223372036854

// The per-layer results and the overall vocabulary (DJA-695 fatia 3).
// "falhou" and "não conseguiu rodar" are different: FAIL is a run that
// failed, BLOCKED is a run that could not start or hit an environment
// refusal, NOT RUN is a layer that did not run, and n/a layers do not
// count in the overall at all.
const (
	validateResultPass    = "PASS"
	validateResultFail    = "FAIL"
	validateResultBlocked = "BLOCKED"
	validateResultNotRun  = "NOT RUN"
	validateResultNA      = "n/a"
	validateResultPlan    = "plan"
)

// validateJSON is the machine-readable report: one object with overall,
// commit, clean, evidence and layers (decision 10); summary.json is the
// same bytes (decision 11). problems carries the BLOCKED reasons (the
// doctor's exact texts for file problems); layers is null when the file
// could not be read.
type validateJSON struct {
	Overall  string          `json:"overall"`
	Commit   *string         `json:"commit"`
	Clean    *bool           `json:"clean"`
	Evidence *string         `json:"evidence"`
	Problems []string        `json:"problems"`
	Layers   []validateLayer `json:"layers"`
}

type validateLayer struct {
	Name       string  `json:"name"`
	Kind       string  `json:"kind"`
	Result     string  `json:"result"`
	Reason     string  `json:"reason"`
	Exit       *int    `json:"exit"`
	DurationMs int64   `json:"duration_ms"`
	Log        *string `json:"log"`
}

// validateLayerResult is one layer's outcome of a run.
type validateLayerResult struct {
	layer       validation.Layer
	kind        string
	result      string
	reason      string
	exit        *int
	durMs       int64
	log         string
	ran         bool
	evidenceErr string
}

// cmdValidate is the native case of soho validate (DJA-695 fatia 3). It
// runs only when called: no other soho command executes a command of the
// file, and under SOHO_NOWRITE=1 even --plan is refused by the read-only
// gate (the command is outside nowriteReadInvocation on purpose).
func cmdValidate(args []string, ctx *core.Config, env platform.Env, cwd string) int {
	return doValidate(args, ctx, env, cwd, platform.Stdout)
}

// doValidate runs the project's VALIDATION.md test layers and reports one
// result per layer plus the worst overall. The file's commands run only
// here: each as `sh -c <command>` at the project root with the inherited
// environment, in file order, without stopping at a failure, each under
// the per-layer timeout as an owned process tree the layer ends. The
// overall is the worst of the layer results in the order FAIL, BLOCKED,
// NOT RUN, PASS (n/a layers do not count); with
// no command layer at all it is NOT RUN. The process exits 0 on PASS, 1 on
// FAIL, 5 on BLOCKED or NOT RUN, and 2 on usage errors. Text from the file
// or the command passes printable (control characters removed, nothing
// else) before it reaches the output; the evidence logs keep the full raw
// output.
func doValidate(args []string, ctx *core.Config, env platform.Env, cwd string, w io.Writer) int {
	layers, timeoutMs, plan, asJSON, ok := parseValidateArgs(args)
	if !ok {
		platform.Die("usage: soho validate [--layer <name>]... [--timeout MS] [--plan] [--json]", 2)
	}
	if ctx == nil {
		empty := core.Config{Entries: map[string]core.ConfigEntry{}}
		ctx = &empty
	}
	root := platform.ProjectRoot(env, cwd)
	check := validation.Check(filepath.Join(root, "VALIDATION.md"))
	if len(check.Problems) > 0 {
		// A file with a problem runs nothing: the overall is BLOCKED and
		// every problem appears with the doctor's exact text.
		return emitValidateBlocked(w, asJSON, check.Problems)
	}
	declared := make(map[string]bool, len(check.Doc.Layers))
	for _, l := range check.Doc.Layers {
		declared[l.Name] = true
	}
	for _, name := range layers {
		if !declared[name] {
			// A name that is not declared exits 2 before anything runs. The
			// diagnostic passes printable: the name is a CLI argument that
			// may carry control characters, and they never reach the output.
			platform.Die(fmt.Sprintf("layer '%s' is not declared in VALIDATION.md", printable(name)), 2)
		}
	}
	selected := make(map[string]bool, len(layers))
	if len(layers) == 0 {
		for _, l := range check.Doc.Layers {
			selected[l.Name] = true
		}
	} else {
		for _, name := range layers {
			selected[name] = true
		}
	}
	if plan {
		// --plan prints what each layer would do and runs nothing: no
		// evidence directory, no git, no command.
		return emitValidatePlan(w, asJSON, check.Doc.Layers, selected)
	}
	evidenceDir, reason := createValidateEvidenceDir(ctx, env, cwd)
	if reason != "" {
		// A state the evidence cannot use blocks before any layer runs.
		return emitValidateBlocked(w, asJSON, []string{"could not create the evidence directory: " + reason})
	}
	commit, clean := validateCommitInfo(env, root)
	results := make([]validateLayerResult, 0, len(check.Doc.Layers))
	for _, l := range check.Doc.Layers {
		r := validateLayerResult{layer: l, kind: l.Kind}
		switch l.Kind {
		case "na":
			// n/a shows n/a and does not count in the overall.
			r.result = validateResultNA
		case "manual":
			r.result = validateResultNotRun
			r.reason = "manual"
		case "command":
			if !selected[l.Name] {
				r.result = validateResultNotRun
				r.reason = "not selected"
			} else {
				// The selected command layer runs here: `sh -c <command>` at the
				// project root, with its evidence log written to the run's
				// evidence directory.
				r = runValidateLayer(l, root, env, timeoutMs, evidenceDir)
			}
		}
		results = append(results, r)
	}
	// The overall is the worst of the layer results in the order FAIL,
	// BLOCKED, NOT RUN, PASS; n/a layers do not count.
	var worst int
	have := false
	for _, r := range results {
		if r.result != validateResultNA {
			if rank := validateResultRank(r.result); !have || rank > worst {
				worst, have = rank, true
			}
		}
	}
	overall := validateResultPass
	if !have {
		// No command layer selected at all: the overall is NOT RUN.
		overall = validateResultNotRun
	} else {
		overall = validateResultName(worst)
	}
	// Evidence is part of the result (DJA-695 F2): a layer whose evidence
	// could not be finalized keeps its layer result, but the overall
	// becomes BLOCKED with the problem, and the run exits 5.
	var problems []string
	for _, r := range results {
		if r.evidenceErr != "" {
			problems = append(problems, r.evidenceErr)
		}
	}
	if len(problems) > 0 {
		overall = validateResultBlocked
	}
	summary := validateJSON{
		Overall:  overall,
		Problems: problems,
		Layers:   make([]validateLayer, 0, len(results)),
	}
	if summary.Problems == nil {
		summary.Problems = []string{}
	}
	if commit != "" {
		summary.Commit = &commit
		c := clean
		summary.Clean = &c
	}
	summary.Evidence = &evidenceDir
	for _, r := range results {
		lj := validateLayer{Name: printable(r.layer.Name), Kind: r.kind, Result: r.result, Reason: printable(r.reason), DurationMs: r.durMs}
		if r.exit != nil {
			lj.Exit = r.exit
		}
		if r.log != "" {
			log := r.log
			lj.Log = &log
		}
		summary.Layers = append(summary.Layers, lj)
	}
	jsonBytes := marshalValidate(summary)
	// The summary is evidence: it is written for EVERY run — the --json
	// run included, byte-equal to what the run prints on stdout (F2) —
	// and a write failure blocks the run, with the problem in the report.
	if err := validateSummaryWriter(evidenceDir, jsonBytes); err != nil {
		problems = append(problems, "could not write the evidence: "+printable(err.Error()))
		summary.Problems = problems
		summary.Overall = validateResultBlocked
		jsonBytes = marshalValidate(summary)
	}
	if asJSON {
		_, _ = w.Write(jsonBytes)
	} else {
		var b bytes.Buffer
		fmt.Fprintf(&b, "validation: %s\n", summary.Overall)
		for _, p := range summary.Problems {
			fmt.Fprintln(&b, printable(p))
		}
		if commit != "" {
			if clean {
				fmt.Fprintf(&b, "commit: %s (clean)\n", commit)
			} else {
				fmt.Fprintf(&b, "commit: %s (dirty)\n", commit)
			}
		} else {
			fmt.Fprintln(&b, "commit: none")
		}
		for _, r := range results {
			fmt.Fprintln(&b, validateLayerLine(r))
		}
		fmt.Fprintf(&b, "evidence: %s\n", evidenceDir)
		_, _ = w.Write(b.Bytes())
	}
	return validateExitCode(summary.Overall)
}

// parseValidateArgs parses the flags: --layer (repeatable), --timeout
// (positive integer of milliseconds), --plan and --json. Anything else is
// a usage error (exit 2).
func parseValidateArgs(args []string) (layers []string, timeoutMs int, plan, jsonOut, ok bool) {
	timeoutMs = validateDefaultTimeoutMs
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--plan":
			plan = true
		case "--json":
			jsonOut = true
		case "--layer":
			if i+1 >= len(args) || args[i+1] == "" {
				return nil, 0, false, false, false
			}
			layers = append(layers, args[i+1])
			i++
		case "--timeout":
			if i+1 >= len(args) {
				return nil, 0, false, false, false
			}
			n, ok := parseValidateTimeoutMs(args[i+1])
			if !ok {
				return nil, 0, false, false, false
			}
			timeoutMs = n
			i++
		default:
			return nil, 0, false, false, false
		}
	}
	return layers, timeoutMs, plan, jsonOut, true
}

// parseValidateTimeoutMs accepts a positive integer of milliseconds within
// the representable bound (maxValidateTimeoutMs): anything else (zero,
// negative, a unit suffix, non-numeric, or an out-of-bound value that
// would wrap the duration multiplication) is a usage error.
func parseValidateTimeoutMs(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 || n > maxValidateTimeoutMs {
		return 0, false
	}
	return n, true
}

// runValidateLayer runs one selected command layer as `sh -c <command>` at
// the project root and maps the outcome to a result (decision 4): exit 0
// is PASS unless the run exercised nothing (NOT RUN); exit 126 or 127, a
// process that did not start, or sh missing from the PATH is BLOCKED with
// the reason; any other exit, a signal, or a timeout is FAIL. The command
// runs as an owned process tree (eval.RunOwnedProcess): the whole tree is
// ended when the layer ends — on normal completion and on the timeout — so
// nothing the command started outlives the layer, and the output streams
// straight into the evidence log (merged stdout and stderr, in write
// order), never buffered in the runner. The per-layer timeout comes from
// context.WithTimeout; cancellation of that context ends the tree.
func runValidateLayer(l validation.Layer, root string, env platform.Env, timeoutMs int, evidenceDir string) (outcome validateLayerResult) {
	outcome.layer = l
	outcome.kind = l.Kind
	started := time.Now()
	defer func() { outcome.durMs = time.Since(started).Milliseconds() }()
	logPath, logFile, openErr := openValidateLog(evidenceDir, l)
	if openErr != "" {
		// Without the evidence log the layer cannot run: the run's output
		// would have nowhere to go, and a run without evidence is not a run
		// the command claims.
		outcome.result, outcome.reason = validateResultBlocked, "not started ("+printable(openErr)+")"
		return outcome
	}
	outcome.log = logPath
	shPath, found := platform.FindExecutable("sh", env, platform.Current())
	if !found {
		outcome.result, outcome.reason = validateResultBlocked, "sh not in PATH"
		finishValidateEvidence(logFile, "not started (sh not in PATH)", time.Since(started).Milliseconds(), false, &outcome)
		return outcome
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()
	res, err := validateRunOwned(ctx, shPath, []string{"-c", l.Value}, eval.OwnedProcessOptions{Dir: root, Env: env.List(), Output: logFile})
	outcome.ran = res.Started
	durMs := time.Since(started).Milliseconds()
	pendingCheck := false
	var exitErr *exec.ExitError
	hasExit := errors.As(err, &exitErr)
	var exitLine string
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		// The deadline fired: the run was killed (the whole tree with it)
		// and the result is the timeout, whatever the exit information says.
		outcome.result, outcome.reason = validateResultFail, fmt.Sprintf("timed out after %dms", timeoutMs)
		exitLine = fmt.Sprintf("timed out after %dms", timeoutMs)
	case !res.Started:
		// The process did not start: the layer did not run, with the run's
		// own short reason.
		outcome.result, outcome.reason = validateResultBlocked, "not started ("+printable(err.Error())+")"
		exitLine = "not started (" + printable(err.Error()) + ")"
	case res.CleanupErr != nil:
		// The tree may still be running: whatever the exit code says, this
		// layer cannot be a pass.
		reason := "could not prove the process tree ended: " + printable(res.CleanupErr.Error())
		outcome.result, outcome.reason = validateResultFail, reason
		exitLine = reason
	case res.Signaled:
		// A signal that is not the run's own cancellation (the timeout is
		// handled above): the process ended by a signal. On Windows there
		// are no signals, so this branch is the Unix one.
		outcome.result, outcome.reason = validateResultFail, "signal "+syscallSignalName(res.Signal)
		exitLine = "signal " + syscallSignalName(res.Signal)
	case err != nil && !hasExit:
		// The process ran, but the run's own error (a group-gone proof
		// failure, a containment or drain refusal) makes the result
		// unprovable: the layer fails with the run's own reason. An exit
		// error is not this case: it carries the exit code, which the
		// default branch maps (res.ExitCode was set from the exit
		// information; 0 for a green run).
		outcome.result, outcome.reason = validateResultFail, printable(err.Error())
		exitLine = printable(err.Error())
	default:
		code := res.ExitCode
		codePtr := code
		outcome.exit = &codePtr
		switch {
		case code == 0:
			// Exit 0 that exercised nothing is NOT RUN, never PASS. The
			// check runs after the tail is appended, over the whole log —
			// header, merged output and tail — the same bytes the evidence
			// keeps.
			outcome.result = validateResultPass
			pendingCheck = true
			exitLine = strconv.Itoa(code)
		case code == 126 || code == 127:
			outcome.result, outcome.reason = validateResultBlocked, fmt.Sprintf("exit %d", code)
			exitLine = strconv.Itoa(code)
		default:
			outcome.result, outcome.reason = validateResultFail, fmt.Sprintf("exit %d", code)
			exitLine = strconv.Itoa(code)
		}
	}
	finishValidateEvidence(logFile, exitLine, durMs, pendingCheck, &outcome)
	return outcome
}

// finishValidateEvidence finalizes the layer's evidence over the same
// descriptor the child wrote into (DJA-695 security amendment, F2 and
// Q2): the tail's write and the close return their errors; an exit-0
// layer then re-reads the finished log over that descriptor — after the
// seek to the start — for the exercised-nothing check, never by reopening
// the path, so the layer cannot swap the log for a link or a FIFO between
// the run and the check. A finalization failure is the run's evidence
// problem (the overall becomes BLOCKED with it, exit 5), not a change to
// the layer's result.
func finishValidateEvidence(f *os.File, exitLine string, durMs int64, pendingCheck bool, outcome *validateLayerResult) {
	defer func() {
		if err := f.Close(); err != nil {
			outcome.failEvidence("could not write the evidence: " + printable(err.Error()))
		}
	}()
	if err := validateLogTailWriter(f, exitLine, durMs); err != nil {
		outcome.failEvidence("could not write the evidence: " + printable(err.Error()))
		return
	}
	if pendingCheck {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			outcome.failEvidence("could not write the evidence: " + printable(err.Error()))
			return
		}
		nothing, err := validation.ExercisedNothing(f)
		if err != nil {
			outcome.failEvidence("could not write the evidence: " + printable(err.Error()))
		} else if nothing {
			outcome.result, outcome.reason = validateResultNotRun, "exercised nothing"
		}
	}
}

// failEvidence records the first evidence problem of the layer; later
// failures do not overwrite it (the run reports one problem per layer).
func (o *validateLayerResult) failEvidence(msg string) {
	if o.evidenceErr == "" {
		o.evidenceErr = msg
	}
}

// openValidateLog creates the layer's evidence log under the evidence
// directory and writes the command header: the child process inherits this
// descriptor and writes its output here directly (merged stdout and
// stderr, in write order), and the runner appends the trailing lines after
// the tree has ended. The file comes from eval.CreateOwnedOutput (DJA-695
// Q2): the runner appends the tail over the SAME descriptor the child wrote
// into, and then reads the finished log back over that same descriptor —
// never by reopening the path — so a layer cannot swap the log for a link
// or a FIFO between the run and the check. The header (before the run), the
// child's output and the tail (after the run) land in that order, and the
// mode is 0600, so the evidence is private. It refuses a pre-existing path
// (never following a link).
func openValidateLog(dir string, l validation.Layer) (string, *os.File, string) {
	path := filepath.Join(dir, l.Name+".log")
	f, err := eval.CreateOwnedOutput(path, 0o600)
	if err != nil {
		return "", nil, err.Error()
	}
	_ = os.Chmod(path, 0o600)
	if _, err := fmt.Fprintf(f, "command: sh -c '%s'\n", printable(l.Value)); err != nil {
		_ = f.Close()
		return "", nil, err.Error()
	}
	return path, f, ""
}

// appendValidateLogTail writes the log's trailing lines — the process
// outcome and the duration — after the child's output, over the same
// descriptor the child wrote into. The tree is dead when this runs (the
// run ends the whole tree before it returns), so the append lands after
// every byte the child wrote. The seek makes that true on Windows too, where
// the child shares this descriptor's file position and could have moved it.
// The leading newline keeps the tail on its own lines even when the child's
// output ended without one; a spare blank line when it did is the price of
// not reading the child's last byte. An error is returned (DJA-695 F2) and
// the file stays open: the finalization reads the finished log back over
// this same descriptor and closes it.
func appendValidateLogTail(f *os.File, exitLine string, durMs int64) error {
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	_, err := fmt.Fprintf(f, "\nexit: %s\nduration: %dms\n", exitLine, durMs)
	return err
}

// syscallSignalName renders the signal number the way the output shows it
// (the name on Unix; on Windows there are no signals, so this branch is
// unreachable — the number keeps the line well-formed if it is ever hit).
func syscallSignalName(n int) string {
	name := syscall.Signal(n).String()
	if name == "" {
		return strconv.Itoa(n)
	}
	return name
}

// createValidateEvidenceDir creates <state>/validation/<stamp>/: the
// parent directories 0700, never following a link in place of them, and a
// stamp unique enough that two runs in the same second do not collide
// (the timestamp carries milliseconds; a collision takes the next
// suffixed name). It returns the directory, or the refusal reason.
func createValidateEvidenceDir(ctx *core.Config, env platform.Env, cwd string) (string, string) {
	stateRoot := core.StateRootPath(ctx, env, cwd)
	base := filepath.Join(stateRoot, "validation")
	for _, p := range []string{stateRoot, base} {
		info, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err.Error()
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", "existing path is a link or not a directory"
		}
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err.Error()
	}
	_ = os.Chmod(base, 0o700)
	stamp := validateStampClock().UTC().Format("20060102T150405.000")
	for i := 0; ; i++ {
		name := stamp
		if i > 0 {
			name = fmt.Sprintf("%s-%d", stamp, i+1)
		}
		dir := filepath.Join(base, name)
		err := os.Mkdir(dir, 0o700)
		if err == nil {
			_ = os.Chmod(dir, 0o700)
			return dir, ""
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", err.Error()
		}
		if i >= 999 {
			return "", "evidence stamp collision"
		}
	}
}

// validateCommitInfo reads the project's commit and tree state with git
// (outside a repository, or without git, the commit is none). A dirty tree
// is shown, never refused.
func validateCommitInfo(env platform.Env, root string) (string, bool) {
	res := platform.RunCli("git", []string{"rev-parse", "HEAD"}, platform.RunOptions{Env: env, Cwd: root, TimeoutMs: 15000})
	if res.NotFound || res.Status == nil || *res.Status != 0 {
		return "", false
	}
	sha := strings.TrimSpace(res.Stdout)
	if sha == "" {
		return "", false
	}
	st := platform.RunCli("git", []string{"status", "--porcelain"}, platform.RunOptions{Env: env, Cwd: root, TimeoutMs: 15000})
	clean := st.Status != nil && *st.Status == 0 && strings.TrimSpace(st.Stdout) == ""
	return sha, clean
}

// emitValidateBlocked prints the BLOCKED report (the overall and the
// reasons) and returns its exit code. Nothing ran: there is no commit
// line and no evidence directory.
func emitValidateBlocked(w io.Writer, asJSON bool, problems []string) int {
	if asJSON {
		sanitized := make([]string, len(problems))
		for i, p := range problems {
			sanitized[i] = printable(p)
		}
		doc := validateJSON{Overall: validateResultBlocked, Problems: sanitized, Layers: nil}
		_, _ = w.Write(marshalValidate(doc))
	} else {
		_, _ = fmt.Fprintln(w, "validation: BLOCKED")
		for _, p := range problems {
			_, _ = fmt.Fprintln(w, printable(p))
		}
	}
	return 5
}

// emitValidatePlan prints the plan (decision 12): validation: plan and,
// per layer, what it would do, with the command. It runs nothing and
// writes nothing.
func emitValidatePlan(w io.Writer, asJSON bool, layers []validation.Layer, selected map[string]bool) int {
	if asJSON {
		doc := validateJSON{Overall: validateResultPlan, Problems: []string{}, Layers: make([]validateLayer, 0, len(layers))}
		for _, l := range layers {
			reason := ""
			switch l.Kind {
			case "command":
				if selected[l.Name] {
					reason = printable(l.Value)
				} else {
					reason = "not selected"
				}
			case "manual":
				reason = "manual"
			}
			doc.Layers = append(doc.Layers, validateLayer{Name: printable(l.Name), Kind: l.Kind, Result: validateResultPlan, Reason: reason})
		}
		_, _ = w.Write(marshalValidate(doc))
	} else {
		_, _ = fmt.Fprintln(w, "validation: plan")
		for _, l := range layers {
			switch l.Kind {
			case "na":
				fmt.Fprintf(w, "%s: n/a\n", printable(l.Name))
			case "manual":
				fmt.Fprintf(w, "%s: manual: %s\n", printable(l.Name), printable(l.Value))
			case "command":
				if selected[l.Name] {
					fmt.Fprintf(w, "%s: run %s\n", printable(l.Name), printable(l.Value))
				} else {
					fmt.Fprintf(w, "%s: not selected\n", printable(l.Name))
				}
			}
		}
	}
	return 0
}

// validateLayerLine renders one layer line of the text output. The layer
// name and the reason both pass printable: the name comes from the file,
// and the reason can carry the run's own (control-character-free, but not
// trusted) text.
func validateLayerLine(r validateLayerResult) string {
	name := printable(r.layer.Name)
	reason := printable(r.reason)
	switch r.result {
	case validateResultNA:
		return fmt.Sprintf("%s: n/a", name)
	case validateResultNotRun:
		if r.ran {
			return fmt.Sprintf("%s: NOT RUN (%s): %s", name, validateDurationText(r.durMs), reason)
		}
		return fmt.Sprintf("%s: NOT RUN: %s", name, reason)
	case validateResultPass:
		return fmt.Sprintf("%s: PASS (%s)", name, validateDurationText(r.durMs))
	case validateResultFail:
		return fmt.Sprintf("%s: FAIL (%s): %s", name, validateDurationText(r.durMs), reason)
	case validateResultBlocked:
		if r.ran {
			return fmt.Sprintf("%s: BLOCKED (%s): %s", name, validateDurationText(r.durMs), reason)
		}
		return fmt.Sprintf("%s: BLOCKED: %s", name, reason)
	}
	return name
}

// validateDurationText renders the duration the way the output shows it:
// seconds with one decimal, as in "12.3s".
func validateDurationText(ms int64) string {
	return strconv.FormatFloat(float64(ms)/1000, 'f', 1, 64) + "s"
}

// validateExitCode maps the overall to the process exit code (decision 9):
// 0 PASS, 1 FAIL, 5 BLOCKED or NOT RUN.
func validateExitCode(overall string) int {
	switch overall {
	case validateResultPass:
		return 0
	case validateResultFail:
		return 1
	default:
		return 5
	}
}

func validateResultRank(result string) int {
	switch result {
	case validateResultFail:
		return 4
	case validateResultBlocked:
		return 3
	case validateResultNotRun:
		return 2
	default:
		return 1
	}
}

func validateResultName(rank int) string {
	switch rank {
	case 4:
		return validateResultFail
	case 3:
		return validateResultBlocked
	case 2:
		return validateResultNotRun
	default:
		return validateResultPass
	}
}

// marshalValidate renders the report object; the same bytes go to --json
// stdout and to summary.json.
func marshalValidate(v validateJSON) []byte {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		b = []byte("{}\n")
	}
	return append(b, '\n')
}

// writeValidateSummary writes summary.json into the evidence directory
// with mode 0600, refusing a pre-existing path (never following a link).
// The write's and the close's errors are returned: the summary is
// evidence, and a persistence failure is a refusal, not a swallowed
// diagnostic (DJA-695 F2).
func writeValidateSummary(dir string, data []byte) error {
	path := filepath.Join(dir, "summary.json")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_ = os.Chmod(path, 0o600)
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
