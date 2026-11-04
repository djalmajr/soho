// Package container tests the transfer budget of the download helper
// (the inline fetch() function in the Dockerfile fetch stage). The
// Dockerfile is the single source of truth for the flags: the tests parse
// the helper line and scale down only the absolute time values, so the
// suite finishes in seconds instead of the minutes the real budget spans.
package container

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// pinnedHelperFlags is the exact ordered flag sequence the Dockerfile
// helper line must carry: -fsSL, the --proto '=https' pin, the
// connection timeout, the stall detector, the per-attempt cap, the retry
// budget and the retry-all-errors flag.
var pinnedHelperFlags = []string{
	"-fsSL",
	"--proto", "=https",
	"--connect-timeout", "30",
	"--speed-limit", "1024",
	"--speed-time", "60",
	"--max-time", "600",
	"--retry", "5",
	"--retry-delay", "10",
	"--retry-max-time", "1200",
	"--retry-all-errors",
}

// reducedSeconds scales the Dockerfile's absolute time values down for
// the local suite. The flag names and the remaining values (-fsSL,
// --retry 5, --speed-limit 1024, --retry-all-errors) come from the
// Dockerfile helper line itself; only these five time values are
// substituted here so the budget is exercised in seconds.
var reducedSeconds = map[string]int{
	"--connect-timeout": 2,
	"--speed-time":      1,
	"--max-time":        3,
	"--retry-delay":     1,
	"--retry-max-time":  8,
}

// ciSlack is the extra margin for a loaded CI on top of the reduced
// retry-max-time + max-time budget.
const ciSlack = 30 * time.Second

// pinnedBytes is the synthetic payload the local servers serve for the
// "good" download; its SHA-256 is computed in this test, never pinned.
var pinnedBytes = func() []byte {
	b := make([]byte, 4096)
	for i := range b {
		b[i] = byte('a' + i%26)
	}
	return b
}()

var pinnedHash = sha256.Sum256(pinnedBytes)

var pinnedHex = hex.EncodeToString(pinnedHash[:])

// --- Dockerfile parsing (single source of truth) -------------------------

const dockerfilePath = "../Dockerfile"

var (
	fetchURLRe  = regexp.MustCompile(`fetch\s+(\S+)\s+"([^"]+)"`)
	grokChainRe = regexp.MustCompile(`(?m)^\s*\|\|\s*fetch\s+(\S+)\s+"([^"]+)"`)
)

func readDockerfile(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(dockerfilePath)
	if err != nil {
		t.Fatalf("read %s: %v", dockerfilePath, err)
	}
	return string(b)
}

func fetchStage(t *testing.T, dockerfile string) string {
	t.Helper()
	var stage []string
	in := false
	for _, l := range strings.Split(dockerfile, "\n") {
		if strings.HasPrefix(l, "FROM ") {
			if in {
				break
			}
			in = strings.Contains(l, "AS fetch")
			continue
		}
		if in {
			stage = append(stage, l)
		}
	}
	if len(stage) == 0 {
		t.Fatalf("Dockerfile: the fetch stage (FROM ... AS fetch) was not found")
	}
	return strings.Join(stage, "\n")
}

