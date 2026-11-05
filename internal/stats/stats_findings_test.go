package stats

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStatsFindingMarks covers DJA-684 slice B scenario 11: --by origin and
// --by class count the review reports' finding marks, with the header
// findings without a mark in the footer, and old reports (no marks) never
// error.
func TestStatsFindingMarks(t *testing.T) {
	f := newAmendLostFixture(t)
	stamps := []string{"20261001T100000", "20261001T100001", "20261001T100002", "20261001T100003"}
	// i=0: four findings, three marked (impl 2, brief 1), one unmarked.
	f.brief("reviewer", stamps[0], "You are running as the `reviewer` role\n")
	f.report("reviewer", stamps[0], "findings: 4 (P0 0, P1 0, P2 0, P3 4) | verdict: fail\n\n"+
		"- [F1] [origin: impl] [class: missing-bound] a bound is missing\n"+
		"- [F2] [origin: brief] [class: doc-drift] the doc drifted\n"+
		"- [F3] [origin: impl] [class: missing-bound] another bound\n"+
		"- a finding without marks\n")
	// i=1: two findings, both marked (brief 1, unknown 1).
	f.brief("reviewer", stamps[1], "You are running as the `reviewer` role\n")
	f.report("reviewer", stamps[1], "findings: 2 (P0 0, P1 0, P2 0, P3 2) | verdict: pass\n\n"+
		"- [F1] [origin: brief] [class: doc-drift] the doc drifted\n"+
		"- [F2] [origin: unknown] [class: race-read-write] a race\n")
	// i=2: an old report, three findings and no marks: footer only.
	f.brief("reviewer", stamps[2], "You are running as the `reviewer` role\n")
	f.report("reviewer", stamps[2], "findings: 3 (P0 0, P1 0, P2 0, P3 3) | verdict: pass\n\n- old finding a\n- old finding b\n- old finding c\n")
	// i=3: a non-review report with marks and a header: not counted.
	f.brief("implementer", stamps[3], "You are running as the `implementer` role\n")
	f.report("implementer", stamps[3], "findings: 1 (P0 0, P1 0, P2 0, P3 1) | verdict: pass\n\n- [F1] [origin: impl] [class: missing-bound] x\n")
	side := `{"version":1,"submission":"accepted","kind":"claude","model":"reviewer-model","effort":""}`
	for _, stamp := range stamps {
		if err := os.WriteFile(filepath.Join(f.briefs, "reviewer-"+stamp+".dispatch.json"), []byte(side), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.briefs, "implementer-"+stamps[3]+".dispatch.json"), []byte(side), 0600); err != nil {
		t.Fatal(err)
	}

	// Totals: brief 2, impl 2, unknown 1, unmarked 1 + 0 + 3 = 4.
	// Classes: missing-bound 2, doc-drift 2, race-read-write 1.
	wantOriginJSON := `{"by":"origin","groups":{"brief":{"findings":2},"impl":{"findings":2},"unknown":{"findings":1}},"unmarked":4}` + "\n"
	wantClassJSON := `{"by":"class","groups":{"doc-drift":{"findings":2},"missing-bound":{"findings":2},"race-read-write":{"findings":1}},"unmarked":4}` + "\n"
	out, rc := f.run(t, "--by", "origin", "--json")
	if rc != 0 {
		t.Fatalf("--by origin --json rc=%d out=%q", rc, out)
	}
	if out != wantOriginJSON {
		t.Fatalf("origin JSON\ngot %s\nwant %s", out, wantOriginJSON)
	}
	out, rc = f.run(t, "--by", "class", "--json")
	if rc != 0 {
		t.Fatalf("--by class --json rc=%d out=%q", rc, out)
	}
	if out != wantClassJSON {
		t.Fatalf("class JSON\ngot %s\nwant %s", out, wantClassJSON)
	}
	// Explicit contract widths, independent of the production table builder.
	originFmt := "%-7s  %8s\n"
	out, rc = f.run(t, "--by", "origin")
	if rc != 0 {
		t.Fatalf("--by origin rc=%d out=%q", rc, out)
	}
	want := "findings by origin:\n" +
		fmt.Sprintf(originFmt, "origin", "findings") +
		fmt.Sprintf(originFmt, "brief", "2") +
		fmt.Sprintf(originFmt, "impl", "2") +
		fmt.Sprintf(originFmt, "unknown", "1") +
		"4 finding(s) without a mark\n"
	if out != want {
		t.Fatalf("origin table\ngot %q\nwant %q", out, want)
	}
	classFmt := "%-15s  %8s\n"
	out, rc = f.run(t, "--by", "class")
	if rc != 0 {
		t.Fatalf("--by class rc=%d out=%q", rc, out)
	}
	// Count order descending, the tie alphabetical: doc-drift before
	// missing-bound, then race-read-write.
	want = "findings by class:\n" +
		fmt.Sprintf(classFmt, "class", "findings") +
		fmt.Sprintf(classFmt, "doc-drift", "2") +
		fmt.Sprintf(classFmt, "missing-bound", "2") +
		fmt.Sprintf(classFmt, "race-read-write", "1") +
		"4 finding(s) without a mark\n"
	if out != want {
		t.Fatalf("class table\ngot %q\nwant %q", out, want)
	}
}

