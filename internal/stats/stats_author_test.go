package stats

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStatsAuthor(t *testing.T) {
	f := newAmendLostFixture(t)
	models := []string{"pi/qwen", "pi/qwen", "codex/gpt", ""}
	for i := range models {
		stamp := fmt.Sprintf("20261001T10000%d", i)
		tree := filepath.Join(f.root, "slice", "pi")
		if i == 2 {
			tree = filepath.Join(f.root, "slice", "codex")
		}
		body := "You are running as the `reviewer` role\n"
		if i != 3 {
			body += "Worktree: " + tree + "\n"
		}
		f.brief("reviewer", stamp, body)
		verdict := "pass"
		severity := "P0 0, P1 0, P2 0, P3 0"
		findings := 0
		if i == 0 {
			verdict = "fail"
			severity = "P0 1, P1 2, P2 3, P3 4"
			findings = 10
		}
		f.report("reviewer", stamp, fmt.Sprintf("findings: %d (%s) | verdict: %s\n", findings, severity, verdict))
		side := fmt.Sprintf(`{"version":1,"submission":"accepted","kind":"claude","model":"reviewer-model","effort":"","author_model":%q,"author_type":"backend"}`, models[i])
		if err := os.WriteFile(filepath.Join(f.briefs, "reviewer-"+stamp+".dispatch.json"), []byte(side), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, by := range []string{"author-model", "type"} {
		out, rc := f.run(t, "--by", by, "--json")
		if rc != 0 {
			t.Fatal(rc)
		}
		want := `{"by":"author-model","groups":{"codex/gpt":{"reviews":1,"pass":1,"fail":0,"fail_rate":0,"severity":{"P0":0,"P1":0,"P2":0,"P3":0},"severity_per_review":{"P0":0,"P1":0,"P2":0,"P3":0},"rounds_per_slice":1},"pi/qwen":{"reviews":2,"pass":1,"fail":1,"fail_rate":0.5,"severity":{"P0":1,"P1":2,"P2":3,"P3":4},"severity_per_review":{"P0":0.5,"P1":1,"P2":1.5,"P3":2},"rounds_per_slice":2}},"reviews_without_author":1}` + "\n"
		if by == "type" {
			want = `{"by":"type","groups":{"backend":{"reviews":3,"pass":2,"fail":1,"fail_rate":0.3333333333333333,"severity":{"P0":1,"P1":2,"P2":3,"P3":4},"severity_per_review":{"P0":0.3333333333333333,"P1":0.6666666666666666,"P2":1,"P3":1.3333333333333333},"rounds_per_slice":1.5}},"reviews_without_author":1}` + "\n"
		}
		if out != want {
			t.Fatalf("JSON %s\ngot %s\nwant %s", by, out, want)
		}
		out, rc = f.run(t, "--by", by)
		if rc != 0 {
			t.Fatal(rc)
		}
		// Explicit contract widths, independent of the production table builder.
		format := "%-12s  %7s  %4s  %4s  %9s  %2s  %2s  %2s  %2s  %9s  %9s  %9s  %9s  %12s\n"
		if by == "type" {
			format = strings.Replace(format, "%-12s", "%-7s", 1)
		}
		want = "reviews by " + by + ":\n" + fmt.Sprintf(format, by, "reviews", "pass", "fail", "fail rate", "P0", "P1", "P2", "P3", "P0/review", "P1/review", "P2/review", "P3/review", "rounds/slice")
		if by == "author-model" {
			want += fmt.Sprintf(format, "codex/gpt", "1", "1", "0", "0.0%", "0", "0", "0", "0", "0.0", "0.0", "0.0", "0.0", "1.0") + fmt.Sprintf(format, "pi/qwen", "2", "1", "1", "50.0%", "1", "2", "3", "4", "0.5", "1.0", "1.5", "2.0", "2.0")
		} else {
			want += fmt.Sprintf(format, "backend", "3", "2", "1", "33.3%", "1", "2", "3", "4", "0.3", "0.7", "1.0", "1.3", "1.5")
		}
		want += "1 reviews have no author record (older dispatches)\n"
		if out != want {
			t.Fatalf("table %s\ngot %q\nwant %q", by, out, want)
		}
		t.Log(out)
	}
	// Author metadata must have no effect on either existing model output.
	for _, jsonFlag := range [][]string{nil, {"--json"}} {
		args := append([]string{"--by", "model"}, jsonFlag...)
		before, rc := f.run(t, args...)
		if rc != 0 {
			t.Fatal(rc)
		}
		for i := range models {
			path := filepath.Join(f.briefs, fmt.Sprintf("reviewer-20261001T10000%d.dispatch.json", i))
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			clean := strings.ReplaceAll(string(raw), `"author_model"`, `"unused_author_model"`)
			if err := os.WriteFile(path, []byte(clean), 0600); err != nil {
				t.Fatal(err)
			}
		}
		after, rc := f.run(t, args...)
		if rc != 0 || before != after {
			t.Fatalf("legacy model output changed: before %s after %s", before, after)
		}
		for i := range models {
			path := filepath.Join(f.briefs, fmt.Sprintf("reviewer-20261001T10000%d.dispatch.json", i))
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(raw), `"unused_author_model"`, `"author_model"`)), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestStatsAuthorLegacyModelContract(t *testing.T) {
	f := newAmendLostFixture(t)
	brief := f.brief("reviewer", "20261001T100000", "You are running as the `reviewer` role\nWorktree: "+filepath.Join(f.root, "slice")+"\n")
	report := f.report("reviewer", "20261001T100000", "findings: 0 (P0 0, P1 0, P2 0, P3 0) | verdict: pass\n")
	if err := os.WriteFile(strings.TrimSuffix(brief, ".md")+".dispatch.json", []byte(`{"version":1,"submission":"accepted","kind":"claude","model":"reviewer-model","effort":"","author_model":"pi/qwen","author_type":"backend"}`), 0600); err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1700000000, 0)
	if err := os.Chtimes(brief, base, base); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(report, base.Add(time.Minute), base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	out, rc := f.run(t, "--by", "model", "--json")
	want := `{"by":"model","groups":{"reviewer-model":{"tasks":1,"briefs":1,"amendments":0,"reuses":0,"no_report":{"pending":0,"lost":0},"not_received":0,"minutes":{"avg":1,"median":1,"max":1},"partials":0}},"lost_briefs":{},"review":{"reviewer-model":{"header":1,"pass":1,"fail":0,"severity":{"P0":0,"P1":0,"P2":0,"P3":0},"no_header":0}}}` + "\n"
	if rc != 0 || out != want {
		t.Fatalf("legacy model JSON rc=%d got=%s want=%s", rc, out, want)
	}
	out, rc = f.run(t, "--by", "model")
	want = "tasks by model:\n" + fmt.Sprintf("%-14s  %5s  %6s  %10s  %6s  %24s  %12s  %7s  %10s  %7s  %8s\n", "model", "tasks", "briefs", "amendments", "reuses", "no-report (pending/lost)", "not-received", "avg min", "median min", "max min", "partials") + fmt.Sprintf("%-14s  %5s  %6s  %10s  %6s  %24s  %12s  %7s  %10s  %7s  %8s\n", "reviewer-model", "1", "1", "0", "0", "0 (0/0)", "0", "1.0", "1.0", "1.0", "0") + "\nreviews by model:\n" + fmt.Sprintf("%-14s  %6s  %4s  %4s  %2s  %2s  %2s  %2s  %9s\n", "model", "header", "pass", "fail", "P0", "P1", "P2", "P3", "no-header") + fmt.Sprintf("%-14s  %6s  %4s  %4s  %2s  %2s  %2s  %2s  %9s\n", "reviewer-model", "1", "1", "0", "0", "0", "0", "0", "0")
	if rc != 0 || out != want {
		t.Fatalf("legacy model text rc=%d got=%q want=%q", rc, out, want)
	}
}
