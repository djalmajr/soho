package gittree_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReview543R6PointerUnavailable(t *testing.T) {
	for _, mode := range []string{"unreadable-pointer", "malformed-pointer", "readable-control", "unreadable-history-control"} {
		t.Run(mode, func(t *testing.T) {
			f := newDiffFixture(t, "# Owned files\n- src/\n", "Completed.\n", "implementer")
			task := filepath.Join(f.state, "reports", "worker-fixture.current.md")
			middle := filepath.Join(f.state, "reports", "worker-middle.md")
			middleStem := filepath.Join(f.state, "briefs", "worker-middle")
			diffWrite(t, middleStem+".dispatch.json", fmt.Sprintf(`{"task_report":%q}`, task))
			diffWrite(t, middleStem+".md", "# Brief\n# Owned files\n- lib/\n")
			current := filepath.Join(f.state, "reports", "worker-current.md")
			stem := filepath.Join(f.state, "briefs", "worker-current")
			diffWrite(t, stem+".md", "# Brief\nCheck again.\n")
			diffWrite(t, stem+".dispatch.json", fmt.Sprintf(`{"role":"implementer","git_tree":%q,"git_head":%q,"git_branch":"main","task_report":%q}`, f.tree, f.head, task))
			diffWrite(t, current, "Completed.\n")
			pointer := filepath.Join(f.state, "task-report-worker.json")
			valid := fmt.Sprintf(`{"version":1,"task_report":%q,"history":[%q,%q],"current":%q}`, task, f.report, middle, current)
			diffWrite(t, pointer, valid)
			t.Cleanup(func() { _ = os.Chmod(pointer, 0600); _ = os.Chmod(middleStem+".dispatch.json", 0600) })
			if mode == "unreadable-pointer" {
				if e := os.Chmod(pointer, 0000); e != nil {
					t.Fatal(e)
				}
				if _, e := os.ReadFile(pointer); e == nil {
					t.Skip("mode-000 readable")
				} else {
					t.Logf("pointer read=%v", e)
				}
			}
			if mode == "malformed-pointer" {
				diffWrite(t, pointer, "{")
			}
			if mode == "unreadable-history-control" {
				if e := os.Chmod(middleStem+".dispatch.json", 0000); e != nil {
					t.Fatal(e)
				}
				if _, e := os.ReadFile(middleStem + ".dispatch.json"); e == nil {
					t.Skip("mode-000 readable")
				}
			}
			diffWrite(t, filepath.Join(f.state, "last-report-worker"), current+"\n")
			diffWrite(t, filepath.Join(f.tree, "lib", "allowed.txt"), "changed\n")
			v, out, warning := f.wait(t)
			t.Logf("mode=%s first=%s stderr=%s", mode, out, warning)
			if mode == "readable-control" {
				if v["diff_stat"] == nil || v["outside_owned"] != nil || warning != "" {
					t.Fatalf("valid scope lost %v %s", v, warning)
				}
				return
			}
			if v["diff_stat"] != nil || v["outside_owned"] != nil || v["cited_unchanged"] != nil || warning != "" {
				t.Errorf("unknown history must skip audit, got=%v stderr=%s", v, warning)
			}
			for _, file := range []string{stem + ".dispatch.json.diff-checked", strings.TrimSuffix(current, ".md") + ".diff-stat.txt"} {
				_, e := os.Stat(file)
				t.Logf("side effect %s: %v", filepath.Base(file), e)
				if !os.IsNotExist(e) {
					t.Errorf("unknown history left artifact/marker: %s %v", file, e)
				}
			}
			b, e := os.ReadFile(stem + ".dispatch.json")
			if e != nil {
				t.Fatal(e)
			}
			t.Logf("sidecar=%s", b)
			if strings.Contains(string(b), "diff_checked") {
				t.Error("unknown history consumed readable flag")
			}
			if e := os.Chmod(pointer, 0600); e != nil {
				t.Fatal(e)
			}
			diffWrite(t, pointer, valid)
			if mode == "unreadable-history-control" {
				if e := os.Chmod(middleStem+".dispatch.json", 0600); e != nil {
					t.Fatal(e)
				}
			}
			retry, out2, w2 := f.wait(t)
			t.Logf("retry=%s stderr=%s", out2, w2)
			if retry["diff_stat"] == nil || retry["outside_owned"] != nil || w2 != "" {
				t.Errorf("restored history must audit once with lib scope: %v %s", retry, w2)
			}
			artifact := strings.TrimSuffix(current, ".md") + ".diff-stat.txt"
			firstArtifact, e := os.ReadFile(artifact)
			if e != nil {
				t.Fatal(e)
			}
			if !strings.Contains(string(firstArtifact), "lib/allowed.txt") {
				t.Fatalf("retry artifact=%s", firstArtifact)
			}
			if _, e := os.Stat(stem + ".dispatch.json.diff-checked"); e != nil {
				t.Fatal(e)
			}
			sidecar, e := os.ReadFile(stem + ".dispatch.json")
			if e != nil || !strings.Contains(string(sidecar), "\"diff_checked\":true") {
				t.Fatalf("retry flag: %s %v", sidecar, e)
			}
			third, out3, w3 := f.wait(t)
			t.Logf("third=%s stderr=%s", out3, w3)
			if third["diff_stat"] != nil || w3 != "" {
				t.Error("audit repeated")
			}
			lastArtifact, e := os.ReadFile(artifact)
			if e != nil || string(firstArtifact) != string(lastArtifact) {
				t.Fatalf("repeat changed artifact: %q %v", lastArtifact, e)
			}
		})
	}
}

