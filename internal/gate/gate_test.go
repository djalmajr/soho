package gate

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

func artifactNames(base string) []string {
	return []string{base + ".command.json", base + ".result.json", base + ".stderr", base + ".stdout"}
}

func TestExitZeroWritesBothStreams(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "run")
	got := runGate(t, prefix, "30s", helperArgs("echo")...)
	if got.code != 0 || got.errOut != "" {
		t.Fatalf("code=%d stderr=%q", got.code, got.errOut)
	}
	res := readResult(t, prefix)
	if want := got.summaryLine(res); got.out != want {
		t.Fatalf("stdout %q, want %q", got.out, want)
	}
	if res.PID <= 0 || res.NativeExit != 0 || res.Signal != "" || res.ContextExpired || !res.WaitJoined || res.WaitError != "" || res.StartError != "" {
		t.Fatalf("result = %+v", res)
	}
	if res.ExecutionBound != "30s" {
		t.Fatalf("execution_bound = %q", res.ExecutionBound)
	}
	if text := readFile(t, prefix+".stdout"); text != "out-line\n" {
		t.Fatalf("stdout file %q", text)
	}
	if text := readFile(t, prefix+".stderr"); text != "err-line\n" {
		t.Fatalf("stderr file %q", text)
	}
	started, err1 := time.Parse(time.RFC3339Nano, res.Started)
	completed, err2 := time.Parse(time.RFC3339Nano, res.Completed)
	if err1 != nil || err2 != nil || !strings.HasSuffix(res.Started, "Z") || !strings.HasSuffix(res.Completed, "Z") || completed.Before(started) {
		t.Fatalf("timestamps started=%q completed=%q (%v, %v)", res.Started, res.Completed, err1, err2)
	}
	if names := listDir(t, dir); !reflect.DeepEqual(names, artifactNames("run")) {
		t.Fatalf("created %v, want exactly %v", names, artifactNames("run"))
	}
	assertReaped(t, res.PID)
}

func TestExitCodeIsThePrograms(t *testing.T) {
	for _, code := range []string{"3", "1", "125", "126", "255"} {
		prefix := filepath.Join(t.TempDir(), "run")
		got := runGate(t, prefix, "30s", helperArgs("exit", code)...)
		res := readResult(t, prefix)
		if got.code != atoi(t, code) || res.NativeExit != atoi(t, code) || res.ContextExpired || !res.WaitJoined || res.Signal != "" {
			t.Fatalf("exit %s: code=%d result=%+v", code, got.code, res)
		}
		if want := got.summaryLine(res); got.out != want {
			t.Fatalf("exit %s: stdout %q, want %q", code, got.out, want)
		}
	}
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	var n int
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}

func TestBoundExpiryEndsASleepingChild(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), "run")
	began := time.Now()
	got := runGate(t, prefix, "1s", helperArgs("sleep")...)
	elapsed := time.Since(began)
	res := readResult(t, prefix)
	if got.code != 124 {
		t.Fatalf("code=%d stdout=%q stderr=%q", got.code, got.out, got.errOut)
	}
	if !res.ContextExpired || !res.WaitJoined || res.StartError != "" || res.PID <= 0 {
		t.Fatalf("result = %+v", res)
	}
	if elapsed < time.Second || elapsed > 20*time.Second {
		t.Fatalf("elapsed %v around a 1s bound", elapsed)
	}
	if want := got.summaryLine(res); got.out != want || !strings.Contains(got.out, "context_expired=true wait_joined=true") {
		t.Fatalf("stdout %q, want %q", got.out, want)
	}
	assertEndedBySignal(t, res, "SIGTERM")
	assertReaped(t, res.PID)
}

