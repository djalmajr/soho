package spawn

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func e2eFixture(t *testing.T, mode string) spawnFixture {
	t.Helper()
	f := newSpawnFixture(t, freshSpawnRules())
	configureSpawnFixture(t, &f)
	f.env["SOHO_LAYOUT"] = mode
	f.env["HERDR_PANE_ID"] = "p1"
	f.env["SOHO_E2E_PORT_BASE"] = "47000"
	f.env["SOHO_MAX_WORKERS"] = "0"
	return f
}
func injectE2EHost(t *testing.T, fn func(int) error) {
	t.Helper()
	old := e2ePortAvailable
	e2ePortAvailable = fn
	t.Cleanup(func() { e2ePortAvailable = old })
}
func e2eSpawn(t *testing.T, f spawnFixture, name string, flags ...string) (map[string]any, []fakecli.Call) {
	t.Helper()
	args := append([]string{"worker", "--name", name}, flags...)
	if strings.Contains(" "+strings.Join(flags, " ")+" ", " --fresh ") {
		rules := []fakecli.Rule{
			{Argv: []string{"pane", "split"}, ArgvPrefix: true, Stdout: `{"result":{"pane":{"pane_id":"p-` + name + `"}}}`},
			{Argv: []string{"tab", "create"}, ArgvPrefix: true, Stdout: `{"result":{"tab":{"tab_id":"t-` + name + `","label":"herd"},"root_pane":{"pane_id":"p-` + name + `"}}}`},
		}
		writeWorkerStateRules(t, f, append(rules, freshSpawnRules()...))
	}
	if f.env.Get("SOHO_LAYOUT") == "tab" {
		args = append(args, "--tab-label", name)
	} else {
		args = append(args, "--direction", "right")
	}
	code, out, stderr, calls := runCmdSpawn(t, f, args)
	if code != 0 {
		t.Fatalf("spawn code=%d out=%s stderr=%s", code, out, stderr)
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(out), &value); err != nil {
		t.Fatal(err)
	}
	return value, calls
}
func assertE2ECreatePorts(t *testing.T, calls []fakecli.Call, want ...string) {
	t.Helper()
	got := []string{}
	for _, c := range calls {
		if len(c.Argv) < 2 {
			continue
		}
		if c.Argv[0] == "pane" && c.Argv[1] == "split" || c.Argv[0] == "tab" && c.Argv[1] == "create" {
			port := ""
			for i := 0; i+1 < len(c.Argv); i++ {
				if c.Argv[i] == "--env" && strings.HasPrefix(c.Argv[i+1], "E2E_PORT=") {
					port = strings.TrimPrefix(c.Argv[i+1], "E2E_PORT=")
				}
			}
			got = append(got, port)
		}
	}
	if len(got) != len(want) || strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("pane ports=%v want=%v calls=%v", got, want, calls)
	}
}
func TestE2EPortSpawnReservations(t *testing.T) {
	for _, mode := range []string{"split", "tab"} {
		t.Run(mode, func(t *testing.T) {
			probes := []int{}
			injectE2EHost(t, func(p int) error { probes = append(probes, p); return nil })
			f := e2eFixture(t, mode)
			a, _ := e2eSpawn(t, f, "first", "--fresh")
			b, calls := e2eSpawn(t, f, "second", "--fresh")
			if a["e2e_port"] != "47000" || b["e2e_port"] != "47001" {
				t.Fatalf("a=%v b=%v", a, b)
			}
			assertE2ECreatePorts(t, calls, "47000", "47001")
			if !reflect.DeepEqual(probes, []int{47000, 47001}) {
				t.Fatalf("probes=%v", probes)
			}
			sd := filepath.Join(f.state, "ws")
			for _, name := range []string{"first", "second"} {
				fields := strings.Split(core.RosterLine(sd, name), "\t")
				if len(fields) != 17 || fields[15] != "inherit" || fields[16] != map[string]string{"first": "47000", "second": "47001"}[name] {
					t.Fatalf("row=%v", fields)
				}
			}
			core.RosterRemove(sd, "first") // Same reservation boundary used by release; public release is tested in cli.
			c, calls := e2eSpawn(t, f, "third", "--fresh")
			if c["e2e_port"] != "47000" {
				t.Fatalf("released port not reused: %v", c)
			}
			assertE2ECreatePorts(t, calls, "47000", "47001", "47000")
			t.Logf("mode=%s ports=%v,%v,%v; argv and roster verified", mode, a["e2e_port"], b["e2e_port"], c["e2e_port"])
		})
	}
}
func TestE2EPortHostCheck(t *testing.T) {
	probes := []int{}
	injectE2EHost(t, func(p int) error {
		probes = append(probes, p)
		if p == 47000 {
			return syscall.EADDRINUSE
		}
		return nil
	})
	f := e2eFixture(t, "split")
	value, calls := e2eSpawn(t, f, "worker", "--fresh")
	if value["e2e_port"] != "47001" || !reflect.DeepEqual(probes, []int{47000, 47001}) {
		t.Fatalf("result=%v probes=%v", value, probes)
	}
	assertE2ECreatePorts(t, calls, "47001")
}
func TestE2EPortReuseKeepsReservation(t *testing.T) {
	probes := 0
	injectE2EHost(t, func(int) error { probes++; return nil })
	rules := append([]fakecli.Rule{idleHerdrRule("worker")}, freshSpawnRules()...)
	f := newSpawnFixture(t, rules)
	configureSpawnFixture(t, &f)
	f.env["SOHO_E2E_PORT_BASE"] = "48000"
	row := strings.Join([]string{"worker", "p-worker", "grok", "worker", "xai", "1", f.cwd, "now", "grok-4.7", "ask", "worker", "", "", "", "high", "inherit", "47000"}, "\t")
	f.roster(t, row)
	before, err := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	value, calls := e2eSpawn(t, f, "worker", "--reuse")
	after, err := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	if value["reused"] != true || value["e2e_port"] != "47000" || probes != 0 || string(after) != string(before) {
		t.Fatalf("value=%v probes=%d roster changed=%t", value, probes, string(after) != string(before))
	}
	assertNoStart(t, calls)
	assertE2ECreatePorts(t, calls)
}
func TestE2EPortOffPreservesSpawn(t *testing.T) {
	injectE2EHost(t, func(int) error { t.Fatal("off mode probed host"); return nil })
	f := e2eFixture(t, "split")
	delete(f.env, "SOHO_E2E_PORT_BASE")
	f.env["E2E_PORT"] = "49999"
	value, calls := e2eSpawn(t, f, "worker", "--fresh")
	if _, ok := value["e2e_port"]; ok {
		t.Fatalf("off result=%v", value)
	}
	assertE2ECreatePorts(t, calls, "")
	fields := strings.Split(core.RosterLine(filepath.Join(f.state, "ws"), "worker"), "\t")
	if len(fields) != 16 || fields[15] != "inherit" {
		t.Fatalf("off roster=%v", fields)
	}
	// Exact pre-feature pane argv, including only its existing registry export.
	want := []string{"pane", "split", "p1", "--direction", "right", "--cwd", f.cwd, "--no-focus", "--ratio", "0.5", "--env", "SOHO_REGISTRY_DIR=" + filepath.Join(f.state, "ws")}
	for _, call := range calls {
		if len(call.Argv) > 1 && call.Argv[0] == "pane" && call.Argv[1] == "split" {
			if !reflect.DeepEqual(call.Argv, want) {
				t.Fatalf("off argv=%v want=%v", call.Argv, want)
			}
			t.Logf("off-argv=%q", strings.ReplaceAll(strings.Join(call.Argv, "\x00"), f.root, "$ROOT"))
		}
	}
	fields[7] = "STAMP"
	t.Logf("off-row=%q", strings.ReplaceAll(strings.Join(fields, "\t"), f.root, "$ROOT"))
}