func helperLine(t *testing.T, stage string) string {
	t.Helper()
	for _, l := range strings.Split(stage, "\n") {
		if strings.Contains(l, "fetch() {") {
			return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(l), `\`))
		}
	}
	t.Fatalf("Dockerfile: the fetch() helper line was not found in the fetch stage")
	return ""
}

// tokenize splits a shell line on whitespace, keeping single- and
// double-quoted segments as one token with the quotes stripped.
func tokenize(line string) []string {
	var out []string
	var cur strings.Builder
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote = c
		case c == ' ' || c == '\t':
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func helperCurlFlags(t *testing.T, line string) []string {
	t.Helper()
	tok := tokenize(line)
	start, end := -1, -1
	for i, s := range tok {
		if s == "curl" {
			start = i
		}
		if s == "-o" {
			end = i
			break
		}
	}
	if start < 0 || end < 0 || end <= start {
		t.Fatalf("Dockerfile: could not locate the curl flags between 'curl' and '-o' in the helper line: %s", line)
	}
	return tok[start+1 : end]
}

func TestFetchHelperPinned(t *testing.T) {
	t.Parallel()
	stage := fetchStage(t, readDockerfile(t))
	flags := helperCurlFlags(t, helperLine(t, stage))
	if !equalStrings(flags, pinnedHelperFlags) {
		t.Fatalf("Dockerfile: the fetch helper flags are %v, want the pinned budget %v", flags, pinnedHelperFlags)
	}
	proto := -1
	for i := 0; i+1 < len(flags); i++ {
		if flags[i] == "--proto" {
			proto = i + 1
		}
	}
	if proto < 0 || flags[proto] != "=https" {
		t.Fatalf("Dockerfile: the fetch helper --proto pin must be '=https', got %v", flags)
	}
	urls := fetchURLRe.FindAllStringSubmatch(stage, -1)
	if len(urls) < 4 {
		t.Fatalf("Dockerfile: expected the four pinned artifacts' fetch URLs, found %d", len(urls))
	}
	for _, m := range urls {
		if !strings.HasPrefix(m[2], "https://") {
			t.Fatalf("Dockerfile: the fetch URL for %s is not https: %q", m[1], m[2])
		}
	}
	grok := grokChainRe.FindAllStringSubmatch(stage, -1)
	if len(grok) != 1 || grok[0][1] != "grok" || !strings.HasPrefix(grok[0][2], "https://") {
		t.Fatalf("Dockerfile: expected exactly one Grok fallback chain '|| fetch grok \"https://...\"', got %q", grok)
	}
	if !strings.Contains(stage, `fetch grok "https://x.ai/`) {
		t.Fatalf("Dockerfile: the Grok primary fetch from https://x.ai/ is missing")
	}
	if !strings.Contains(stage, `| sha256sum -c -`) {
		t.Fatalf("Dockerfile: the checksum gate '| sha256sum -c -' is missing from the fetch stage")
	}
	for _, l := range strings.Split(stage, "\n") {
		if strings.HasPrefix(l, "RUN ") {
			if !strings.Contains(l, "set -eu") {
				t.Fatalf("Dockerfile: the fetch stage RUN must keep the set -e form as in the file, got: %s", l)
			}
			return
		}
	}
	t.Fatalf("Dockerfile: the fetch stage has no RUN line")
}

// --- runtime budget (parsed from the Dockerfile, time values reduced) ----

type parsedBudget struct {
	Flags        []string // reduced curl flags, in Dockerfile order, minus --proto
	Retry        int
	RetryMaxTime int
	MaxTime      int
}

func reducedBudget(t *testing.T) parsedBudget {
	t.Helper()
	flags := helperCurlFlags(t, helperLine(t, fetchStage(t, readDockerfile(t))))
	have := map[string]string{}
	var out []string
	for i := 0; i < len(flags); i++ {
		f := flags[i]
		switch {
		case f == "--proto":
			i++ // dropped: the local servers speak http on 127.0.0.1;
			// TestFetchHelperPinned asserts the '=https' pin stays in the Dockerfile
		case f == "-fsSL" || f == "--retry-all-errors":
			out = append(out, f)
			have[f] = ""
		case i+1 < len(flags):
			out = append(out, f, flags[i+1])
			if rv, ok := reducedSeconds[f]; ok {
				out[len(out)-1] = strconv.Itoa(rv)
				have[f] = strconv.Itoa(rv)
			} else {
				have[f] = flags[i+1]
			}
			i++
		default:
			t.Fatalf("Dockerfile: the fetch helper flag %q has no value", f)
		}
	}
	b := parsedBudget{Flags: out}
	for _, f := range []string{"-fsSL", "--speed-limit", "--speed-time", "--max-time", "--retry", "--retry-delay", "--retry-max-time", "--retry-all-errors"} {
		v, ok := have[f]
		if !ok {
			t.Fatalf("Dockerfile: the fetch helper lacks %q — the transfer budget is not in place, the budget tests cannot run", f)
		}
		if f == "-fsSL" || f == "--retry-all-errors" {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("Dockerfile: the fetch helper value for %s is not a number: %q", f, v)
		}
		switch f {
		case "--max-time":
			b.MaxTime = n
		case "--retry":
			b.Retry = n
		case "--retry-max-time":
			b.RetryMaxTime = n
		}
	}
	return b
}

func budgetDeadline(b parsedBudget) time.Duration {
	return time.Duration(b.RetryMaxTime+b.MaxTime)*time.Second + ciSlack
}

// --- local servers ---------------------------------------------------------

type connRecorder struct {
	mu     sync.Mutex
	starts []time.Time
}

func (r *connRecorder) begin() {
	r.mu.Lock()
	r.starts = append(r.starts, time.Now())
	r.mu.Unlock()
}

func (r *connRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.starts)
}

