package spawn

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/hc"
	backend "github.com/djalmajr/soho/internal/hcbackend"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestSpawnEnvSnapshotBeforeStarter(t *testing.T) {
	cwd, bin, state, env, ctx := hcSpawnFixture(t, "worker", "pi")
	env["SELECTED_CANARY"] = "FAKE admitted value"
	rules := append(hcFixtureRules(), fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: hcReadyWorker})
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	conn := newFakeSpawnRPCConn()
	client := hc.New(env)
	client.Connect = conn.Connect
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	code, message := configuredExit(func() {
		cmdSpawnWithHcStarter([]string{"worker", "--fresh", "--env-from", "SELECTED_CANARY"}, ctx, env, cwd, func(c *core.Config, o *spawnOptions, k string, e platform.Env, a []string) {
			e["SELECTED_CANARY"] = "FAKE changed by starter"
			startHcAgentUsingServices(c, o, k, e, a, backend.NewWithClient(e, client))
		})
	})
	if code != 0 || message != "" {
		t.Fatalf("spawn refused: %d %s", code, message)
	}
	_, params := parseTerminalRunReq(t, conn.getWritten())
	want := map[string]any{"SOHO_TRANSPORT": "hc", "SELECTED_CANARY": "FAKE admitted value", "SOHO_REGISTRY_DIR": filepath.Join(state, "sX"), "SOHO_AGENT": "worker"}
	if !reflect.DeepEqual(params["env"], want) {
		t.Fatal("starter changed admitted snapshot")
	}
}

func envFixtureState(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			files[p] = string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestSpawnEnvRPC(t *testing.T) {
	for _, mode := range []string{"absent", "literal", "snapshot", "max16", "maxbytes", "duplicates", "delimiter-value", "native-boundary", "option-shaped-value", "lowercase"} {
		t.Run(mode, func(t *testing.T) {
			cwd, bin, state, env, ctx := hcSpawnFixture(t, "worker", "pi")
			env["USERPROFILE"], env["XDG_CONFIG_HOME"] = env["HOME"], env["HOME"]
			env["SELECTED_CANARY"] = "FAKE selected '$`\\\n雪�=literal"
			env["UNRELATED_CANARY"] = "FAKE unrelated private"
			if mode == "maxbytes" {
				env["SELECTED_CANARY"] = strings.Repeat("x", 65536)
			}
			args := []string{"worker", "--fresh", "--model", "test/model", "--effort", "high", "--approvals", "ask"}
			if mode == "delimiter-value" {
				args = append(args, "--tab-label", "--")
			}
			if mode == "option-shaped-value" {
				args = append(args, "--tab-label", "--env-from")
			}
			want := map[string]any{"SOHO_TRANSPORT": "hc", "SOHO_REGISTRY_DIR": filepath.Join(state, "sX"), "SOHO_AGENT": "worker"}
			if mode != "absent" && mode != "native-boundary" && mode != "option-shaped-value" {
				args = append(args, "--env-from", "SELECTED_CANARY")
				want["SELECTED_CANARY"] = env["SELECTED_CANARY"]
			}
			if mode == "lowercase" {
				env["selected_canary"] = "FAKE lowercase selected"
				args[len(args)-1] = "selected_canary"
				delete(want, "SELECTED_CANARY")
				want["selected_canary"] = env["selected_canary"]
			}
			if mode == "native-boundary" {
				args = append(args, "--", "--env-from", "NATIVE_ONLY")
			}
			if mode == "duplicates" {
				for i := 0; i < 20; i++ {
					args = append(args, "--env-from", "SELECTED_CANARY")
				}
			}
			if mode == "max16" {
				for i := 0; i < 15; i++ {
					name := fmt.Sprintf("CANARY_%d", i)
					env[name] = "fake"
					want[name] = "fake"
					args = append(args, "--env-from", name)
				}
			}
			beforeEnv, beforeArgs := env.Clone(), append([]string(nil), args...)
			rules := append(hcFixtureRules(), fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: hcReadyWorker})
			if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
				t.Fatal(err)
			}
			conn := newFakeSpawnRPCConn()
			if mode == "snapshot" {
				conn.beforeRead = func(_ []byte) { env["SELECTED_CANARY"] = "FAKE changed after admission" }
			}
			out, stderr, calls := spawnHcWithConn(t, bin, args, ctx, env, cwd, conn)
			_, params := parseTerminalRunReq(t, conn.getWritten())
			if mode == "native-boundary" {
				argv, ok := params["argv"].([]any)
				if !ok || len(argv) < 2 || argv[len(argv)-2] != "--env-from" || argv[len(argv)-1] != "NATIVE_ONLY" {
					t.Fatalf("native boundary lost arguments: %v", params["argv"])
				}
			}
			if !reflect.DeepEqual(params["env"], want) {
				t.Fatal("RPC environment differs from explicit snapshot")
			}
			if !reflect.DeepEqual(args, beforeArgs) {
				t.Fatal("caller argv changed")
			}
			if mode == "snapshot" {
				env["SELECTED_CANARY"] = beforeEnv["SELECTED_CANARY"]
			}
			if !reflect.DeepEqual(env, beforeEnv) {
				t.Fatal("caller environment changed")
			}
			assertHcOnlyReads(t, bin, calls)
			public := out.String() + stderr.String() + fmt.Sprint(params["argv"]) + fmt.Sprint(calls)
			for _, b := range envFixtureState(t, state) {
				public += b
			}
			if strings.Contains(public, beforeEnv["SELECTED_CANARY"]) || strings.Contains(public, beforeEnv["UNRELATED_CANARY"]) {
				t.Fatal("private canary leaked into observable output/storage")
			}
			if !conn.closed || conn.reapCount != 1 {
				t.Fatal("RPC fixture not closed and reaped")
			}
		})
	}
}

