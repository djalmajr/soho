package wait

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestReview546HistoryAndComposerBoundary(t *testing.T) {
	for _, line := range []string{"Elapsed 7.0s", "Context 20% use…", "Pursuing goal (1d 19h 23m)", "↑160k", "59.5%/262k", "── ⠴ Working ──", "125 tokens", "Working (12s)", "  indented output  "} {
		a := line + "\nsubstantive output\n› draft\nfooter"
		b := "changed " + a
		if idleFingerprint(a, "codex") == idleFingerprint(b, "codex") {
			t.Errorf("history line lost: %q", line)
		}
		if idleFingerprint(a, "codex") != idleFingerprint(strings.Replace(a, "draft\nfooter", "another draft\nanother footer", 1), "codex") {
			t.Errorf("composer/footer retained: %q", line)
		}
	}
	pi := review546Pi(t)
	if idleFingerprint(pi, "pi") == idleFingerprint(strings.Replace(pi, "Steering: Read the file", "Steering: Another queued message", 1), "pi") {
		t.Fatal("queued content above the composer lost")
	}
}

func review546Pi(t *testing.T) string {
	f, e := parser.ParseFile(token.NewFileSet(), "../provider/arrival_working_border_test.go", nil, 0)
	if e != nil {
		t.Fatal(e)
	}
	values := map[string]string{}
	var eval func(ast.Expr) string
	eval = func(x ast.Expr) string {
		switch n := x.(type) {
		case *ast.BasicLit:
			s, e := strconv.Unquote(n.Value)
			if e != nil {
				t.Fatal(e)
			}
			return s
		case *ast.Ident:
			return values[n.Name]
		case *ast.BinaryExpr:
			return eval(n.X) + eval(n.Y)
		case *ast.CallExpr:
			count, e := strconv.Atoi(n.Args[1].(*ast.BasicLit).Value)
			if e != nil {
				t.Fatal(e)
			}
			return strings.Repeat(eval(n.Args[0]), count)
		}
		t.Fatalf("unexpected capture expression %T", x)
		return ""
	}
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, s := range g.Specs {
			v, ok := s.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, n := range v.Names {
				if n.Name == "piWorkingBorder" || n.Name == "piSteerT1Screen" {
					values[n.Name] = eval(v.Values[i])
				}
			}
		}
	}
	if !strings.Contains(values["piSteerT1Screen"], "Elapsed 7.0s") {
		t.Fatal("pi capture anchor")
	}
	return values["piSteerT1Screen"]
}

func review546Run(t *testing.T, kind string, polls int, step time.Duration, screen func(int) string) (int, int, string, string) {
	t.Helper()
	root := t.TempDir()
	sd, bin := filepath.Join(root, "state"), filepath.Join(root, "bin")
	if e := os.MkdirAll(filepath.Join(sd, "wait"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte("worker\tp0a\t"+kind+"\timplementer\n"), 0600); e != nil {
		t.Fatal(e)
	}
	var rules []fakecli.Rule
	for i := 1; i <= polls; i++ {
		rules = append(rules, fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: i, Stdout: `{"result":{"agent":{"agent_status":"working","state_change_seq":1}}}`})
	}
	rules = append(rules, fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":2}}}`})
	for i := 1; i <= polls+5; i++ {
		rules = append(rules, fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: i, Stdout: screen(i)})
	}
	rules = append(rules, fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "20"}, Stdout: "command output"}, fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40"}, Stdout: "command output"})
	if _, e := fakecli.Install(t, bin, "herdr", rules); e != nil {
		t.Fatal(e)
	}
	env := platform.Env{"PATH": bin, "SOHO_FAKECLI_CONFIG": bin, "GORACE": "atexit_sleep_ms=0", "SOHO_WAIT_POLL_MS": "1", "HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root, "HERDR_SOCKET_PATH": filepath.Join(root, "none.sock")}
	calls := func() []fakecli.Call {
		cs, e := fakecli.ReadCalls(filepath.Join(bin, "herdr.calls.jsonl"))
		if e != nil && !os.IsNotExist(e) {
			t.Fatal(e)
		}
		return cs
	}
	oldNow, oldOut, oldErr, oldFriction := platform.Now, platform.Stdout, platform.Stderr, frictionLogFile
	var out, stderr bytes.Buffer
	friction := filepath.Join(root, "friction.log")
	platform.Stdout, platform.Stderr = &out, &stderr
	SetFrictionLogFile(friction)
	platform.Now = func() time.Time {
		n := 0
		for _, c := range calls() {
			if strings.Join(c.Argv, " ") == "agent get worker" {
				n++
			}
		}
		return time.Unix(1700000000, 0).Add(time.Duration(n) * step)
	}
	defer func() {
		platform.Now, platform.Stdout, platform.Stderr = oldNow, oldOut, oldErr
		SetFrictionLogFile(oldFriction)
	}()
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{"idle_alert_minutes": {Value: "20"}, "stuck_warn_minutes": {Value: "0"}, "settled_grace": {Value: "0"}}}
	code := WaitFor([]string{"worker"}, sd, ctx, env, float64((100*time.Minute)/time.Millisecond), false, root)
	if code != 6 {
		t.Fatalf("did not settle: exit=%d out=%s err=%s", code, out.String(), stderr.String())
	}
	for _, c := range calls() {
		if len(c.Argv) > 1 && (c.Argv[1] == "prompt" || c.Argv[1] == "send-keys") {
			t.Fatalf("input: %v", c.Argv)
		}
	}
	var obj map[string]any
	if e := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &obj); e != nil {
		t.Fatal(e)
	}
	count := strings.Count(stderr.String(), "has been working for")
	logged, e := os.ReadFile(friction)
	if e != nil && !os.IsNotExist(e) {
		t.Fatal(e)
	}
	if strings.Count(string(logged), "has been working for") != count {
		t.Fatalf("friction/stderr discrepancy")
	}
	if count > 0 && obj["idle_alerts"] != float64(count) {
		t.Fatal("count missing from JSON")
	}
	if count == 0 && obj["idle_alerts"] != nil {
		t.Fatal("quiet JSON changed")
	}
	t.Logf("exit=%d alerts=%d friction_alerts=%d JSON=%s stderr=%q; zero prompt/send-keys", code, count, strings.Count(string(logged), "has been working for"), out.String(), stderr.String())
	return code, count, out.String(), stderr.String()
}

