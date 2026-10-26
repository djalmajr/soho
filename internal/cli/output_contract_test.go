package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func assertStatusColumns(t *testing.T, output string, want [][]string) {
	t.Helper()
	rows := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	if len(rows) != len(want) {
		t.Fatalf("status rows=%q want %d", rows, len(want))
	}
	for i, row := range rows {
		cols := strings.Split(row, "\t")
		if len(cols) != len(want[i]) {
			t.Fatalf("status row %d columns=%q want %d", i, cols, len(want[i]))
		}
		for j, expected := range want[i] {
			if expected == "<N>" {
				if cols[j] == "-" {
					continue // No activity timestamp is represented by a dash.
				}
				if n, err := strconv.Atoi(cols[j]); err != nil || n < 0 {
					t.Fatalf("status row %d column %d must be nonnegative seconds: %q", i, j, cols[j])
				}
			} else if cols[j] != expected {
				t.Fatalf("status row %d column %d=%q want %q", i, j, cols[j], expected)
			}
		}
	}
}

func assertRetirementJSON(t *testing.T, output string, documents []map[string]any) {
	t.Helper()
	if len(documents) == 0 && output != "" {
		t.Fatalf("refused dispatch printed stdout: %q", output)
	}
	decoder := json.NewDecoder(strings.NewReader(output))
	for i, want := range documents {
		var got map[string]any
		if err := decoder.Decode(&got); err != nil {
			t.Fatalf("JSON document %d: %v; stdout=%q", i, err, output)
		}
		for key, expected := range want {
			if actual, exists := got[key]; !exists || !reflect.DeepEqual(actual, expected) {
				t.Fatalf("JSON document %d %s=%v (present=%t), want %v", i, key, actual, exists, expected)
			}
		}
		if _, spawn := got["name"]; spawn {
			for _, key := range []string{"pane_id", "family", "created_pane", "layout", "effort", "approvals", "agent_args"} {
				if _, exists := got[key]; !exists {
					t.Fatalf("spawn missing key %q: %v", key, got)
				}
			}
			if got["pane_id"] != "p-new" || got["family"] != "xai" || got["created_pane"] != true || got["layout"] != "tab" {
				t.Fatalf("spawn identity/placement: %v", got)
			}
		} else {
			for _, key := range []string{"composed_prompt", "report", "task_report"} {
				value, ok := got[key].(string)
				wantDir := "/reports/"
				if key == "composed_prompt" && got["report_volatile"] != true {
					wantDir = "/briefs/"
				}
				if !ok || !strings.Contains(value, wantDir) {
					t.Fatalf("dispatch %s=%v: expected %s path", key, got[key], wantDir)
				}
			}
		}
	}
	if len(documents) > 0 && documents[len(documents)-1]["wait_status"] != "done" && strings.TrimSpace(output[decoder.InputOffset():]) != "" {
		t.Fatalf("unexpected output after JSON documents: %q", output[decoder.InputOffset():])
	}
}

func TestConfigTableContract(t *testing.T) {
	env, cwd := tm8CommandFixture(t)
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(env.Get("XDG_CONFIG_HOME"), "soho", "config"), "max_workers=5\nlayout=tab\n")
	write(filepath.Join(cwd, ".agents", "soho.conf"), "max_workers=6\nlane.build.kind=codex\n")
	write(filepath.Join(env.Get("SOHO_DIR"), "ws", "session.conf"), "max_workers=7\n")
	for _, tc := range []struct{ envValue, value, source string }{{"", "7", "session"}, {"8", "8", "env"}} {
		t.Run(tc.source, func(t *testing.T) {
			stepEnv := env.Clone()
			stepEnv["SOHO_MAX_WORKERS"] = tc.envValue
			code, out, stderr := runIn(t, []string{"config"}, stepEnv, cwd)
			if code != 0 || stderr != "" {
				t.Fatalf("code=%d stderr=%q", code, stderr)
			}
			lines := strings.Split(out, "\n")
			if lines[0] != fmt.Sprintf("%-18s %-30s %s", "KEY", "VALUE", "SOURCE") {
				t.Fatalf("config header=%q", lines[0])
			}
			for key, expected := range map[string][2]string{"max_workers": {tc.value, tc.source}, "layout": {"tab", "user"}, "lane.build.kind": {"codex", "project"}, "multi_role": {"on", "defaults"}} {
				found := false
				for _, line := range lines[1:] {
					if !strings.HasPrefix(line, fmt.Sprintf("%-18s ", key)) {
						continue
					}
					if found || line != fmt.Sprintf("%-18s %-30s %s", key, expected[0], expected[1]) {
						t.Fatalf("config %s row=%q want value/source=%v", key, line, expected)
					}
					found = true
				}
				if !found {
					t.Fatalf("config row missing: %s", key)
				}
			}
		})
	}
}

func TestDoctorSummaryContract(t *testing.T) {
	env, cwd := tm6CLIEnv(t)
	code, out, stderr := runIn(t, []string{"doctor"}, env, cwd)
	if code != 0 || stderr != "" {
		t.Fatalf("doctor code=%d stderr=%q", code, stderr)
	}
	oks, warnings := 0, 0
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "ok     ") {
			oks++
		}
		if strings.HasPrefix(line, "warn   ") {
			warnings++
		}
	}
	if oks == 0 || warnings == 0 || lines[len(lines)-1] != fmt.Sprintf("%d ok, %d warning(s)", oks, warnings) {
		t.Fatalf("doctor summary=%q counted ok=%d warnings=%d", lines[len(lines)-1], oks, warnings)
	}
	if lines[len(lines)-2] != "first_run: true" {
		t.Fatalf("doctor first_run=%q", lines[len(lines)-2])
	}
}

func runSetupTM5InitContract(t *testing.T, seedConfig bool) {
	t.Helper()
	out, stderr, _, _, code := runTM10Init(t, `{"result":{"agent":{"name":"orchestrator"}}}`, "", 0, 0, func(repo, state string) {
		if seedConfig {
			tm6Write(t, filepath.Join(repo, ".agents", "soho.conf"), "lane.build.kind=grok\n")
		}
	})
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("init output %q: %v", out, err)
	}
	if code != 0 || got["first_run"] != !seedConfig || !strings.Contains(stderr, "warning(s)") {
		t.Fatalf("init code=%d JSON=%v stderr=%q", code, got, stderr)
	}
	for _, key := range []string{"orchestrator", "pane_id", "tab_id", "workspace_id", "layout", "state_dir", "title"} {
		if _, ok := got[key].(string); !ok {
			t.Fatalf("init %s=%v must be string", key, got[key])
		}
	}
}
