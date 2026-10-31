package peer_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/cli"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// The native fake's remote process records its own identity before its normal
// WaitFile rule runs. This adds an observable lifecycle handshake without a
// second fake engine or a shell fixture.
func init() {
	if file := os.Getenv("SOHO_FIND_REMOTE_PID"); file != "" &&
		strings.Join(os.Args[1:], " ") == "--machine slow api snapshot" {
		// Write then rename, so the test never reads a created but still empty file.
		tmp := file + ".tmp"
		if err := os.WriteFile(tmp, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			os.Exit(91)
		}
		if err := os.Rename(tmp, file); err != nil {
			os.Exit(91)
		}
	}
}

type findProgressWriter struct {
	mu    sync.Mutex
	data  bytes.Buffer
	ready chan struct{}
	once  sync.Once
}

func (w *findProgressWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.data.Write(p)
	if strings.Contains(w.data.String(), "local/w1:p1\tbuild\t") {
		w.once.Do(func() { close(w.ready) })
	}
	return n, err
}

func (w *findProgressWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.data.String()
}

func TestFindStreamPublishesBeforeDeadlineAndStopsRemoteProcess(t *testing.T) {
	dir := t.TempDir()
	f := newFixtureAt(t, dir, []fakecli.Rule{
		{Argv: []string{"api", "snapshot"}, Stdout: `{"result":{"snapshot":` + localSnapshot + `}}`},
		{Argv: []string{"machine", "list", "--json"}, Stdout: `[{"label":"slow","enabled":true}]`},
		{Argv: []string{"--machine", "slow", "api", "snapshot"}, WaitFile: filepath.Join(dir, "never-remote")},
	})
	pidFile := filepath.Join(dir, "remote.pid")
	f.env["SOHO_FIND_REMOTE_PID"] = pidFile
	f.env["FAKECLI_WAIT_FILE_TIMEOUT_MS"] = "10000"
	processEnv := f.env.Clone()
	if runtime.GOOS != "windows" {
		ps, err := exec.LookPath("ps")
		if err != nil {
			t.Fatal(err)
		}
		bin := t.TempDir()
		if err := os.Symlink(ps, filepath.Join(bin, "ps")); err != nil {
			t.Fatal(err)
		}
		processEnv["PATH"] = bin
	}
	out := &findProgressWriter{ready: make(chan struct{})}
	var stderr bytes.Buffer
	oldOut, oldErr := platform.Stdout, platform.Stderr
	platform.Stdout, platform.Stderr = out, &stderr
	done := make(chan struct{})
	var code int
	go func() {
		defer close(done)
		code = cli.Run([]string{"find", "build", "--all", "--timeout", "2000"}, f.env)
	}()
	defer func() {
		// Restore the shared writers only after the bounded CLI run has ended.
		<-done
		platform.Stdout, platform.Stderr = oldOut, oldErr
	}()
	select {
	case <-out.ready:
	case <-done:
		t.Fatal("local output arrived only after the CLI returned")
	case <-time.After(time.Second):
		t.Fatal("local output waited for the stopped remote's deadline")
	}
	select {
	case <-done:
		t.Fatal("the in-flight local-output control did not observe a pending remote")
	default:
	}
	// Any read error is "not yet" until the deadline: on Windows another
	// process (an indexer or antivirus) can hold the freshly renamed file for
	// a moment, and the read then fails with a sharing violation (DJA-635).
	var pid int
	var lastErr error
	until := time.Now().Add(time.Second)
	for time.Now().Before(until) {
		data, err := os.ReadFile(pidFile)
		if err == nil {
			pid, err = strconv.Atoi(string(data))
			if err != nil || pid <= 0 {
				t.Fatalf("remote PID handshake=%q err=%v", data, err)
			}
			break
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatalf("remote did not start before the CLI deadline (last read error: %v)", lastErr)
	}
	start, name, alive := platform.ProcInfo(pid, processEnv)
	if !alive || start == "" || name == "" {
		t.Fatal("remote was not alive at its native PID handshake")
	}
	t.Cleanup(func() {
		currentStart, currentName, currentAlive := platform.ProcInfo(pid, processEnv)
		if currentAlive && currentStart == start && currentName == name {
			if process, err := os.FindProcess(pid); err == nil {
				_ = process.Kill()
			}
		}
	})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("CLI did not finish at its bounded remote deadline")
	}
	want := "local/w1:p1\tbuild\tpi\tworking\tsoho\tmain\t/Users/x/soho\n"
	if code != 0 || out.String() != want || !strings.Contains(stderr.String(), "timed out") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), stderr.String())
	}
	if _, live := platform.ReadProc(pid, processEnv); live != platform.ProcGone {
		t.Fatalf("the remote process was not proven gone after CLI return: %v", live)
	}
}