func TestProgramNotFound(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "run")
	got := runGate(t, prefix, "30s", filepath.Join(dir, "no-such-program"), "arg")
	if got.code != 125 {
		t.Fatalf("code=%d stderr=%q", got.code, got.errOut)
	}
	res := readResult(t, prefix)
	if res.PID != 0 || res.StartError == "" || res.WaitJoined || res.ContextExpired || res.Signal != "" || res.WaitError != "" {
		t.Fatalf("result = %+v", res)
	}
	if want := "pid=0 native_exit=-1 context_expired=false wait_joined=false\n"; got.out != want {
		t.Fatalf("stdout %q, want %q", got.out, want)
	}
	if names := listDir(t, dir); !reflect.DeepEqual(names, artifactNames("run")) {
		t.Fatalf("created %v, want exactly %v", names, artifactNames("run"))
	}
	command := readFile(t, prefix+".command.json")
	if !strings.Contains(command, "no-such-program") {
		t.Fatalf("command.json lacks the attempted argv: %q", command)
	}
	for _, suffix := range []string{".stdout", ".stderr"} {
		if text := readFile(t, prefix+suffix); text != "" {
			t.Fatalf("%s = %q, want empty", suffix, text)
		}
	}
	// A bare name that is not on PATH is the same class.
	prefix2 := filepath.Join(dir, "bare")
	if got := runGate(t, prefix2, "30s", "soho-gate-no-such-command-xyz"); got.code != 125 {
		t.Fatalf("bare name: code=%d", got.code)
	}
	if res := readResult(t, prefix2); res.PID != 0 || res.StartError == "" {
		t.Fatalf("bare name result = %+v", res)
	}
}

func TestRefusalsStartNothingAndWriteNothing(t *testing.T) {
	base := t.TempDir()
	marker := filepath.Join(base, "marker")
	touch := helperArgs("touch", marker)
	missingParent := filepath.Join(base, "no-such-dir", "run")
	cases := []struct {
		name string
		args []string
	}{
		{"duration below 1s", append([]string{filepath.Join(base, "a"), "999ms", "--"}, touch...)},
		{"duration above 1h", append([]string{filepath.Join(base, "b"), "1h0m0.000000001s", "--"}, touch...)},
		{"zero duration", append([]string{filepath.Join(base, "c"), "0", "--"}, touch...)},
		{"negative duration", append([]string{filepath.Join(base, "d"), "-5s", "--"}, touch...)},
		{"not a duration", append([]string{filepath.Join(base, "e"), "soon", "--"}, touch...)},
		{"number without unit", append([]string{filepath.Join(base, "f"), "30", "--"}, touch...)},
		{"missing --", append([]string{filepath.Join(base, "g"), "30s"}, touch...)},
		{"-- in the wrong place", append([]string{filepath.Join(base, "h"), "30s", touch[0], "--"}, touch[1:]...)},
		{"missing program", []string{filepath.Join(base, "i"), "30s", "--"}},
		{"empty program", []string{filepath.Join(base, "j"), "30s", "--", ""}},
		{"missing duration", []string{filepath.Join(base, "k")}},
		{"no arguments", nil},
		{"empty prefix", append([]string{"", "30s", "--"}, touch...)},
		{"missing parent directory", append([]string{missingParent, "30s", "--"}, touch...)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := listDir(t, base)
			got := runGateRaw(t, tc.args...)
			if got.code != 2 || got.out != "" || !strings.HasPrefix(got.errOut, "soho: gate: ") || !strings.Contains(got.errOut, "usage: gate <prefix> <duration> -- <program> [args…]") {
				t.Fatalf("code=%d stdout=%q stderr=%q", got.code, got.out, got.errOut)
			}
			if after := listDir(t, base); !reflect.DeepEqual(before, after) {
				t.Fatalf("directory changed: %v -> %v", before, after)
			}
			if _, err := os.Stat(marker); err == nil {
				t.Fatal("the program ran")
			}
		})
	}
}

func TestExistingArtifactIsRefusedWhicheverOne(t *testing.T) {
	for _, suffix := range []string{".command.json", ".stdout", ".stderr", ".result.json"} {
		t.Run(suffix, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "marker")
			prefix := filepath.Join(dir, "run")
			if err := os.WriteFile(prefix+suffix, []byte("keep me"), 0o644); err != nil {
				t.Fatal(err)
			}
			got := runGate(t, prefix, "30s", helperArgs("touch", marker)...)
			if got.code != 2 || got.out != "" || !strings.Contains(got.errOut, prefix+suffix+" already exists") {
				t.Fatalf("code=%d stdout=%q stderr=%q", got.code, got.out, got.errOut)
			}
			if names := listDir(t, dir); !reflect.DeepEqual(names, []string{"run" + suffix}) {
				t.Fatalf("directory = %v, want only the existing file", names)
			}
			if text := readFile(t, prefix+suffix); text != "keep me" {
				t.Fatalf("existing file changed: %q", text)
			}
			if _, err := os.Stat(marker); err == nil {
				t.Fatal("the program ran")
			}
		})
	}
}

