package spawn

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const reflectedEnvValue = "s3cr3t-VALUE-0123456789"

func TestSpawnEnvReflectedOutput(t *testing.T) {
	for _, mode := range []string{"message", "hint", "blocked-diagnostic", "blocked-screen", "ready-screen", "overlap", "four-bytes", "common-word", "multiline-control", "many"} {
		t.Run(mode, func(t *testing.T) {
			cwd, bin, state, env, ctx := hcSpawnFixture(t, "worker", "pi")
			env["USERPROFILE"], env["XDG_CONFIG_HOME"] = env["HOME"], env["HOME"]
			value := reflectedEnvValue
			if mode == "four-bytes" {
				value = "Ab9!"
			}
			if mode == "common-word" {
				// An ordinary word reflected by the backend is still replaced. A value
				// that coincides with soho's own fixed text (a role or agent name) is
				// outside the guarantee: that text does not come from the variable.
				value = "orchard"
			}
			if mode == "multiline-control" {
				value = "first\nsecond\t\x1b[31m"
			}
			env["SELECTED_CANARY"] = value
			args := []string{"worker", "--fresh", "--env-from", "SELECTED_CANARY"}
			if mode == "overlap" {
				env["SHORT_CANARY"] = "s3cr3t-VALUE"
				args = append(args, "--env-from", "SHORT_CANARY")
			}
			if mode == "many" {
				for i := 0; i < 15; i++ {
					name := fmt.Sprintf("CANARY_%02d", i)
					env[name] = strings.Repeat(string(rune('a'+i)), 8)
					args = append(args, "--env-from", name)
				}
			}
			text := "before\n\x1b[31m" + value + "\nafter"
			if mode == "overlap" {
				text += " " + env["SHORT_CANARY"]
			}
			if mode == "many" {
				for i := 0; i < 15; i++ {
					text += " " + env[fmt.Sprintf("CANARY_%02d", i)]
				}
			}
			frame := rpcRunSuccess
			wantCode := 7
			status := `{"address":"t7","state":"live","reported":true,"status":{"state":"blocked","msg":` + quotedEnv(text) + `}}`
			read := fakecli.Rule{Argv: []string{"read", "t7", "--lines", "0", "--json"}, Stdout: `{"text":` + quotedEnv(text) + `}`}
			if mode == "message" || mode == "hint" {
				message, hint := text, "ordinary hint"
				if mode == "hint" {
					message, hint = "rejected", text
				}
				frame = `{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":` + quotedEnv(message) + `,"data":{"kind":"internal","hint":` + quotedEnv(hint) + `}}}`
				wantCode = 4
			}
			if mode == "blocked-diagnostic" {
				read.Stdout, read.Code = "", 1
			}
			if mode == "ready-screen" {
				status, wantCode = hcReadyWorker, 0
			}
			rules := append(hcFixtureRules(), fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: status}, read)
			if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
				t.Fatal(err)
			}
			conn := newFakeSpawnRPCConn(rpcHelloSuccess + "\n" + frame + "\n")
			exit, out, stderr, calls := spawnHcExitWithConn(t, bin, args, ctx, env, cwd, conn)
			code, message := 0, ""
			if exit != nil {
				code, message = exit.Code, exit.Msg
			}
			if code != wantCode {
				t.Fatalf("exit=%d want=%d", code, wantCode)
			}
			public := out.String() + stderr.String() + message
			for _, contents := range envFixtureState(t, state) {
				public += contents
			}
			for i := 2; i < len(args); i += 2 {
				name := args[i+1]
				if v := env[name]; v != "" && strings.Contains(public, v) {
					t.Errorf("selected value leaked: %s", name)
				}
			}
			if mode != "ready-screen" && !strings.Contains(public, "[redacted:SELECTED_CANARY]") {
				t.Error("missing replacement token")
			}
			if mode == "overlap" && !strings.Contains(public, "[redacted:SHORT_CANARY]") {
				t.Error("missing shorter value token")
			}
			if wantCode == 7 && !spawnJSONHasField(t, out.String(), `"status": "blocked_at_startup"`) {
				t.Error("lost blocked classification")
			}
			if wantCode == 0 && !spawnJSONHasField(t, out.String(), `"status": "ready"`) {
				t.Error("lost ready classification")
			}
			for _, call := range calls {
				if call.Argv[0] == "kill" || call.Argv[0] == "rm" {
					t.Fatal("rejected/blocked/ready start performed cleanup")
				}
			}
			if !conn.closed || conn.reapCount != 1 {
				t.Fatal("RPC connection not cleaned up")
			}
		})
	}
}