func TestFindStreamLocalBeforeStoppedRemote(t *testing.T) {
	deadlineMs := 800
	if runtime.GOOS == "windows" {
		// Windows Server CI exhausted 800ms while starting enumeration.
		// Leave startup room while keeping the global deadline below the
		// fake's 10s wait cap and checking that local rows arrive first.
		deadlineMs = 3000
	}
	deadlineArg := strconv.Itoa(deadlineMs)
	minimumWait := time.Duration(deadlineMs)*time.Millisecond - 200*time.Millisecond
	t.Run("local rows print while the stopped remote is still due, and the run exits at the deadline", func(t *testing.T) { // 验收 1: 真实 CmdFind 入口, 本地先于停机的远程
		dir := t.TempDir()
		rules := []fakecli.Rule{
			{Argv: []string{"api", "snapshot"}, Stdout: `{"result":{"snapshot":` + localSnapshot + `}}`},
			{Argv: []string{"machine", "list", "--json"}, Stdout: `[{"label":"slow","enabled":true}]`},
			// The remote waits for a file that never appears. The global
			// deadline must stop it before the fake's own 10s wait cap.
			// Race detection stays enabled without its artificial exit wait.
			{Argv: []string{"--machine", "slow", "api", "snapshot"}, WaitFile: filepath.Join(dir, "never-slow")},
		}
		f := newFixtureAt(t, dir, rules)
		f.env["FAKECLI_WAIT_FILE_TIMEOUT_MS"] = "10000"
		start := time.Now()
		code, out, stderr := f.run([]string{"find", "build", "--all", "--timeout", deadlineArg})
		elapsed := time.Since(start)
		if code != 0 {
			t.Fatalf("code=%d out=%q stderr=%q elapsed=%v", code, out, stderr, elapsed)
		}
		// Exactly the local TSV row: the stopped remote contributes nothing,
		// and stdout holds no JSON noise in TSV mode.
		want := "local/w1:p1\tbuild\tpi\tworking\tsoho\tmain\t/Users/x/soho\n"
		if out != want {
			t.Fatalf("out=%q stderr=%q elapsed=%v (want exactly the local TSV row)", out, stderr, elapsed)
		}
		if !strings.Contains(stderr, `machine 'slow' unavailable`) || !strings.Contains(stderr, "timed out") {
			t.Fatalf("stderr=%q elapsed=%v (want the remote's deadline diagnostic)", stderr, elapsed)
		}
		if elapsed < minimumWait || elapsed > 8000*time.Millisecond {
			t.Fatalf("elapsed=%v (want near the %dms deadline, not the fake's 10s cap)", elapsed, deadlineMs)
		}
	})
	t.Run("json rows stay one clean object per line", func(t *testing.T) {
		dir := t.TempDir()
		f := newFixtureAt(t, dir, []fakecli.Rule{
			{Argv: []string{"api", "snapshot"}, Stdout: `{"result":{"snapshot":` + localSnapshot + `}}`},
			{Argv: []string{"machine", "list", "--json"}, Stdout: `[{"label":"slow","enabled":true}]`},
			{Argv: []string{"--machine", "slow", "api", "snapshot"}, WaitFile: filepath.Join(dir, "never-slow")},
		})
		f.env["FAKECLI_WAIT_FILE_TIMEOUT_MS"] = "10000"
		start := time.Now()
		code, out, stderr := f.run([]string{"find", "build", "--all", "--json", "--timeout", deadlineArg})
		elapsed := time.Since(start)
		if code != 0 {
			t.Fatalf("code=%d out=%q stderr=%q elapsed=%v", code, out, stderr, elapsed)
		}
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if len(lines) != 1 {
			t.Fatalf("lines=%q elapsed=%v", out, elapsed)
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
			t.Fatalf("json row %q: %v", lines[0], err)
		}
		if entry["ref"] != "local/w1:p1" {
			t.Fatalf("entry=%v", entry)
		}
		if !strings.Contains(stderr, `machine 'slow' unavailable`) {
			t.Fatalf("stderr=%q elapsed=%v", stderr, elapsed)
		}
		if elapsed < minimumWait || elapsed > 8000*time.Millisecond {
			t.Fatalf("elapsed=%v (want near the %dms deadline)", elapsed, deadlineMs)
		}
	})
	t.Run("a stopped enumeration still leaves the local rows and reports the deadline", func(t *testing.T) { // 验收 1: 枚举停掉也在本地输出之后
		dir := t.TempDir()
		f := newFixtureAt(t, dir, []fakecli.Rule{
			{Argv: []string{"api", "snapshot"}, Stdout: `{"result":{"snapshot":` + localSnapshot + `}}`},
			{Argv: []string{"machine", "list", "--json"}, WaitFile: filepath.Join(dir, "never-list")},
		})
		f.env["FAKECLI_WAIT_FILE_TIMEOUT_MS"] = "10000"
		start := time.Now()
		code, out, stderr := f.run([]string{"find", "--all", "--timeout", deadlineArg})
		elapsed := time.Since(start)
		if code != 0 {
			t.Fatalf("code=%d out=%q stderr=%q elapsed=%v", code, out, stderr, elapsed)
		}
		if !strings.Contains(out, "local/w1:p1") || !strings.Contains(out, "local/w1:p2") {
			t.Fatalf("out=%q (local rows must not wait on the enumeration)", out)
		}
		if !strings.Contains(stderr, "machine list failed") || !strings.Contains(stderr, "timed out") {
			t.Fatalf("stderr=%q elapsed=%v", stderr, elapsed)
		}
		if elapsed < minimumWait || elapsed > 8000*time.Millisecond {
			t.Fatalf("elapsed=%v (want near the %dms deadline)", elapsed, deadlineMs)
		}
	})
}