func TestReview546KnownVolatileFrames(t *testing.T) {
	raw, e := os.ReadFile("../peer/testdata/evidence-rc11-codex-history.txt")
	if e != nil {
		t.Fatal(e)
	}
	codex, pi := string(raw), review546Pi(t)
	cases := []struct{ name, kind, frame, from, to string }{
		{"codex-clipped-context", "codex", codex, "Context 20% use…", "Context 21% use…"},
		{"codex-goal-elapsed", "codex", codex, "(1d 19h 23m)", "(1d 19h 24m)"},
		{"pi-border-spinner", "pi", pi, "⠴ Working", "⠦ Working"},
		{"pi-elapsed", "pi", pi, "Elapsed 7.0s", "Elapsed 8.0s"},
		{"pi-input-counter", "pi", pi, "↑160k", "↑161k"},
		{"pi-context-counter", "pi", pi, "59.5%/262k", "59.6%/262k"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changed := strings.Replace(tc.frame, tc.from, tc.to, 1)
			if changed == tc.frame {
				t.Fatal("fixture anchor")
			}
			same := idleFingerprint(tc.frame, tc.kind) == idleFingerprint(changed, tc.kind)
			t.Logf("fixture-only mutation %q -> %q fingerprint_equal=%t", tc.from, tc.to, same)
			_, count, _, _ := review546Run(t, tc.kind, 27, time.Minute, func(i int) string {
				if i%2 == 0 {
					return changed
				}
				return tc.frame
			})
			if !same || count != 1 {
				t.Errorf("volatile-only working screen must alert once: fingerprint_equal=%t alerts=%d", same, count)
			}
		})
	}
}

func TestReview546SubstantiveCounterPath(t *testing.T) {
	for _, tokenName := range []bool{true, false} {
		t.Run(fmt.Sprint(tokenName), func(t *testing.T) {
			screen := func(i int) string {
				if tokenName {
					return fmt.Sprintf("command output\nprocessed fixtures/%d tokens.json", i)
				}
				return fmt.Sprintf("command output\nprocessed fixtures/item%d.json", i)
			}
			same := idleFingerprint(screen(1), "claude") == idleFingerprint(screen(2), "claude")
			t.Logf("before=%q after=%q fingerprint_equal=%t", screen(1), screen(2), same)
			_, count, _, _ := review546Run(t, "claude", 27, time.Minute, screen)
			if same || count != 0 {
				t.Errorf("changing tool-output filenames must reset: equal=%t alerts=%d", same, count)
			}
		})
	}
}

func TestReview546ObserveAlreadyReadProgress(t *testing.T) {
	for _, step := range []time.Duration{30 * time.Second, time.Minute} {
		t.Run(step.String(), func(t *testing.T) {
			_, count, _, _ := review546Run(t, "claude", 44, step, func(i int) string {
				if i%2 == 0 {
					return fmt.Sprintf("command output\nprocessed item %d", i)
				}
				return "command output\nwaiting on command"
			})
			if count != 0 {
				t.Errorf("already-read substantive output changes on every working poll must reset timer: step=%s alerts=%d", step, count)
			}
		})
	}
}
