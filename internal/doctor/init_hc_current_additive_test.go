package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/sessionmeta"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const r286RootMismatch = "init: the caller terminal's root does not match the project root"

// Every fixture goes through CmdInit and the real backend's fake CLI boundary.
// Stable detail replies let the same inputs run on the base and the additive flow.
func r286InitCase(t *testing.T, named bool, shape string, wantMessage string) {
	t.Helper()
	name := ""
	if named {
		name = "orchestrator"
	}
	env, root := initHcTestFixture(t, func(root string) []fakecli.Rule {
		outside := t.TempDir()
		cmd := exec.Command("git", "init", "-q", outside)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init outside: %s: %v", out, err)
		}
		record := func(cwd string) map[string]any {
			return map[string]any{"address": "t2c", "name": name, "state": "live", "cwd": cwd, "status": map[string]any{"app": "codex", "state": "working"}}
		}
		summary, detail := record(root), record(root)
		detailCode := 0
		switch shape {
		case "root", "subdirectory", "symlink", "symlink-subdirectory":
			summary["cwd"], detail["cwd"] = outside, outside
			current := root
			if shape == "subdirectory" || shape == "symlink-subdirectory" {
				current = filepath.Join(root, "nested space 'quoted'")
				if err := os.Mkdir(current, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if shape == "symlink" || shape == "symlink-subdirectory" {
				alias := filepath.Join(t.TempDir(), "alias space 'quoted'")
				if err := os.Symlink(current, alias); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
				current = alias
			}
			detail["foreground"] = map[string]any{"cwd": current}
		case "outside", "relative", "dotdot", "symlink-outside", "nested-repository":
			current := outside
			switch shape {
			case "relative":
				current = "."
			case "dotdot":
				if err := os.Mkdir(filepath.Join(root, "sub"), 0700); err != nil {
					t.Fatal(err)
				}
				current = root + string(filepath.Separator) + "sub" + string(filepath.Separator) + ".."
			case "symlink-outside":
				current = filepath.Join(root, "outside-alias")
				if err := os.Symlink(outside, current); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			case "nested-repository":
				current = filepath.Join(root, "independent")
				if out, err := exec.Command("git", "init", "-q", current).CombinedOutput(); err != nil {
					t.Fatalf("nested git init: %s: %v", out, err)
				}
			}
			detail["foreground"] = map[string]any{"cwd": current}
		case "activity-absent":
			detail["status"] = map[string]any{"app": "codex"}
		case "kind-absent":
			detail["status"] = map[string]any{"state": "working"}
		case "detail-cwd-absent":
			delete(detail, "cwd")
		case "detail-name-empty":
			detail["name"] = ""
		case "detail-lifecycle-absent":
			delete(detail, "state")
		case "detail-address-absent", "detail-address-different":
			detail["labels"] = map[string]any{"soho.session": "hc-t2c"}
			delete(detail, "address")
			if shape == "detail-address-different" {
				detail["address"] = "t99"
			}
		case "detail-session-different":
			detail["labels"] = map[string]any{"soho.session": "other"}
		case "summary-only-inside":
			summary["cwd"], detail["cwd"] = outside, outside
			summary["foreground"] = map[string]any{"cwd": root}
		case "summary-only-outside":
			summary["foreground"] = map[string]any{"cwd": outside}
		case "current-empty", "current-null", "current-number":
			var current any = ""
			if shape == "current-null" {
				current = nil
			} else if shape == "current-number" {
				current = 123
			}
			detail["foreground"] = map[string]any{"cwd": current}
		case "override-address-mismatch", "override-session-mismatch", "override-not-live", "override-fetch-failed", "override-malformed":
			summary["cwd"], detail["cwd"] = outside, outside
			detail["foreground"] = map[string]any{"cwd": root}
			switch shape {
			case "override-address-mismatch":
				detail["address"] = "t99"
			case "override-session-mismatch":
				detail["labels"] = map[string]any{"soho.session": "other"}
			case "override-not-live":
				detail["state"] = "finished"
			case "override-fetch-failed":
				detailCode = 1
			}
		default:
			// All nine start-directory pairs in R283, including missing/missing.
			var s, d string
			if _, err := fmt.Sscanf(shape, "starts-%1s-%1s", &s, &d); err != nil {
				t.Fatalf("unknown shape %q: %v", shape, err)
			}
			for key, rec := range map[string]map[string]any{s: summary} {
				if key == "O" {
					rec["cwd"] = outside
				} else if key == "M" {
					delete(rec, "cwd")
				}
			}
			if d == "O" {
				detail["cwd"] = outside
			} else if d == "M" {
				delete(detail, "cwd")
			}
		}
		encode := func(v any) string {
			b, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			return string(b)
		}
		detailJSON := encode(detail)
		if shape == "override-malformed" {
			detailJSON = "bad-json"
		}
		return []fakecli.Rule{
			{Argv: []string{"get", "t2c", "--json"}, Call: 1, Stdout: nativeCallerGetJSON(root)},
			{Argv: []string{"get", "t2c", "--json"}, Stdout: detailJSON, Code: detailCode},
			{Argv: []string{"ls", "--json"}, Stdout: encode(map[string]any{"terminals": []any{summary}})},
		}
	})
	out, stderr, recovered := runCmdInitCapture(t, env, root)
	code, message := 0, ""
	if recovered != nil {
		exit, ok := recovered.(*platform.ExitError)
		if !ok {
			t.Fatalf("unexpected panic: %#v", recovered)
		}
		code, message = exit.Code, exit.Msg
	}
	t.Logf("OUTCOME code=%d message=%q stderr=%q json=%t", code, message, stderr, json.Valid([]byte(out)))
	_, persisted, readErr := (sessionmeta.Store{StateBase: core.StateRootPathForProject(&core.Config{}, env, root), SessionID: "hc-t2c"}).Read()
	t.Logf("OBSERVED persisted=%t readError=%v", persisted, readErr)
	if wantMessage != "" {
		if code != 2 || message != wantMessage || out != "" || stderr != "" {
			t.Errorf("code=%d message=%q stdout=%q stderr=%q; want exit 2, message=%q and empty streams", code, message, out, stderr, wantMessage)
		}
		assertNoStateWrite(t, env, root, "hc-t2c")
		t.Log("PERSISTED false")
		return
	}
	if code != 0 || message != "" || stderr != "" {
		t.Fatalf("code=%d message=%q stderr=%q", code, message, stderr)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		t.Fatalf("JSON: %v; stdout=%q", err, out)
	}
	wantName := name
	if wantName == "" {
		wantName = "t2c"
	}
	for key, want := range map[string]any{"orchestrator": wantName, "pane_id": "t2c", "workspace_id": "hc-t2c", "tab_id": "", "layout": "split", "title": "", "state_dir": filepath.Join(env["SOHO_DIR"], "hc-t2c")} {
		if obj[key] != want {
			t.Errorf("JSON[%s]=%v want %v", key, obj[key], want)
		}
	}
	if _, ok := obj["first_run"].(bool); !ok {
		t.Errorf("first_run=%v", obj["first_run"])
	}
	rec, ok, err := (sessionmeta.Store{StateBase: core.StateRootPathForProject(&core.Config{}, env, root), SessionID: "hc-t2c"}).Read()
	t.Logf("PERSISTED %t", ok)
	if err != nil || !ok || rec.Terminal != "t2c" || rec.SessionID != "hc-t2c" || rec.ProjectRoot != root || rec.Name != wantName || rec.Kind != "codex" || rec.Task != filepath.Base(root) {
		t.Fatalf("record=%+v ok=%v err=%v", rec, ok, err)
	}
}

func TestInitHcR286Current(t *testing.T) {
	for _, named := range []bool{false, true} {
		for _, shape := range []string{"root", "subdirectory", "symlink", "symlink-subdirectory", "outside", "relative", "dotdot", "symlink-outside", "nested-repository"} {
			t.Run(fmt.Sprintf("named=%t/%s", named, shape), func(t *testing.T) {
				message := r286RootMismatch
				if shape == "root" || shape == "subdirectory" || shape == "symlink" || shape == "symlink-subdirectory" {
					message = ""
				}
				r286InitCase(t, named, shape, message)
			})
		}
	}
}

func TestInitHcR286Compatibility(t *testing.T) {
	for _, named := range []bool{false, true} {
		for _, s := range []string{"I", "O", "M"} {
			for _, d := range []string{"I", "O", "M"} {
				shape := "starts-" + s + "-" + d
				t.Run(fmt.Sprintf("named=%t/%s", named, shape), func(t *testing.T) {
					message := ""
					if s == "M" {
						message = "init: the caller terminal reports no cwd"
					} else if s == "O" {
						message = r286RootMismatch
					} else if !named && d == "M" {
						message = "init: the detailed caller reports no cwd"
					} else if !named && d == "O" {
						message = "init: the detailed caller root does not match the project root"
					}
					r286InitCase(t, named, shape, message)
				})
			}
		}
		for _, shape := range []string{"activity-absent", "kind-absent", "detail-cwd-absent", "detail-name-empty", "detail-lifecycle-absent", "detail-address-absent", "detail-address-different", "detail-session-different", "summary-only-inside", "summary-only-outside", "current-empty", "current-null", "current-number", "override-address-mismatch", "override-session-mismatch", "override-not-live", "override-fetch-failed", "override-malformed"} {
			t.Run(fmt.Sprintf("named=%t/%s", named, shape), func(t *testing.T) {
				message := ""
				switch shape {
				case "summary-only-inside", "override-address-mismatch", "override-session-mismatch", "override-not-live", "override-fetch-failed", "override-malformed":
					message = r286RootMismatch
				case "detail-name-empty":
					if named {
						message = "init: the caller terminal's name could not be read: the caller terminal has no name"
					}
				case "activity-absent":
					if !named {
						message = "init: the caller terminal activity is not known"
					}
				case "kind-absent":
					if !named {
						message = "init: the caller terminal names no known kind"
					}
				case "detail-cwd-absent":
					if !named {
						message = "init: the detailed caller reports no cwd"
					}
				case "detail-lifecycle-absent":
					if !named {
						message = "init: the caller terminal is not live"
					}
				case "detail-address-absent", "detail-address-different":
					if !named {
						message = "init: the detailed caller address does not match"
					}
				case "detail-session-different":
					if !named {
						message = "init: the detailed caller session does not match the session"
					}
				}
				r286InitCase(t, named, shape, message)
			})
		}
	}
}