// minGap is the shortest time between two attempts, or 0 with fewer than two.
func (r *connRecorder) minGap() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	var gap time.Duration
	for i := 1; i < len(r.starts); i++ {
		if d := r.starts[i].Sub(r.starts[i-1]); i == 1 || d < gap {
			gap = d
		}
	}
	return gap
}

func stallServer(t *testing.T, body []byte) (*httptest.Server, *connRecorder) {
	t.Helper()
	rec := &connRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.begin()
		w.Write(body)
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		select {
		case <-r.Context().Done(): // the client gave up
		case <-time.After(90 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// slowServer keeps sending at about 10 KiB/s, above --speed-limit.
func slowServer(t *testing.T) (*httptest.Server, *connRecorder) {
	t.Helper()
	return pacedServer(t, 1000, 100*time.Millisecond)
}

// trickleServer keeps sending at about 400 B/s: below --speed-limit 1024 but
// above the 1 B/s curl assumes when only --speed-time is given.
func trickleServer(t *testing.T) (*httptest.Server, *connRecorder) {
	t.Helper()
	return pacedServer(t, 100, 250*time.Millisecond)
}

// pacedServer sends size bytes every interval until the client gives up.
func pacedServer(t *testing.T, size int, every time.Duration) (*httptest.Server, *connRecorder) {
	t.Helper()
	rec := &connRecorder{}
	chunk := make([]byte, size)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.begin()
		fl, _ := w.(http.Flusher)
		for {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			fl.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(every):
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func partialThenGoodServer(t *testing.T, data []byte) (*httptest.Server, *connRecorder) {
	t.Helper()
	rec := &connRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.begin()
		n := rec.count()
		if n == 1 {
			w.Write(data[:1000])
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			select {
			case <-r.Context().Done():
			case <-time.After(90 * time.Second):
			}
			return
		}
		w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func notFoundServer(t *testing.T, body string) (*httptest.Server, *connRecorder) {
	t.Helper()
	rec := &connRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.begin()
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func goodServer(t *testing.T, data []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func deadPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a dead port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

// --- runners ----------------------------------------------------------------

func skipUnlessCurlAvailable(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fetch helper only runs in the Linux fetch stage of the image; the curl/sh budget tests are skipped on Windows")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("curl is not in PATH and CI is set: the fetch budget tests cannot run")
		}
		t.Skip("curl is not in PATH")
	}
}

func runCurl(t *testing.T, b parsedBudget, out, url string) (int, string, time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), budgetDeadline(b))
	defer cancel()
	args := append([]string{}, b.Flags...)
	args = append(args, "-o", out, url)
	cmd := exec.CommandContext(ctx, "curl", args...)
	var sb bytes.Buffer
	cmd.Stderr = &sb
	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)
	if ctx.Err() != nil {
		t.Fatalf("curl did not end within the reduced budget cap %s: the transfer budget guard failed (stderr: %s)", budgetDeadline(b), sb.String())
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), sb.String(), elapsed
	}
	if err != nil {
		t.Fatalf("curl run failed: %v", err)
	}
	return 0, sb.String(), elapsed
}

func fetchChainScript(b parsedBudget, out, primary, fallback string) string {
	// Same form as the Dockerfile RUN: the fetch() function wrapping curl
	// with the (reduced) flags, then the '|| fetch' fallback chain.
	return "set -eu\n" +
		"fetch() { curl " + strings.Join(b.Flags, " ") + ` -o "$@"; }` + "\n" +
		"fetch '" + out + "' '" + primary + "' || fetch '" + out + "' '" + fallback + "'\n"
}

func runShell(t *testing.T, b parsedBudget, script string) (int, string, time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), budgetDeadline(b))
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	// The context kills only the shell. Its curl child inherits the stderr
	// pipe, so Wait would block until curl ends; WaitDelay bounds that wait
	// for the test. The child is not killed: curl's own timers end it.
	cmd.WaitDelay = 5 * time.Second
	var sb bytes.Buffer
	cmd.Stderr = &sb
	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)
	if ctx.Err() != nil {
		t.Fatalf("the fetch chain did not end within the reduced budget cap %s (stderr: %s)", budgetDeadline(b), sb.String())
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), sb.String(), elapsed
	}
	if err != nil {
		t.Fatalf("sh run failed: %v", err)
	}
	return 0, sb.String(), elapsed
}