func TestExistingDirectoryAtAnArtifactNameIsRefused(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "run")
	if err := os.Mkdir(prefix+".stdout", 0o700); err != nil {
		t.Fatal(err)
	}
	got := runGate(t, prefix, "30s", helperArgs("echo")...)
	if got.code != 2 || got.out != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", got.code, got.out, got.errOut)
	}
	if names := listDir(t, dir); !reflect.DeepEqual(names, []string{"run.stdout"}) {
		t.Fatalf("directory = %v", names)
	}
}

func TestLargeOutputIsCompleteInTheFiles(t *testing.T) {
	const outBytes, errBytes = 9<<20 + 12345, 6<<20 + 7
	prefix := filepath.Join(t.TempDir(), "run")
	got := runGate(t, prefix, "60s", helperArgs("big", strconv.Itoa(outBytes), strconv.Itoa(errBytes))...)
	if got.code != 0 {
		t.Fatalf("code=%d stderr=%q", got.code, got.errOut)
	}
	for _, check := range []struct {
		suffix string
		size   int
	}{{".stdout", outBytes}, {".stderr", errBytes}} {
		data, err := os.ReadFile(prefix + check.suffix)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) != check.size {
			t.Fatalf("%s holds %d bytes, want %d", check.suffix, len(data), check.size)
		}
		want := make([]byte, check.size)
		for i := range want {
			want[i] = patternByte(i)
		}
		if sha256.Sum256(data) != sha256.Sum256(want) {
			t.Fatalf("%s content differs from the stream the program wrote", check.suffix)
		}
	}
	if res := readResult(t, prefix); res.NativeExit != 0 || !res.WaitJoined {
		t.Fatalf("result = %+v", res)
	}
}

func TestCommandJSONRecordsArgvCwdBoundAndNoEnvironment(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("GATE_PROBE", "secret-probe-value")
	hostile := []string{"a b", `quote"inside`, "$(touch hostile-ran)", "*", "-n", "line\nbreak", "'single'"}
	prefix := "rel-run" // relative: used exactly as given, in the working directory.
	got := runGate(t, prefix, "1m", helperArgs("argv", hostile...)...)
	if got.code != 0 {
		t.Fatalf("code=%d stderr=%q", got.code, got.errOut)
	}
	var seen []string
	if err := json.Unmarshal([]byte(readFile(t, prefix+".stdout")), &seen); err != nil || !reflect.DeepEqual(seen, hostile) {
		t.Fatalf("the program saw %q (%v), want %q", seen, err, hostile)
	}
	if _, err := os.Stat(filepath.Join(dir, "hostile-ran")); err == nil {
		t.Fatal("an argument was interpreted by a shell")
	}
	raw := readFile(t, prefix+".command.json")
	if strings.Contains(raw, "secret-probe-value") || strings.Contains(raw, "GATE_PROBE") {
		t.Fatalf("command.json records the environment: %q", raw)
	}
	var generic map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &generic); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(generic))
	for key := range generic {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if want := []string{"argv", "bound", "cwd", "started"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("command.json keys %v, want %v", keys, want)
	}
	var rec commandRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		t.Fatal(err)
	}
	wantCwd, _ := os.Getwd()
	if rec.Cwd != wantCwd || rec.Bound != "1m0s" || !reflect.DeepEqual(rec.Argv, append(helperArgs("argv"), hostile...)) {
		t.Fatalf("command.json = %+v", rec)
	}
	if res := readResult(t, prefix); res.Started != rec.Started {
		t.Fatalf("started differs: result %q command %q", res.Started, rec.Started)
	}
}

func TestProgramKeepsWorkingDirectoryEnvironmentAndClosedStdin(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("GATE_PROBE", "kept")
	got := runGate(t, "probe-run", "1m", helperArgs("probe")...)
	if got.code != 0 {
		t.Fatalf("code=%d stderr=%q", got.code, got.errOut)
	}
	wantCwd, _ := os.Getwd()
	want := "cwd=" + wantCwd + "\nprobe=kept\nstdin=0\n"
	if text := readFile(t, "probe-run.stdout"); text != want {
		t.Fatalf("probe output %q, want %q", text, want)
	}
}