func TestSpawnEnvRefusalsBeforeEffects(t *testing.T) {
	for _, delimiterValue := range []bool{false, true} {
		label := "ordinary"
		if delimiterValue {
			label = "delimiter-value"
		}
		t.Run(label, func(t *testing.T) {
			for _, mode := range []string{"missing-argument", "empty-name", "digit", "equals", "unicode-name", "nul-name", "invalid-name-utf8", "reserved-soho", "reserved-hc", "reserved-herdr", "missing", "empty", "nul", "utf8", "large", "max17", "no-fresh", "reuse", "fresh-then-reuse", "tui", "opencode", "codex", "case-alias", "case-alias-reversed"} {
				t.Run(mode, func(t *testing.T) {
					cwd, bin, state, env, ctx := hcSpawnFixture(t, "worker", "pi")
					args := []string{"worker", "--fresh", "--env-from", "SELECTED_CANARY"}
					env["SELECTED_CANARY"] = "FAKE private canary"
					switch mode {
					case "case-alias", "case-alias-reversed":
						env["selected_canary"] = "FAKE alias value"
						args = append(args, "--env-from", "selected_canary")
						if mode == "case-alias-reversed" {
							args[3], args[5] = args[5], args[3]
						}
					case "missing-argument":
						args = args[:len(args)-1]
					case "empty-name":
						args[3] = ""
					case "digit":
						args[3] = "1KEY"
					case "equals":
						args[3] = "KEY=value"
					case "unicode-name":
						args[3] = "雪"
					case "nul-name":
						args[3] = "KEY\x00"
					case "invalid-name-utf8":
						args[3] = "KEY\xff"
					case "reserved-soho":
						args[3] = "sOhO_KEY"
					case "reserved-hc":
						args[3] = "hC_KEY"
					case "reserved-herdr":
						args[3] = "HeRdR_KEY"
					case "missing":
						delete(env, "SELECTED_CANARY")
					case "empty":
						env["SELECTED_CANARY"] = ""
					case "nul":
						env["SELECTED_CANARY"] = "fake\x00canary"
					case "utf8":
						env["SELECTED_CANARY"] = "fake\xffcanary"
					case "large":
						env["SELECTED_CANARY"] = strings.Repeat("x", 65537)
					case "max17":
						for i := 0; i < 16; i++ {
							name := fmt.Sprintf("CANARY_%d", i)
							env[name] = "fake"
							args = append(args, "--env-from", name)
						}
					case "no-fresh":
						args = append(args[:1], args[2:]...)
					case "reuse":
						args[1] = "--reuse"
					case "fresh-then-reuse":
						args = append(args, "--reuse")
					case "tui":
						env["SOHO_TRANSPORT"] = "herdr"
					case "opencode", "codex":
						args = append(args, "--kind", mode)
					}
					if delimiterValue {
						args = append([]string{args[0], "--tab-label", "--"}, args[1:]...)
					}
					before := envFixtureState(t, state)
					conn := newFakeSpawnRPCConn()
					exit, out, stderr, calls := spawnHcExitWithConn(t, bin, args, ctx, env, cwd, conn)
					if exit == nil || exit.Code != 2 || exit.Msg != spawnEnvRefusal || exit.Friction {
						t.Fatalf("incorrect value-free refusal: %+v", exit)
					}
					if out.Len() != 0 || stderr.Len() != 0 || len(calls) != 0 || conn.getConnectCount() != 0 {
						t.Fatal("refusal performed output or backend effects")
					}
					if !reflect.DeepEqual(before, envFixtureState(t, state)) {
						t.Fatal("refusal changed persisted state")
					}
					herdrCalls, err := fakecli.ReadCalls(filepath.Join(bin, "herdr.calls.jsonl"))
					if err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
					if len(herdrCalls) != 0 {
						t.Fatal("refusal invoked TUI backend")
					}
				})
			}
		})
	}
}