func sha256GateArgs(t *testing.T) []string {
	t.Helper()
	if p, err := exec.LookPath("sha256sum"); err == nil {
		return []string{p, "-c", "-"}
	}
	if p, err := exec.LookPath("shasum"); err == nil {
		return []string{p, "-a", "256", "-c", "-"}
	}
	t.Fatal("neither sha256sum nor shasum is in PATH: the checksum gate cannot run")
	return nil
}

// runChecksumGate runs the Dockerfile's checksum gate form: the pinned
// hex digest and the file name on stdin of sha256sum -c - (or
// shasum -a 256 -c - where the first is unavailable).
func runChecksumGate(t *testing.T, file, hexSum string) (int, string) {
	t.Helper()
	args := sha256GateArgs(t)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin = strings.NewReader(hexSum + "  " + file + "\n")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), buf.String()
	}
	if err != nil {
		t.Fatalf("checksum gate run failed: %v", err)
	}
	return 0, buf.String()
}

func assertChecksumPasses(t *testing.T, out string) {
	t.Helper()
	exit, gateOut := runChecksumGate(t, out, pinnedHex)
	if exit != 0 {
		t.Fatalf("the checksum gate must pass on the pinned bytes, exit %d: %s", exit, gateOut)
	}
}

// --- budget tests --------------------------------------------------------------

func TestFetchStalledTransferEndsInBudget(t *testing.T) {
	t.Parallel()
	skipUnlessCurlAvailable(t)
	b := reducedBudget(t)
	srv, rec := stallServer(t, bytes.Repeat([]byte("canary-stall-do-not-print"), 40))
	out := filepath.Join(t.TempDir(), "stall.bin")
	exit, stderr, elapsed := runCurl(t, b, out, srv.URL)
	if exit != 28 {
		t.Fatalf("stalled transfer: exit %d, want 28 (operation timeout); stderr: %s", exit, stderr)
	}
	if !strings.Contains(stderr, "Operation too slow") {
		t.Fatalf("stalled transfer: the stall detector (--speed-limit/--speed-time) must cut the attempt, got: %s", stderr)
	}
	n := rec.count()
	if n < 2 || n > b.Retry+1 {
		t.Fatalf("stalled transfer: the server saw %d attempts, want more than 1 and at most %d", n, b.Retry+1)
	}
	if elapsed > budgetDeadline(b) {
		t.Fatalf("stalled transfer took %s, beyond the reduced budget cap %s", elapsed, budgetDeadline(b))
	}
}

func TestFetchSlowTransferEndsInBudget(t *testing.T) {
	t.Parallel()
	skipUnlessCurlAvailable(t)
	b := reducedBudget(t)
	srv, rec := slowServer(t)
	out := filepath.Join(t.TempDir(), "slow.bin")
	exit, stderr, elapsed := runCurl(t, b, out, srv.URL)
	if exit != 28 {
		t.Fatalf("slow transfer: exit %d, want 28 (operation timeout); stderr: %s", exit, stderr)
	}
	// No assert on the curl message: a pause on a loaded machine can drop one
	// attempt below --speed-limit for the reduced 1 s window. Without
	// --max-time this server never ends, so runCurl's deadline fails the test.
	// Every attempt lasts --max-time here, so --retry-max-time is what ends
	// the retries before --retry runs out. The cap needs that premise: a pause
	// on a loaded machine can trip the stall detector, and shorter attempts
	// fit more starts in the window, so the count only binds when no attempt
	// ended early.
	maxAttempts := b.RetryMaxTime/(b.MaxTime+reducedSeconds["--retry-delay"]) + 2
	if n := rec.count(); n > maxAttempts && rec.minGap() >= time.Duration(b.MaxTime)*time.Second {
		t.Fatalf("slow transfer: the server saw %d attempts, want at most %d: --retry-max-time must end the retries", n, maxAttempts)
	}
	if elapsed > budgetDeadline(b) {
		t.Fatalf("slow transfer took %s, beyond the reduced budget cap %s", elapsed, budgetDeadline(b))
	}
}