func TestBoundaryDurationsAreAccepted(t *testing.T) {
	for _, bound := range []string{"1s", "1000ms", "1h", "60m", "3600s"} {
		prefix := filepath.Join(t.TempDir(), "run")
		got := runGate(t, prefix, bound, helperArgs("exit", "0")...)
		// A 1s bound may genuinely expire on a slow start (124); what must
		// never happen is the usage refusal (2) of an in-range duration.
		if got.code != 0 && !(got.code == 124 && (bound == "1s" || bound == "1000ms")) {
			t.Fatalf("bound %s: code=%d stderr=%q", bound, got.code, got.errOut)
		}
		if res := readResult(t, prefix); res.ExecutionBound != mustDuration(t, bound).String() {
			t.Fatalf("bound %s: execution_bound %q", bound, res.ExecutionBound)
		}
	}
}

func mustDuration(t *testing.T, s string) time.Duration {
	t.Helper()
	d, err := time.ParseDuration(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// A program that ends at the instant of the bound is either a normal exit or
// an expiry, never a mixture: every outcome must be self-consistent.
func TestExitAtTheInstantOfTheBoundIsConsistent(t *testing.T) {
	for i := 0; i < 6; i++ {
		prefix := filepath.Join(t.TempDir(), "run")
		got := runGate(t, prefix, "1s", helperArgs("sleep-ms", "1000")...)
		res := readResult(t, prefix)
		if !res.WaitJoined || res.StartError != "" {
			t.Fatalf("round %d: result = %+v", i, res)
		}
		switch {
		case res.ContextExpired:
			if got.code != 124 {
				t.Fatalf("round %d: expired but code=%d result=%+v", i, got.code, res)
			}
		default:
			if got.code != 0 || res.NativeExit != 0 || res.Signal != "" {
				t.Fatalf("round %d: normal exit but code=%d result=%+v", i, got.code, res)
			}
		}
	}
}

func TestFilesForUsesThePrefixAsGiven(t *testing.T) {
	got := FilesFor("~/$HOME/*/x y")
	want := Files{Command: "~/$HOME/*/x y.command.json", Stdout: "~/$HOME/*/x y.stdout", Stderr: "~/$HOME/*/x y.stderr", Result: "~/$HOME/*/x y.result.json"}
	if got != want {
		t.Fatalf("got %+v", got)
	}
}

func TestHostilePrefixIsNotExpanded(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	prefix := "we ird $HOME *"
	if runtime.GOOS == "windows" {
		// '*' cannot be part of a Windows file name: there the gate refuses
		// the prefix without creating anything, and the expansion check uses
		// characters cmd.exe would interpret instead.
		refused := runGate(t, prefix, "180s", helperArgs("echo")...)
		if refused.code != 2 {
			t.Fatalf("invalid file name: code=%d stderr=%q", refused.code, refused.errOut)
		}
		if names := listDir(t, dir); len(names) != 0 {
			t.Fatalf("refusal created %v", names)
		}
		prefix = "we ird $HOME %PATH% ^&"
	}
	// This test concerns literal filenames, not the child's execution bound.
	got := runGate(t, prefix, "180s", helperArgs("echo")...)
	if got.code != 0 {
		t.Fatalf("code=%d stderr=%q", got.code, got.errOut)
	}
	want := []string{prefix + ".command.json", prefix + ".result.json", prefix + ".stderr", prefix + ".stdout"}
	if names := listDir(t, dir); !reflect.DeepEqual(names, want) {
		t.Fatalf("created %v, want %v", names, want)
	}
}

func TestResultFileIsOneJSONLine(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), "run")
	runGate(t, prefix, "30s", helperArgs("echo")...)
	data := readFile(t, prefix+".result.json")
	if !strings.HasSuffix(data, "}\n") || strings.Count(data, "\n") != 1 || !bytes.HasPrefix([]byte(data), []byte(`{"pid":`)) {
		t.Fatalf("result.json = %q", data)
	}
}