func TestE2EPortGivenPaneDoesNotReserve(t *testing.T) {
	injectE2EHost(t, func(int) error { t.Fatal("given pane probed host"); return nil })
	f := e2eFixture(t, "split")
	value, calls := e2eSpawn(t, f, "worker", "--pane", "existing", "--fresh")
	if _, ok := value["e2e_port"]; ok {
		t.Fatalf("given pane result=%v", value)
	}
	assertE2ECreatePorts(t, calls)
}

func TestE2EPortExistingHerdRootDoesNotClaimExport(t *testing.T) {
	injectE2EHost(t, func(int) error { return nil })
	f := e2eFixture(t, "tab")
	sd := core.StateDir(f.ctx, f.env, f.cwd)
	if err := os.WriteFile(filepath.Join(sd, "herd-tab"), []byte("t-herd\therd\tauto\n"), 0600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr, calls := runCmdSpawn(t, f, []string{"worker", "--name", "worker", "--fresh"})
	if code != 0 || strings.Contains(out, `"e2e_port"`) {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
	}
	assertE2ECreatePorts(t, calls)
	if port := core.RosterE2EPort(strings.Split(core.RosterLine(sd, "worker"), "\t")); port != "" {
		t.Fatalf("unexported port=%s", port)
	}
}
func TestE2EPortNativeListenerSkipped(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:47000")
	if err != nil {
		if os.IsPermission(err) || errors.Is(err, syscall.EPERM) {
			t.Skipf("[env] localhost listen refused: %v; rerun on host", err)
		}
		t.Skipf("[env] fixture port 47000 unavailable: %v", err)
	}
	t.Cleanup(func() { listener.Close() })
	f := e2eFixture(t, "split")
	value, calls := e2eSpawn(t, f, "worker", "--fresh")
	if value["e2e_port"] != "47001" {
		t.Fatalf("held listener was not skipped: %v", value)
	}
	assertE2ECreatePorts(t, calls, "47001")
}

func TestE2EPortNativeTwoSpawns(t *testing.T) {
	// Confirm both exact fixture ports are available before asserting them.
	for _, address := range []string{"127.0.0.1:47000", "127.0.0.1:47001"} {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			if os.IsPermission(err) || errors.Is(err, syscall.EPERM) {
				t.Skipf("[env] localhost listen refused: %v; rerun on host", err)
			}
			t.Skipf("[env] exact fixture port unavailable: %v", err)
		}
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
	}
	f := e2eFixture(t, "split")
	a, _ := e2eSpawn(t, f, "first", "--fresh")
	b, calls := e2eSpawn(t, f, "second", "--fresh")
	if a["e2e_port"] != "47000" || b["e2e_port"] != "47001" {
		t.Fatalf("a=%v b=%v", a, b)
	}
	assertE2ECreatePorts(t, calls, "47000", "47001")
}
func TestE2EPortScanBound(t *testing.T) {
	probes := []int{}
	injectE2EHost(t, func(p int) error { probes = append(probes, p); return syscall.EADDRINUSE })
	f := e2eFixture(t, "split")
	code, out, stderr, calls := runCmdSpawn(t, f, []string{"worker", "--name", "worker", "--fresh"})
	if code != 4 || out != "" || len(probes) != 200 || probes[0] != 47000 || probes[199] != 47199 {
		t.Fatalf("code=%d out=%q stderr=%q probes=%v", code, out, stderr, probes)
	}
	assertE2ECreatePorts(t, calls)
	if core.RosterLine(filepath.Join(f.state, "ws"), "worker") != "" {
		t.Fatal("failed reservation wrote a worker")
	}
	if _, err := os.Stat(filepath.Join(f.state, "ws", "e2e-ports.lock")); !os.IsNotExist(err) {
		t.Fatalf("lock cleanup: %v", err)
	}
}