// TestStatsFindingMarksNoReviews: an empty workspace (and a review report
// without a header) never errors.
func TestStatsFindingMarksNoReviews(t *testing.T) {
	f := newAmendLostFixture(t)
	// An empty workspace keeps the stats' established "no dispatches
	// recorded" message and exit 0 (the same as every other --by value):
	// never an error.
	for _, by := range []string{"origin", "class"} {
		out, rc := f.run(t, "--by", by, "--json")
		if rc != 0 {
			t.Fatalf("--by %s rc=%d out=%q", by, rc, out)
		}
		if !strings.Contains(out, "no dispatches recorded") {
			t.Fatalf("--by %s on an empty workspace: %q", by, out)
		}
	}
	// A review report without a header contributes nothing (no findings
	// total is known), and a marked review report with a header contributes
	// its marks.
	stamp := "20261001T100000"
	f.brief("reviewer", stamp, "You are running as the `reviewer` role\n")
	f.report("reviewer", stamp, "no header here\n- [F1] [origin: impl] [class: missing-bound] x\n")
	side := `{"version":1,"submission":"accepted","kind":"claude","model":"reviewer-model","effort":""}`
	if err := os.WriteFile(filepath.Join(f.briefs, "reviewer-"+stamp+".dispatch.json"), []byte(side), 0600); err != nil {
		t.Fatal(err)
	}
	out, rc := f.run(t, "--by", "origin", "--json")
	if rc != 0 {
		t.Fatalf("rc=%d out=%q", rc, out)
	}
	want := `{"by":"origin","groups":{"brief":{"findings":0},"impl":{"findings":0},"unknown":{"findings":0}},"unmarked":0}` + "\n"
	if out != want {
		t.Fatalf("header-less review report changed the view:\ngot %s\nwant %s", out, want)
	}
}

// The class rows go by count first: the fixture's alphabetical order is the
// opposite of its count order, so dropping the count comparison changes the
// output.
func TestStatsByClassOrdersByCount(t *testing.T) {
	f := newAmendLostFixture(t)
	stamp := "20261001T100000"
	f.brief("reviewer", stamp, "You are running as the `reviewer` role\n")
	f.report("reviewer", stamp, "findings: 3 (P0 0, P1 0, P2 0, P3 3) | verdict: pass\n\n"+
		"- [F1] [origin: impl] [class: aaa-rare] once\n"+
		"- [F2] [origin: impl] [class: zzz-common] first\n"+
		"- [F3] [origin: impl] [class: zzz-common] second\n")
	side := `{"version":1,"submission":"accepted","kind":"claude","model":"reviewer-model","effort":""}`
	if err := os.WriteFile(filepath.Join(f.briefs, "reviewer-"+stamp+".dispatch.json"), []byte(side), 0600); err != nil {
		t.Fatal(err)
	}
	out, rc := f.run(t, "--by", "class", "--json")
	want := `{"by":"class","groups":{"zzz-common":{"findings":2},"aaa-rare":{"findings":1}},"unmarked":0}` + "\n"
	if rc != 0 || out != want {
		t.Fatalf("rc=%d\ngot %s\nwant %s", rc, out, want)
	}
	out, rc = f.run(t, "--by", "class")
	if rc != 0 || strings.Index(out, "zzz-common") > strings.Index(out, "aaa-rare") {
		t.Fatalf("rc=%d the table is not in count order:\n%s", rc, out)
	}
}