func TestDiffPointerAbsentAndExplicitOwnedControls(t *testing.T) {
	for _, mode := range []string{"absent-single-task", "explicit-owned-malformed-pointer", "explicit-owned-unreadable-pointer"} {
		t.Run(mode, func(t *testing.T) {
			f := newDiffFixture(t, "# Owned files\n- src/\n", "Completed.\n", "implementer")
			task := filepath.Join(f.state, "reports", "worker-fixture.current.md")
			pointer := filepath.Join(f.state, "task-report-worker.json")
			sidecar := fmt.Sprintf("{\"role\":\"implementer\",\"git_tree\":%q,\"git_head\":%q,\"git_branch\":\"main\",\"task_report\":%q}", f.tree, f.head, task)
			diffWrite(t, f.sidecar, sidecar)
			// Force the original-brief fallback in the genuinely absent single-task case.
			if mode == "absent-single-task" {
				current := filepath.Join(f.state, "reports", "worker-single.md")
				stem := filepath.Join(f.state, "briefs", "worker-single")
				diffWrite(t, stem+".md", "# Brief\nNo current Owned section.\n")
				diffWrite(t, stem+".dispatch.json", sidecar)
				diffWrite(t, current, "Completed.\n")
				diffWrite(t, filepath.Join(f.state, "last-report-worker"), current+"\n")
			} else {
				diffWrite(t, pointer, "{")
				if mode == "explicit-owned-unreadable-pointer" {
					t.Cleanup(func() { _ = os.Chmod(pointer, 0600) })
					if e := os.Chmod(pointer, 0000); e != nil {
						t.Fatal(e)
					}
					if _, e := os.ReadFile(pointer); e == nil {
						t.Skip("mode-000 readable")
					}
				}
			}
			diffWrite(t, filepath.Join(f.tree, "src", "a.go"), "changed\n")
			value, out, warning := f.wait(t)
			t.Logf("mode=%s output=%s stderr=%s", mode, out, warning)
			if value["diff_stat"] == nil || value["outside_owned"] != nil || warning != "" {
				t.Fatalf("control scope lost: %v %s", value, warning)
			}
			next, _, warning := f.wait(t)
			if next["diff_stat"] != nil || warning != "" {
				t.Fatalf("control audit repeated: %v %s", next, warning)
			}
		})
	}
}