func TestFindStreamArrivalAndPartial(t *testing.T) {
	t.Run("two remotes with inverted completion print in arrival order", func(t *testing.T) { // 验收 2: 到达顺序, 本地最先
		f := newFixture(t, []fakecli.Rule{
			{Argv: []string{"api", "snapshot"}, Stdout: `{"result":{"snapshot":` + localSnapshot + `}}`},
			{Argv: []string{"--machine", "fast", "api", "snapshot"}, Stdout: `{"result":{"snapshot":` + streamSnapshot("fast", "agent-fast") + `}}`, Delay: 150},
			{Argv: []string{"--machine", "slow", "api", "snapshot"}, Stdout: `{"result":{"snapshot":` + streamSnapshot("slow", "agent-slow") + `}}`, Delay: 600},
		})
		code, out, stderr := f.run([]string{"find", "--machine", "fast", "--machine", "slow", "--timeout", "3000"})
		if code != 0 || stderr != "" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		refs := streamRefs(out)
		want := []string{"local/w1:p1", "local/w1:p2", "fast/wfast:p1", "slow/wslow:p1"}
		if len(refs) != len(want) {
			t.Fatalf("refs=%v (want %v)", refs, want)
		}
		for i := range want {
			if refs[i] != want[i] {
				t.Fatalf("refs=%v (arrival order: local first, then the earlier remote)", refs)
			}
		}
	})
	t.Run("a partial failure is explicit and duplicate labels do not duplicate rows", func(t *testing.T) {
		f := newFixture(t, []fakecli.Rule{
			{Argv: []string{"api", "snapshot"}, Stdout: `{"result":{"snapshot":` + localSnapshot + `}}`},
			{Argv: []string{"--machine", "ok", "api", "snapshot"}, Stdout: `{"result":{"snapshot":` + streamSnapshot("ok", "agent-ok") + `}}`},
			{Argv: []string{"--machine", "bad", "api", "snapshot"}, Stderr: "machine down", Code: 1},
		})
		code, out, stderr := f.run([]string{"find", "--machine", "ok", "--machine", "bad", "--machine", "ok"})
		if code != 0 {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if !strings.Contains(stderr, `machine 'bad' unavailable: machine down`) {
			t.Fatalf("stderr=%q (want the explicit partial failure)", stderr)
		}
		refs := streamRefs(out)
		counts := map[string]int{}
		for _, ref := range refs {
			counts[ref]++
		}
		if len(refs) != 3 || counts["local/w1:p1"] != 1 || counts["local/w1:p2"] != 1 || counts["ok/wok:p1"] != 1 {
			t.Fatalf("refs=%v (want local twice and ok once, each a single row)", refs)
		}
		for ref := range counts {
			if strings.HasPrefix(ref, "bad/") {
				t.Fatalf("rows from the failed machine: %v", refs)
			}
		}
	})
	t.Run("an invalid --timeout is refused before any herdr call", func(t *testing.T) {
		bad := [][]string{
			{"find", "build", "--timeout", "abc"},
			{"find", "build", "--timeout", "0"},
			{"find", "build", "--timeout", "-5"},
			{"find", "build", "--timeout", "1.5"},
			{"find", "build", "--timeout"},
			{"find", "build", "--timeout", "--all"},
		}
		for _, argv := range bad {
			f := newFixture(t, []fakecli.Rule{{Argv: []string{"api", "snapshot"}, Stdout: `{"result":{"snapshot":` + localSnapshot + `}}`}})
			code, out, stderr := f.run(argv)
			calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
			if code != 2 || out != "" || stderr != "soho: find: --timeout expects the deadline in milliseconds or a duration such as 90s, 30m or 2h\n" {
				t.Fatalf("argv=%v code=%d out=%q stderr=%q", argv, code, out, stderr)
			}
			if (err != nil && !os.IsNotExist(err)) || (err == nil && len(calls) != 0) {
				t.Fatalf("argv=%v herdr calls=%+v err=%v (refused before invoking herdr)", argv, calls, err)
			}
		}
	})
	t.Run("a remote reference pulls in its machine", func(t *testing.T) {
		f := newFixture(t, []fakecli.Rule{
			{Argv: []string{"api", "snapshot"}, Stdout: `{"result":{"snapshot":` + localSnapshot + `}}`},
			{Argv: []string{"--machine", "fast", "api", "snapshot"}, Stdout: `{"result":{"snapshot":` + streamSnapshot("fast", "agent-fast") + `}}`},
		})
		code, out, stderr := f.run([]string{"find", "fast/wfast:p1"})
		if code != 0 || stderr != "" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		refs := streamRefs(out)
		if len(refs) != 1 || refs[0] != "fast/wfast:p1" {
			t.Fatalf("refs=%v (the exact reference, from its own machine)", refs)
		}
	})
}

func TestFindStreamExitCodes(t *testing.T) {
	t.Run("a failed local still exits 4 and keeps the remote rows", func(t *testing.T) { // 决策: 保留 4/0/1 现行码
		f := newFixture(t, []fakecli.Rule{
			{Argv: []string{"api", "snapshot"}, Stderr: "local down", Code: 1},
			{Argv: []string{"--machine", "ok", "api", "snapshot"}, Stdout: `{"result":{"snapshot":` + streamSnapshot("ok", "agent-ok") + `}}`},
		})
		code, out, stderr := f.run([]string{"find", "--machine", "ok"})
		if code != 4 {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if !strings.Contains(out, "ok/wok:p1") || !strings.Contains(stderr, `machine 'local' unavailable: local down`) {
			t.Fatalf("out=%q stderr=%q", out, stderr)
		}
	})
	t.Run("no match exits 1 with empty stdout", func(t *testing.T) {
		f := newFixture(t, []fakecli.Rule{{Argv: []string{"api", "snapshot"}, Stdout: `{"result":{"snapshot":` + localSnapshot + `}}`}})
		code, out, stderr := f.run([]string{"find", "zzz-no-match"})
		if code != 1 || out != "" || stderr != "" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
}