func TestFetchTrickleBelowSpeedLimitIsCut(t *testing.T) {
	t.Parallel()
	skipUnlessCurlAvailable(t)
	b := reducedBudget(t)
	srv, _ := trickleServer(t)
	out := filepath.Join(t.TempDir(), "trickle.bin")
	exit, stderr, elapsed := runCurl(t, b, out, srv.URL)
	if exit != 28 {
		t.Fatalf("trickle: exit %d, want 28 (operation timeout); stderr: %s", exit, stderr)
	}
	if !strings.Contains(stderr, "Operation too slow") {
		t.Fatalf("trickle: --speed-limit must cut a transfer below it, got: %s", stderr)
	}
	if elapsed > budgetDeadline(b) {
		t.Fatalf("trickle took %s, beyond the reduced budget cap %s", elapsed, budgetDeadline(b))
	}
}

func TestFetchRetryReplacesPartialBytes(t *testing.T) {
	t.Parallel()
	skipUnlessCurlAvailable(t)
	b := reducedBudget(t)
	srv, rec := partialThenGoodServer(t, pinnedBytes)
	out := filepath.Join(t.TempDir(), "partial.bin")
	exit, stderr, _ := runCurl(t, b, out, srv.URL)
	if exit != 0 {
		t.Fatalf("retry of a partial stalled attempt must end in success, exit %d; stderr: %s", exit, stderr)
	}
	if n := rec.count(); n != 2 {
		t.Fatalf("want exactly 2 attempts (first partial+stalled, second full), the server saw %d", n)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read the downloaded file: %v", err)
	}
	if !bytes.Equal(got, pinnedBytes) {
		t.Fatalf("the final file is not the pinned bytes: got %d bytes, want %d", len(got), len(pinnedBytes))
	}
	assertChecksumPasses(t, out)
}

func TestFetchPrimaryFailsFallbackSucceeds(t *testing.T) {
	t.Parallel()
	skipUnlessCurlAvailable(t)
	b := reducedBudget(t)

	t.Run("primary stalled", func(t *testing.T) {
		t.Parallel()
		primary, _ := stallServer(t, bytes.Repeat([]byte("canary-stall-do-not-print"), 40))
		fallback := goodServer(t, pinnedBytes)
		out := filepath.Join(t.TempDir(), "grok.bin")
		exit, stderr, elapsed := runShell(t, b, fetchChainScript(b, out, primary.URL, fallback.URL))
		if exit != 0 {
			t.Fatalf("the '|| fetch' chain must end in success via the fallback, exit %d; stderr: %s", exit, stderr)
		}
		if elapsed > budgetDeadline(b) {
			t.Fatalf("the '|| fetch' chain took %s, beyond the reduced budget cap %s", elapsed, budgetDeadline(b))
		}
		assertChecksumPasses(t, out)
	})

	t.Run("primary 404", func(t *testing.T) {
		t.Parallel()
		primary, _ := notFoundServer(t, "canary-404-do-not-print")
		fallback := goodServer(t, pinnedBytes)
		out := filepath.Join(t.TempDir(), "grok404.bin")
		exit, stderr, _ := runShell(t, b, fetchChainScript(b, out, primary.URL, fallback.URL))
		if exit != 0 {
			t.Fatalf("the '|| fetch' chain must end in success via the fallback, exit %d; stderr: %s", exit, stderr)
		}
		assertChecksumPasses(t, out)
	})
}

func TestFetchFallbackAlteredBytesFail(t *testing.T) {
	t.Parallel()
	skipUnlessCurlAvailable(t)
	b := reducedBudget(t)
	primary, _ := notFoundServer(t, "canary-404-do-not-print")
	altered := bytes.Repeat([]byte("canary-altered-fallback-bytes"), 128) // != pinnedBytes
	fallback := goodServer(t, altered)
	out := filepath.Join(t.TempDir(), "grok-altered.bin")
	exit, stderr, _ := runShell(t, b, fetchChainScript(b, out, primary.URL, fallback.URL))
	if exit != 0 {
		t.Fatalf("the '|| fetch' chain must download the fallback bytes (the failure comes from the checksum gate), exit %d; stderr: %s", exit, stderr)
	}
	exitGate, gateOut := runChecksumGate(t, out, pinnedHex)
	if exitGate == 0 {
		t.Fatalf("the checksum gate must fail on altered fallback bytes, got exit 0")
	}
	if strings.Contains(gateOut, "canary-altered-fallback-bytes") {
		t.Fatalf("the altered body leaked into the checksum gate output: %s", gateOut)
	}
}