func TestSpawnEnvRedactorIsolationAndRepresentations(t *testing.T) {
	first := spawnOptions{envSnapshot: map[string]string{"FIRST": "s3cr3t-value"}}
	second := spawnOptions{envSnapshot: map[string]string{"SECOND": "other-value"}}
	var workers sync.WaitGroup
	for i := 0; i < 16; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			text := "s3cr3t-value other-value"
			if first.outputText(text) != "[redacted:FIRST] other-value" || second.outputText(text) != "s3cr3t-value [redacted:SECOND]" {
				t.Error("redaction snapshots crossed invocations")
			}
		}()
	}
	workers.Wait()
	// Exact substring matching cannot recognize these transformed forms.
	for _, text := range []string{"s3cr3t-\nvalue", "s3cr3t%2Dvalue", "czNjcjN0LXZhbHVl"} {
		if first.outputText(text) != text {
			t.Error("unexpected representation matching")
		}
	}
}

func TestSpawnEnvLaneRefusalOutput(t *testing.T) {
	for _, mode := range []string{"kind", "busy"} {
		t.Run(mode, func(t *testing.T) {
			cwd, bin, state, env, ctx := hcSpawnFixture(t, "worker", "pi")
			env["SELECTED_CANARY"], env["SOHO_LANES"] = "worker", "on"
			kind := "pi"
			wantCode := 10
			if mode == "kind" {
				kind, wantCode = "codex", 13
			}
			for key, value := range map[string]string{"lane_build_roles": "worker", "lane_build_kind": kind, "lane_build_panes": "1"} {
				ctx.Entries[key] = core.ConfigEntry{Value: value, Source: "project"}
				ctx.Order = append(ctx.Order, key)
			}
			rules := hcFixtureRules()
			if mode == "busy" {
				env["SELECTED_CANARY"] = "full"
				sd := filepath.Join(state, "sX")
				if err := os.MkdirAll(sd, 0700); err != nil {
					t.Fatal(err)
				}
				core.RosterAppend(sd, []string{"occupied", "t8", "pi", "worker", "pi", "1", cwd, "now", "", "ask", "worker", "build", "false", "", "", "inherit"})
				rules = append(rules, fakecli.Rule{Argv: []string{"get", "occupied", "--json"}, Stdout: `{"address":"t8","state":"live","reported":true,"status":{"state":"working"}}`})
			}
			if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
				t.Fatal(err)
			}
			conn := newFakeSpawnRPCConn()
			exit, out, stderr, _ := spawnHcExitWithConn(t, bin, []string{"worker", "--fresh", "--kind", "pi", "--env-from", "SELECTED_CANARY"}, ctx, env, cwd, conn)
			if exit == nil || exit.Code != wantCode {
				t.Fatalf("wrong exit: %+v; out=%s stderr=%s", exit, out, stderr)
			}
			public := out.String() + stderr.String()
			for _, contents := range envFixtureState(t, state) {
				public += contents
			}
			if strings.Contains(public, env["SELECTED_CANARY"]) {
				t.Fatal("lane refusal leaked selected word")
			}
			if !strings.Contains(public, "[redacted:SELECTED_CANARY]") {
				t.Fatal("missing token")
			}
			if conn.getConnectCount() != 0 {
				t.Fatal("lane refusal started a worker")
			}
		})
	}
}

func quotedEnv(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestSpawnEnvShortValueRefused(t *testing.T) {
	for _, value := range []string{"x", "xy", "雪"} {
		t.Run(quotedEnv(value), func(t *testing.T) {
			cwd, bin, state, env, ctx := hcSpawnFixture(t, "worker", "pi")
			env["SELECTED_CANARY"] = value
			before := envFixtureState(t, state)
			conn := newFakeSpawnRPCConn()
			exit, out, stderr, calls := spawnHcExitWithConn(t, bin, []string{"worker", "--fresh", "--env-from", "SELECTED_CANARY"}, ctx, env, cwd, conn)
			if exit == nil || exit.Code != 2 || exit.Msg != "spawn: --env-from SELECTED_CANARY: value too short to protect in output" {
				t.Errorf("wrong refusal: %+v", exit)
			}
			if out.Len() != 0 || stderr.Len() != 0 || len(calls) != 0 || conn.getConnectCount() != 0 {
				t.Error("refusal had effects")
			}
			if len(before) != len(envFixtureState(t, state)) {
				t.Error("refusal wrote state")
			}
		})
	}
}