func TestFetchFailureModesDistinguished(t *testing.T) {
	t.Parallel()
	skipUnlessCurlAvailable(t)
	b := reducedBudget(t)
	dir := t.TempDir()

	// 1) connection refused.
	refused := "http://127.0.0.1:" + strconv.Itoa(deadPort(t)) + "/nope"
	exitRefused, stderrRefused, _ := runCurl(t, b, filepath.Join(dir, "refused.bin"), refused)

	// 2) stalled transfer.
	stall, _ := stallServer(t, bytes.Repeat([]byte("canary-stall-do-not-print"), 40))
	exitStall, stderrStall, _ := runCurl(t, b, filepath.Join(dir, "stall.bin"), stall.URL)

	// 3) object not found (404 with -f).
	notfound, rec404 := notFoundServer(t, "canary-404-do-not-print")
	exit404, stderr404, _ := runCurl(t, b, filepath.Join(dir, "notfound.bin"), notfound.URL)

	// 4) checksum mismatch: the download succeeds, the gate must fail.
	badBytes := bytes.Repeat([]byte("canary-bad-bytes-do-not-print"), 32)
	bad := goodServer(t, badBytes)
	exitBad, _, _ := runCurl(t, b, filepath.Join(dir, "bad.bin"), bad.URL)
	if exitBad != 0 {
		t.Fatalf("the bad-bytes download must succeed (exit %d); the failure must come from the checksum gate", exitBad)
	}
	exitGate, gateOut := runChecksumGate(t, filepath.Join(dir, "bad.bin"), pinnedHex)

	codes := map[string]int{
		"connection refused": exitRefused,
		"stalled transfer":   exitStall,
		"object not found":   exit404,
		"checksum mismatch":  exitGate,
	}
	owner := map[int]string{}
	for name, code := range codes {
		if prev, seen := owner[code]; seen {
			t.Fatalf("the failure modes must exit with distinct codes: %q and %q both exit %d", prev, name, code)
		}
		owner[code] = name
	}
	if exitRefused != 7 {
		t.Fatalf("connection refused must exit 7 (curl could not connect), got %d", exitRefused)
	}
	if exitStall != 28 {
		t.Fatalf("stalled transfer must exit 28 (operation timeout), got %d", exitStall)
	}
	if exit404 != 22 {
		t.Fatalf("object not found must exit 22 (HTTP error with -f), got %d", exit404)
	}
	if n := rec404.count(); n < 2 {
		t.Fatalf("object not found: the server saw %d attempt, --retry-all-errors must retry a 404", n)
	}
	if exitGate == 0 {
		t.Fatalf("the checksum gate must fail on different bytes, got exit 0")
	}
	if strings.Contains(stderrRefused, "canary") || strings.Contains(stderrStall, "canary-stall-do-not-print") {
		t.Fatalf("the stalled transfer server body leaked into curl stderr: %s", stderrStall)
	}
	if strings.Contains(stderr404, "canary-404-do-not-print") {
		t.Fatalf("the 404 response body leaked into curl stderr: %s", stderr404)
	}
	if strings.Contains(gateOut, "canary-bad-bytes-do-not-print") {
		t.Fatalf("the downloaded body leaked into the checksum gate output: %s", gateOut)
	}
}

func TestFetchGoodDownloadProducesPinnedBytes(t *testing.T) {
	t.Parallel()
	skipUnlessCurlAvailable(t)
	b := reducedBudget(t)
	srv := goodServer(t, pinnedBytes)
	out := filepath.Join(t.TempDir(), "good.bin")
	exit, stderr, _ := runCurl(t, b, out, srv.URL)
	if exit != 0 {
		t.Fatalf("good download must succeed, exit %d; stderr: %s", exit, stderr)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read the downloaded file: %v", err)
	}
	if !bytes.Equal(got, pinnedBytes) {
		t.Fatalf("good download: got %d bytes, want exactly the %d pinned bytes", len(got), len(pinnedBytes))
	}
	sum := sha256.Sum256(got)
	if hex.EncodeToString(sum[:]) != pinnedHex {
		t.Fatalf("good download: SHA-256 is %s, want the test-computed %s", sum, pinnedHex)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
