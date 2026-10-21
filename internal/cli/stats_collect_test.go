package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

type statsFixture struct {
	t                                                            *testing.T
	root, state, workspace, reports, briefs, tmp, skill, fakebin string
}

func newCommandFixture(t *testing.T) *statsFixture {
	t.Helper()
	root := filepath.Clean(filepath.Dir(testSkillDir(t)))
	base := t.TempDir()
	f := &statsFixture{t: t, root: root, state: filepath.Join(base, "state"), workspace: "ws", reports: filepath.Join(base, "state", "ws", "reports"), briefs: filepath.Join(base, "state", "ws", "briefs"), tmp: filepath.Join(base, "tmp"), skill: filepath.Join(root, "skills", "soho"), fakebin: filepath.Join(base, "fakebin")}
	if _, err := fakecli.Install(t, f.fakebin, "herdr", nil); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{f.reports, f.briefs, filepath.Join(f.tmp, "soho", "ws", "reports")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.state, "ws", "agents.tsv"), []byte("# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *statsFixture) env() platform.Env {
	env := envFrom(fakecli.Env(testutil.CleanEnv(f.t), f.fakebin))
	env["HERDR_ENV"] = "1"
	env["SOHO_DIR"] = f.state
	env["HERDR_WORKSPACE_ID"] = f.workspace
	env["SOHO_SKILL_DIR"] = f.skill
	env["TMPDIR"] = f.tmp
	return env
}
func (f *statsFixture) pair(agent, stamp, role, report string, offset time.Duration, side string) string {
	f.t.Helper()
	name := agent + "-" + stamp
	brief := filepath.Join(f.briefs, name+".md")
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	if err := os.WriteFile(brief, []byte("You are running as the `"+role+"` role\n"), 0o600); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Chtimes(brief, now, now); err != nil {
		f.t.Fatal(err)
	}
	if side != "" {
		if err := os.WriteFile(filepath.Join(f.briefs, name+".dispatch.json"), []byte(side), 0o600); err != nil {
			f.t.Fatal(err)
		}
	}
	if report != "" {
		p := filepath.Join(f.reports, name+".md")
		if err := os.WriteFile(p, []byte(report), 0o600); err != nil {
			f.t.Fatal(err)
		}
		then := now.Add(offset)
		if err := os.Chtimes(p, then, then); err != nil {
			f.t.Fatal(err)
		}
	}
	return brief
}
func (f *statsFixture) roster(name string) {
	f.t.Helper()
	p := filepath.Join(f.state, "ws", "agents.tsv")
	old, _ := os.ReadFile(p)
	_ = os.WriteFile(p, append(old, []byte(name+"\tP1\tclaude\timplementer\tanthropic\t\t"+f.root+"\n")...), 0o600)
}
func (f *statsFixture) invoke(args ...string) (string, string, int) {
	f.t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &errOut
	code := Run(append([]string(nil), args...), f.env())
	platform.Stdout, platform.Stderr = oldOut, oldErr
	return out.String(), errOut.String(), code
}
func envFrom(entries []string) platform.Env {
	env := platform.Env{}
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = value
		}
	}
	return env
}
func (f *statsFixture) accepted(kind, model, effort string) string {
	return fmt.Sprintf(`{"version":1,"submission":"accepted","kind":%q,"model":%q,"effort":%q}`, kind, model, effort)
}

func TestStatsPortedCases(t *testing.T) {
	t.Run("Windows .cmd timeout creates a missing TMPDIR before writing its spec", func(t *testing.T) { // JS: "Windows .cmd timeout creates a missing TMPDIR before writing its spec"
		t.Skip("Node's Windows cmd.exe argument-spec wrapper is a harness implementation detail; Go CLI tests do not invoke that wrapper")
	})
	t.Run("runCli returns temp directory errors for every file-backed mode", func(t *testing.T) { // JS: "runCli returns temp directory errors for every file-backed mode"
		t.Skip("runCli is a JS test-harness helper; it has no Go command contract to exercise")
	})
	t.Run("Windows .cmd timeout resolves a relative TMPDIR before using a different child cwd", func(t *testing.T) { // JS: "Windows .cmd timeout resolves a relative TMPDIR before using a different child cwd"
		t.Skip("Node's Windows cmd.exe argument-spec wrapper is a harness implementation detail; Go CLI tests do not invoke that wrapper")
	})
	t.Run("the review section counts header, pass, fail, severity and no-header", func(t *testing.T) { // JS: "stats: the review section counts header, pass, fail, severity and no-header"
		f := newCommandFixture(t)
		f.pair("pass", "20260928T120000", "reviewer", "findings: 2 (P0 1, P1 0, P2 1, P3 0) | verdict: pass\n", 0, "")
		f.pair("fail", "20260928T120001", "reviewer", "findings: 1 (P0 0, P1 1, P2 0, P3 0) | verdict: fail\n", 0, "")
		f.pair("legacy", "20260928T120002", "reviewer", "report without a review header\n", 0, "")
		out, _, code := f.invoke("stats", "--json")
		if code != 0 || !strings.Contains(out, `"reviewer":{"header":2,"pass":1,"fail":1,"severity":{"P0":1,"P1":1,"P2":1,"P3":0},"no_header":1}`) {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run("since keeps the full set's last dispatch when filtering by mtime", func(t *testing.T) { // JS: "stats: --since never overwrites the last dispatch of the full set"
		f := newCommandFixture(t)
		f.roster("alice")
		old := f.pair("alice", "20260928T120000", "tasker", "", 0, "")
		latest := f.pair("alice", "20260928T120001", "tasker", "", 0, "")
		cutoff := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
		before := cutoff.Add(-2 * time.Hour)
		if err := os.Chtimes(old, cutoff, cutoff); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(latest, before, before); err != nil {
			t.Fatal(err)
		}
		out, _, code := f.invoke("stats", "--since", cutoff.Format(time.RFC3339), "--json")
		if code != 0 || !strings.Contains(out, `"tasks":1`) || !strings.Contains(out, `"pending":0,"lost":1`) {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run("the TMPDIR routing is counted alongside the state dir", func(t *testing.T) { // JS: "stats: the $TMPDIR routing is counted alongside the state dir"
		f := newCommandFixture(t)
		f.pair("state", "20260928T120000", "tasker", "done\n", time.Minute, "")
		tmpBrief := filepath.Join(f.tmp, "soho", f.workspace, "reports", "tmp-20260928T120001.brief.md")
		tmpReport := filepath.Join(f.tmp, "soho", f.workspace, "reports", "tmp-20260928T120001.md")
		if err := os.WriteFile(tmpBrief, []byte("You are running as the `tasker` role\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(tmpReport, []byte("done\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		out, _, code := f.invoke("stats", "--json")
		if code != 0 || !strings.Contains(out, `"tasker":{"tasks":2`) {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run("a mirrored TMPDIR pair is counted once and the state copy wins", func(t *testing.T) { // JS: "stats: a mirrored $TMPDIR pair is counted once (the state-dir copy wins)"
		f := newCommandFixture(t)
		f.pair("mirror", "20260928T120000", "tasker", "done\n", time.Minute, f.accepted("claude", "state-model", "high"))
		tmpBrief := filepath.Join(f.tmp, "soho", f.workspace, "reports", "mirror-20260928T120000.brief.md")
		tmpReport := filepath.Join(f.tmp, "soho", f.workspace, "reports", "mirror-20260928T120000.md")
		if err := os.WriteFile(tmpBrief, []byte("You are running as the `tasker` role\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(tmpReport, []byte("done\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(filepath.Dir(tmpBrief), "mirror-20260928T120000.dispatch.json"), []byte(f.accepted("codex", "tmp-model", "low")), 0o600); err != nil {
			t.Fatal(err)
		}
		out, _, code := f.invoke("stats", "--by", "model", "--json")
		if code != 0 || !strings.Contains(out, `"state-model":{"tasks":1`) || strings.Contains(out, `"tmp-model"`) {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run("pair order uses UTF-16 code units for agent names", func(t *testing.T) { // JS: "stats: pair order uses code units for agent names"
		f := newCommandFixture(t)
		f.roster("😀")
		f.roster("\ue000")
		for _, a := range []string{"\ue000", "😀"} {
			f.pair(a, "20260928T120000", "tasker", "done\n", 0, "")
		}
		out, _, code := f.invoke("stats", "--by", "agent", "--json")
		groups := strings.Split(out, `"groups":{`)
		if code != 0 || len(groups) != 2 || strings.Index(groups[1], "😀") < 0 || strings.Index(groups[1], "\ue000") < 0 || strings.Index(groups[1], "😀") > strings.Index(groups[1], "\ue000") {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("JSON groups sort names by UTF-16 code units", func(t *testing.T) { // JS: "stats: JSON groups sort names by code units"
		f := newCommandFixture(t)
		for _, a := range []string{"\ue000", "😀"} {
			f.pair(a, "20260928T120000", "tasker", "done\n", 0, "")
		}
		out, _, code := f.invoke("stats", "--by", "agent", "--json")
		section := strings.Split(out, `"groups":{`)
		if code != 0 || len(section) != 2 || strings.Index(section[1], "😀") < 0 || strings.Index(section[1], "\ue000") < 0 || strings.Index(section[1], "😀") > strings.Index(section[1], "\ue000") {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("JSON lost briefs sort names by UTF-16 code units", func(t *testing.T) { // JS: "stats: JSON lost briefs sort names by code units"
		f := newCommandFixture(t)
		f.pair("alice", "20260928T120000", "\ue000", "", 0, "")
		f.pair("bob", "20260928T120001", "😀", "", 0, "")
		out, _, code := f.invoke("stats", "--json")
		section := strings.Split(out, `"lost_briefs":{`)
		if code != 0 || len(section) != 2 || strings.Index(section[1], "😀") < 0 || strings.Index(section[1], "\ue000") < 0 || strings.Index(section[1], "😀") > strings.Index(section[1], "\ue000") {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("JSON review groups sort names by UTF-16 code units", func(t *testing.T) { // JS: "stats: JSON review groups sort names by code units"
		f := newCommandFixture(t)
		for i, name := range []string{"\ue000", "😀"} {
			f.roster(name)
			f.pair(name, fmt.Sprintf("20260928T12000%d", i), "reviewer", "findings: 1 (P0 0, P1 0, P2 0, P3 1) | verdict: pass\n", 0, "")
		}
		out, _, code := f.invoke("stats", "--by", "agent", "--json")
		section := strings.Split(out, `"review":{`)
		if code != 0 || len(section) != 2 || strings.Index(section[1], "😀") < 0 || strings.Index(section[1], "\ue000") < 0 || strings.Index(section[1], "😀") > strings.Index(section[1], "\ue000") {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("task table sorts names by UTF-16 code units", func(t *testing.T) { // JS: "stats: task table sorts names by code units"
		f := newCommandFixture(t)
		for _, a := range []string{"\ue000", "😀"} {
			f.pair(a, "20260928T120000", "tasker", "done\n", 0, "")
		}
		out, _, code := f.invoke("stats", "--by", "agent")
		if code != 0 || strings.Index(out, "😀") < 0 || strings.Index(out, "\ue000") < 0 || strings.Index(out, "😀") > strings.Index(out, "\ue000") {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("review table sorts names by UTF-16 code units", func(t *testing.T) { // JS: "stats: review table sorts names by code units"
		f := newCommandFixture(t)
		for i, name := range []string{"\ue000", "😀"} {
			f.roster(name)
			f.pair(name, fmt.Sprintf("20260928T12000%d", i), "reviewer", "findings: 1 (P0 0, P1 0, P2 0, P3 1) | verdict: pass\n", 0, "")
		}
		out, _, code := f.invoke("stats", "--by", "agent")
		section := strings.Split(out, "reviews by agent:\n")
		if code != 0 || len(section) != 2 || strings.Index(section[1], "😀") < 0 || strings.Index(section[1], "\ue000") < 0 || strings.Index(section[1], "😀") > strings.Index(section[1], "\ue000") {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("lost brief lines sort names by UTF-16 code units", func(t *testing.T) { // JS: "stats: lost brief lines sort names by code units"
		f := newCommandFixture(t)
		f.pair("alice", "20260928T120000", "\ue000", "", 0, "")
		f.pair("bob", "20260928T120001", "😀", "", 0, "")
		out, _, code := f.invoke("stats")
		if code != 0 || strings.Index(out, "😀") < 0 || strings.Index(out, "\ue000") < 0 || strings.Index(out, "😀") > strings.Index(out, "\ue000") {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("malformed and unsupported sidecars warn without exposing raw contents", func(t *testing.T) { // JS: "stats: a malformed or unsupported sidecar is not accepted and warns without its raw contents"
		f := newCommandFixture(t)
		f.pair("bad", "20260928T120000", "tasker", "done\n", 0, `{"version":99,"submission":"accepted","secret":"do-not-print"}`)
		out, stderr, code := f.invoke("stats", "--json")
		if code != 0 || !strings.Contains(out, `"roles":{}`) || !strings.Contains(stderr, "not a valid attempt sidecar") || strings.Contains(stderr, "do-not-print") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("sidecar is read from the winning TMPDIR mirror pair", func(t *testing.T) { // JS: "stats: the sidecar is read from the winning side of the pair (tmp routing and mirror)"
		f := newCommandFixture(t)
		f.pair("mirror", "20260928T120000", "tasker", "done\n", time.Minute, f.accepted("state-kind", "state-model", "high"))
		tmpBase := filepath.Join(f.tmp, "soho", f.workspace, "reports")
		if err := os.WriteFile(filepath.Join(tmpBase, "mirror-20260928T120000.brief.md"), []byte("You are running as the `tasker` role\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmpBase, "mirror-20260928T120000.md"), []byte("done\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmpBase, "mirror-20260928T120000.dispatch.json"), []byte(f.accepted("tmp-kind", "tmp-model", "low")), 0o600); err != nil {
			t.Fatal(err)
		}
		out, _, code := f.invoke("stats", "--by", "kind", "--json")
		if code != 0 || !strings.Contains(out, `"state-kind":{"tasks":1`) || strings.Contains(out, `"tmp-kind"`) {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("by agent groups reviews only for reviewer-role reports", func(t *testing.T) { // JS: "stats: --by groups the review of the reviewer-role reports only"
		f := newCommandFixture(t)
		f.pair("reviewer", "20260928T120000", "reviewer", "findings: 1 (P0 0, P1 1, P2 0, P3 0) | verdict: fail\n", 0, "")
		f.pair("tasker", "20260928T120001", "tasker", "findings: 4 (P0 0, P1 0, P2 0, P3 4) | verdict: pass\n", 0, "")
		out, _, code := f.invoke("stats", "--by", "agent", "--json")
		if code != 0 || !strings.Contains(out, `"review":{"reviewer":{"header":1,"pass":0,"fail":1`) || strings.Contains(out, `"review":{"tasker"`) {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run("lost briefs retain full-set order and inclusive since boundary", func(t *testing.T) { // JS: "stats: lost_briefs keeps the full-set order and the inclusive --since boundary"
		f := newCommandFixture(t)
		f.pair("alice", "20260928T120001", "tasker", "", 0, "")
		f.pair("bob", "20260928T120000", "tasker", "", 0, "")
		for _, name := range []string{"alice-20260928T120001.md", "bob-20260928T120000.md"} {
			path := filepath.Join(f.briefs, name)
			stamp := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
			if strings.HasPrefix(name, "bob") {
				stamp = stamp.Add(-time.Minute)
			}
			if err := os.Chtimes(path, stamp, stamp); err != nil {
				t.Fatal(err)
			}
		}
		cutoff := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
		out, _, code := f.invoke("stats", "--since", cutoff.Format(time.RFC3339), "--json")
		if code != 0 || !strings.Contains(out, `"lost":1`) || strings.Contains(out, "bob-20260928T120000.md") || !strings.Contains(out, "alice-20260928T120001.md") {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run("an accepted queued pair does not count as not received", func(t *testing.T) { // JS: "stats: an accepted queued pair does not count as not_received"
		f := newCommandFixture(t)
		sc := strings.TrimSuffix(f.accepted("claude", "m", "high"), "}") + `,"arrival":"queued"}`
		f.pair("alice", "20260928T120000", "tasker", "done\n", 0, sc)
		out, _, code := f.invoke("stats", "--json")
		if code != 0 || !strings.Contains(out, `"not_received":0`) {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run("agent groups sort by UTF-16 code units", func(t *testing.T) { // JS: "stats CLI orders agent groups by UTF-16 code units"
		f := newCommandFixture(t)
		f.roster("😀")
		f.roster("\ue000")
		for _, agent := range []string{"😀", "\ue000"} {
			f.pair(agent, "20260928T120000", "tasker", "done\n", 0, "")
		}
		out, _, code := f.invoke("stats", "--by", "agent")
		if code != 0 {
			t.Fatalf("code=%d out=%q", code, out)
		}
		if strings.Index(out, "😀") < 0 || strings.Index(out, "\ue000") < 0 {
			t.Fatalf("missing agent rows: %q", out)
		}
		if strings.Index(out, "😀") > strings.Index(out, "\ue000") {
			t.Fatalf("agent groups must sort by UTF-16 code unit (the emoji's lead surrogate sorts before U+E000): %q", out)
		}
	})
	t.Run("ISO cutoff without an offset uses the local timezone", func(t *testing.T) { // JS: "stats: --since parses an ISO date without an offset in local time"
		// Mutation captured: changing the zone-less ISO location from time.Local to time.UTC includes the pre-cutoff prompt.
		t.Setenv("TZ", "America/Sao_Paulo")
		oldLocal := time.Local
		location, err := time.LoadLocation("America/Sao_Paulo")
		if err != nil {
			t.Fatal(err)
		}
		time.Local = location
		t.Cleanup(func() { time.Local = oldLocal })
		f := newCommandFixture(t)
		before := f.pair("before", "20260928T025900", "tasker", "", 0, "")
		after := f.pair("after", "20260928T030000", "tasker", "", 0, "")
		beforeAt := time.Date(2026, 9, 28, 2, 59, 0, 0, time.UTC)
		afterAt := time.Date(2026, 9, 28, 3, 0, 0, 0, time.UTC)
		if err := os.Chtimes(before, beforeAt, beforeAt); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(after, afterAt, afterAt); err != nil {
			t.Fatal(err)
		}
		out, _, code := f.invoke("stats", "--since", "2026-09-28T00:00", "--json")
		if code != 0 || !strings.Contains(out, `"tasker":{"tasks":1`) {
			t.Fatalf("code=%d out=%s", code, out)
		}
		dateOut, _, dateCode := f.invoke("stats", "--since", "2026-09-28", "--json")
		utcOut, _, utcCode := f.invoke("stats", "--since", "2026-09-28T00:00Z", "--json")
		if dateCode != 0 || !strings.Contains(dateOut, `"tasker":{"tasks":1`) || utcCode != 0 || !strings.Contains(utcOut, `"tasker":{"tasks":2`) {
			t.Fatalf("date code=%d out=%s; UTC code=%d out=%s", dateCode, dateOut, utcCode, utcOut)
		}
	})
	t.Run("pending review dispatches do not count as reviews without headers", func(t *testing.T) { // JS: "stats: dispatches without reports are not reviews without headers"
		// Mutation captured: counting addReview for a prompt without a report increments no_header.
		f := newCommandFixture(t)
		f.pair("pending", "20260928T120000", "reviewer", "", 0, "")
		f.pair("reported", "20260928T120001", "reviewer", "report without review header\n", 0, "")
		out, _, code := f.invoke("stats", "--json")
		if code != 0 || !strings.Contains(out, `"reviewer":{"header":0,"pass":0,"fail":0,"severity":{"P0":0,"P1":0,"P2":0,"P3":0},"no_header":1}`) {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run("table cell widths use UTF-16 units", func(t *testing.T) { // JS: "stats text tables measure string width as UTF-16 units"
		// Mutation captured: using UTF-8 byte length pads accented and private-use roles with the wrong number of spaces.
		f := newCommandFixture(t)
		f.pair("accent", "20260928T120000", "cafés", "done\n", 0, "")
		f.pair("private", "20260928T120001", "\ue000", "done\n", 0, "")
		f.pair("emoji", "20260928T120002", "😀", "done\n", 0, "")
		out, _, code := f.invoke("stats")
		if code != 0 || !strings.Contains(out, "cafés"+strings.Repeat(" ", 6)+"1") || !strings.Contains(out, "\ue000"+strings.Repeat(" ", 10)+"1") || !strings.Contains(out, "😀"+strings.Repeat(" ", 9)+"1") {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("lost briefs sort by UTF-16 code units", func(t *testing.T) { // JS: "stats sorts pairs with JavaScript string ordering"
		// Mutation captured: comparing UTF-8 bytes puts U+E000 before U+1F600 in lost_briefs.
		f := newCommandFixture(t)
		f.pair("\ue000", "20260928T120000", "tasker", "", 0, "")
		f.pair("😀", "20260928T120001", "tasker", "", 0, "")
		out, _, code := f.invoke("stats", "--json")
		first, second := strings.Index(out, "😀-20260928T120001.md"), strings.Index(out, "\ue000-20260928T120000.md")
		if code != 0 || first < 0 || second < 0 || first >= second {
			t.Fatalf("code=%d order invalid: %s", code, out)
		}
	})
	t.Run("JS differential fixture: text and JSON formats", func(t *testing.T) { // JS: "differential generator captures text and --json output for briefs, reports, sidecars and amendments"
		f := newCommandFixture(t)
		f.pair("alice", "20260928T120000", "reviewer", "findings: 1 (p0 0, p1 0, p2 1, p3 0) | verdict: pass\n- [partial] fixture\n", 0, f.accepted("claude", "sonnet", "high"))
		amend := filepath.Join(f.briefs, "alice-20260928T120001.md")
		_ = os.WriteFile(amend, []byte("# Amendment to your current brief\n"), 0o600)
		_ = os.WriteFile(filepath.Join(f.reports, "alice-20260928T120001.md"), []byte("amended report\n"), 0o600)
		fixtureBytes, err := os.ReadFile(filepath.Join(packageDir, "..", "stats", "testdata", "stats.json"))
		if err != nil {
			t.Fatal(err)
		}
		var want struct {
			Text string `json:"text"`
			JSON string `json:"json"`
		}
		if err := json.Unmarshal(fixtureBytes, &want); err != nil {
			t.Fatal(err)
		}
		textOut, _, textCode := f.invoke("stats")
		jsonOut, _, jsonCode := f.invoke("stats", "--json")
		if textCode != 0 || jsonCode != 0 || textOut != want.Text || jsonOut != want.JSON {
			t.Fatalf("text code=%d equal=%t\njson code=%d equal=%t\ntext=%q\nwant=%q\njson=%q\nwant=%q", textCode, textOut == want.Text, jsonCode, jsonOut == want.JSON, textOut, want.Text, jsonOut, want.JSON)
		}
	})
	t.Run("roles, prompt-to-report times and [partial] per role", func(t *testing.T) { // JS: "stats: roles, prompt-to-report times and [partial] per role"
		f := newCommandFixture(t)
		f.pair("alice", "20260928T120000", "implementer", "findings: 1 (p0 0, p1 0, p2 1, p3 0) | verdict: pass\n- [partial] missing evidence\n", 90*time.Minute, f.accepted("claude", "sonnet", "high"))
		out, _, code := f.invoke("stats", "--json")
		if code != 0 || !strings.Contains(out, `"implementer":{"tasks":1`) || !strings.Contains(out, `"avg":90`) || !strings.Contains(out, `"partials":1`) {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run("median durations are sorted and partials are summed", func(t *testing.T) { // JS: "stats: durations use the median and partial markers aggregate"
		// Mutation captured: replacing the median with the maximum or overwriting accumulated partials changes the aggregate.
		f := newCommandFixture(t)
		f.pair("a", "20260928T120000", "tasker", "- [partial] one\n", time.Minute, "")
		f.pair("b", "20260928T120001", "tasker", "- [partial] two\n", 2*time.Minute, "")
		f.pair("c", "20260928T120002", "tasker", "- [partial] three\n", 30*time.Minute, "")
		out, _, code := f.invoke("stats", "--json")
		if code != 0 || !strings.Contains(out, `"median":2`) || !strings.Contains(out, `"partials":3`) {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run("an amendment counts under the role of the previous prompt of the same agent", func(t *testing.T) { // JS: "stats: an amendment counts under the role of the previous prompt of the same agent"
		// Mutation captured: treating an amendment as a task removes the amendment count.
		f := newCommandFixture(t)
		f.pair("alice", "20260928T120000", "planner", "done\n", time.Minute, "")
		p := filepath.Join(f.briefs, "alice-20260928T120001.md")
		_ = os.WriteFile(p, []byte("# Amendment to your current brief\n"), 0o600)
		out, _, code := f.invoke("stats", "--json")
		if code != 0 || !strings.Contains(out, `"planner":{"tasks":1,"briefs":1,"amendments":1`) {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run("a report-less prompt is pending for the live last dispatch, lost otherwise", func(t *testing.T) { // JS: "stats: a report-less prompt is pending for the live last dispatch, lost otherwise"
		// Mutation captured: swapping the roster and last-dispatch condition flips the per-agent pending and lost groups.
		f := newCommandFixture(t)
		f.pair("alice", "20260928T120000", "tasker", "", 0, "")
		f.roster("alice")
		f.pair("bob", "20260928T120001", "tasker", "", 0, "")
		out, _, code := f.invoke("stats", "--by", "agent", "--json")
		if code != 0 || !strings.Contains(out, `"alice":{"tasks":1,"briefs":1,"amendments":0,"reuses":0,"no_report":{"pending":1,"lost":0}`) || !strings.Contains(out, `"bob":{"tasks":1,"briefs":1,"amendments":0,"reuses":0,"no_report":{"pending":0,"lost":1}`) {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run("--since filters by the prompt mtime; an invalid date dies 2", func(t *testing.T) { // JS: "stats: --since filters by the prompt mtime; an invalid date dies 2"
		f := newCommandFixture(t)
		f.pair("alice", "20260928T120000", "tasker", "done", 0, "")
		_, errOut, code := f.invoke("stats", "--since", "2026-02-30")
		if code != 2 || !strings.Contains(errOut, "stats: --since expects") {
			t.Fatalf("code=%d stderr=%s", code, errOut)
		}
	})
	t.Run("--json prints one compact object with stable shape", func(t *testing.T) { // JS: "stats: --json prints one compact object with the stable shape"
		f := newCommandFixture(t)
		f.pair("alice", "20260928T120000", "tasker", "", 0, "")
		out, _, code := f.invoke("stats", "--json")
		if code != 0 || !strings.HasPrefix(out, "{") || !strings.HasSuffix(out, "}\n") || strings.Contains(out, "\n\n") {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("--by kind, model and effort use the sidecar snapshot", func(t *testing.T) { // JS: "stats: --by kind|model|effort uses the sidecar snapshot, not the current roster"
		f := newCommandFixture(t)
		f.pair("alice", "20260928T120000", "tasker", "done", 0, f.accepted("claude", "sonnet-4", "high"))
		for _, dim := range []string{"kind", "model", "effort"} {
			out, _, code := f.invoke("stats", "--by", dim, "--json")
			want := map[string]string{"kind": "claude", "model": "sonnet-4", "effort": "high"}[dim]
			if code != 0 || !strings.Contains(out, `"by":"`+dim+`"`) || !strings.Contains(out, want) {
				t.Fatalf("by %s: code=%d out=%s", dim, code, out)
			}
		}
	})
	t.Run("only an accepted sidecar is a task", func(t *testing.T) { // JS: "stats: only an accepted sidecar is a task; attempted/failed never count and never become lost"
		f := newCommandFixture(t)
		f.pair("alice", "20260928T120000", "tasker", "", 0, `{"version":1,"submission":"attempted","kind":"claude","model":"m","effort":"high"}`)
		out, _, code := f.invoke("stats", "--json")
		if code != 0 || !strings.Contains(out, `"roles":{}`) || strings.Contains(out, `"lost":1`) {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run("an accepted pair with not-received counts once per query", func(t *testing.T) { // JS: "stats: an accepted pair with the not-received arrival mark counts once, per query"
		// Mutation captured: ignoring the accepted sidecar arrival mark drops not_received.
		f := newCommandFixture(t)
		sc := strings.TrimSuffix(f.accepted("claude", "m", "high"), "}") + `,"arrival":"not-received"}`
		f.pair("alice", "20260928T120000", "tasker", "done", 0, sc)
		out, _, code := f.invoke("stats", "--json")
		if code != 0 || !strings.Contains(out, `"not_received":1`) {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run("review table covers ui-reviewer and inspector roles", func(t *testing.T) { // JS: "stats: the review table covers ui-reviewer and inspector too"
		f := newCommandFixture(t)
		for i, role := range []string{"ui-reviewer", "inspector"} {
			name := fmt.Sprintf("agent%d", i)
			f.pair(name, fmt.Sprintf("20260928T12000%d", i), role, "findings: 1 (P0 0, P1 0, P2 1, P3 0) | verdict: pass\n", 0, "")
		}
		out, _, code := f.invoke("stats", "--json")
		if code != 0 || !strings.Contains(out, `"inspector":{"header":1,"pass":1`) || !strings.Contains(out, `"ui-reviewer":{"header":1,"pass":1`) {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run("an empty state directory prints the no-dispatches message", func(t *testing.T) { // JS: "stats: an empty state dir prints the no-dispatches message and exits 0"
		f := newCommandFixture(t)
		out, _, code := f.invoke("stats")
		if code != 0 || !strings.Contains(out, "no dispatches recorded") {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("legacy pair without sidecar remains counted under unknown", func(t *testing.T) { // JS: "stats: a legacy pair (no sidecar) keeps today's behavior and groups under (unknown)"
		f := newCommandFixture(t)
		f.pair("alice", "20260928T120000", "implementer", "done\n", time.Minute, "")
		out, _, code := f.invoke("stats", "--by", "kind", "--json")
		if code != 0 || !strings.Contains(out, `"(unknown)":{"tasks":1`) {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run("missing or invalid by dimension exits two", func(t *testing.T) { // JS: "stats: --by requires a known dimension (missing or invalid dies 2)"
		f := newCommandFixture(t)
		for _, args := range [][]string{{"stats", "--by"}, {"stats", "--by", "banana"}} {
			_, stderr, code := f.invoke(args...)
			if code != 2 || !strings.Contains(stderr, "stats: --by expects") {
				t.Fatalf("args=%v code=%d stderr=%q", args, code, stderr)
			}
		}
	})
	t.Run("since accepts strict ISO shapes and rejects permissive date spellings", func(t *testing.T) { // JS: "stats: --since accepts only AAAA-MM-DD or a strict ISO 8601 shape"
		f := newCommandFixture(t)
		f.pair("alice", "20260928T120000", "implementer", "done\n", 0, "")
		for _, date := range []string{"2026-09-28", "2026-09-28T12:00", "2026-09-28T12:00:30", "2026-09-28T12:00:30.123Z", "2026-09-28T12:00+02:00", "2026-09-28T12:00+0200"} {
			_, stderr, code := f.invoke("stats", "--since", date)
			if code != 0 {
				t.Errorf("accepted date %q: code=%d stderr=%q", date, code, stderr)
			}
		}
		for _, date := range []string{"2026-9-28", "09/28/2026", "2026-02-30", "2026-09-28 12:00"} {
			_, stderr, code := f.invoke("stats", "--since", date)
			if code != 2 || !strings.Contains(stderr, "stats: --since expects") {
				t.Errorf("rejected date %q: code=%d stderr=%q", date, code, stderr)
			}
		}
	})
	t.Run("rejected dispatch does not replace the latest accepted pending task", func(t *testing.T) { // JS: "stats: a rejected dispatch does not supersede the last counted dispatch for pending/lost"
		f := newCommandFixture(t)
		f.roster("alice")
		f.pair("alice", "20260928T120000", "implementer", "", 0, f.accepted("grok", "m1", "full"))
		f.pair("alice", "20260928T120001", "reviewer", "", 0, `{"version":1,"kind":"grok","model":"m2","effort":"full","submission":"failed"}`)
		out, _, code := f.invoke("stats", "--json")
		if code != 0 || !strings.Contains(out, `"pending":1,"lost":0`) || !strings.Contains(out, `"tasks":1`) {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run("failed dispatch does not change role inherited by amendment", func(t *testing.T) { // JS: "stats: a failed dispatch does not change the role inherited by a later accepted amendment"
		f := newCommandFixture(t)
		f.pair("alice", "20260928T120000", "implementer", "done\n", time.Minute, f.accepted("grok", "m1", "full"))
		f.pair("alice", "20260928T120001", "reviewer", "", 0, `{"version":1,"kind":"grok","model":"m1","effort":"full","submission":"failed"}`)
		amend := filepath.Join(f.briefs, "alice-20260928T120002.md")
		if err := os.WriteFile(amend, []byte("# Amendment to your current brief\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.reports, "alice-20260928T120002.md"), []byte("amended\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.briefs, "alice-20260928T120002.dispatch.json"), []byte(f.accepted("grok", "m1", "full")), 0o600); err != nil {
			t.Fatal(err)
		}
		out, _, code := f.invoke("stats", "--json")
		if code != 0 || !strings.Contains(out, `"implementer":{"tasks":1,"briefs":1,"amendments":1`) || strings.Contains(out, `"reviewer":`) {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run("only absent sidecar means legacy; present invalid sidecars are excluded", func(t *testing.T) { // JS: "stats: only an ENOENT sidecar is legacy; present-but-unreadable or field-less sidecars are invalid"
		f := newCommandFixture(t)
		f.pair("legacy", "20260928T120000", "implementer", "", 0, "")
		bad := filepath.Join(f.briefs, "bad-20260928T120000.dispatch.json")
		if err := os.Mkdir(bad, 0o700); err != nil {
			t.Fatal(err)
		}
		f.pair("bad", "20260928T120000", "implementer", "", 0, "")
		out, stderr, code := f.invoke("stats", "--json")
		if code != 0 || !strings.Contains(stderr, "bad-20260928T120000.dispatch.json is not a valid attempt sidecar") || strings.Contains(out, `"tasks":2`) {
			t.Fatalf("code=%d out=%s stderr=%q", code, out, stderr)
		}
	})
	t.Run("a new task after a role change is counted as a reuse", func(t *testing.T) { // JS: "stats: a non-amendment is a reuse when the immediately previous counted known role differs"
		// A5: the reuse rule now counts any earlier non-amendment pair of the same agent, role change or not; this case keeps its assertion (reuses 1) under the new rule.
		f := newCommandFixture(t)
		f.pair("alice", "20260928T120000", "implementer", "done\n", time.Minute, "")
		f.pair("alice", "20260928T120001", "reviewer", "done\n", time.Minute, "")
		out, _, code := f.invoke("stats", "--json")
		if code != 0 || !strings.Contains(out, `"reviewer":{"tasks":0,"briefs":1,"amendments":0,"reuses":1`) {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run("lost briefs expose their composed prompt path", func(t *testing.T) { // JS: "stats: a lost pair exposes its composed prompt path; pending, tmp and mirror keep their exact paths"
		f := newCommandFixture(t)
		brief := f.pair("gone", "20260928T120000", "implementer", "", 0, "")
		out, _, code := f.invoke("stats", "--json")
		var result struct {
			LostBriefs map[string][]string `json:"lost_briefs"`
		}
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("decode stats output %q: %v", out, err)
		}
		gotPath := ""
		if paths := result.LostBriefs["implementer"]; len(paths) == 1 {
			gotPath = filepath.ToSlash(filepath.Clean(paths[0]))
		}
		if code != 0 || gotPath != filepath.ToSlash(filepath.Clean(brief)) || !strings.Contains(out, `"lost":1`) {
			t.Fatalf("code=%d out=%s expected path=%s", code, out, brief)
		}
	})
	t.Run("prototype-like grouping keys are preserved", func(t *testing.T) { // JS: "stats: --by keeps prototype-ish dimension keys (__proto__, constructor) in JSON and text"
		f := newCommandFixture(t)
		for i, model := range []string{"__proto__", "constructor"} {
			f.pair(fmt.Sprintf("a%d", i), fmt.Sprintf("20260928T12000%d", i), "implementer", "done\n", time.Minute, f.accepted("grok", model, "full"))
		}
		out, _, code := f.invoke("stats", "--by", "model", "--json")
		if code != 0 || !strings.Contains(out, `"__proto__":{"tasks":1`) || !strings.Contains(out, `"constructor":{"tasks":1`) {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run("an amendment without its own report is not lost when the next dispatch has one", func(t *testing.T) { // A5 (Go fix; the JS product keeps the old behavior): an amendment never enters tasks, lost, pending or lost_briefs
		f := newCommandFixture(t)
		f.pair("alice", "20260928T120000", "tasker", "done\n", time.Minute, "")
		amend := filepath.Join(f.briefs, "alice-20260928T120001.md")
		if err := os.WriteFile(amend, []byte("# Amendment to your current brief\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		f.pair("alice", "20260928T120002", "tasker", "done\n", time.Minute, "")
		out, _, code := f.invoke("stats", "--json")
		if code != 0 || !strings.Contains(out, `"tasker":{"tasks":1,"briefs":2,"amendments":1,"reuses":1,"no_report":{"pending":0,"lost":0}`) || !strings.Contains(out, `"lost_briefs":{}`) {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run("a report-less prompt stays pending when only an amendment follows", func(t *testing.T) { // A5 (Go fix): the last non-amendment pair of a roster agent stays pending; the amendment does not count as the last pair nor as lost
		f := newCommandFixture(t)
		f.roster("alice")
		f.pair("alice", "20260928T120000", "tasker", "", 0, "")
		amend := filepath.Join(f.briefs, "alice-20260928T120001.md")
		if err := os.WriteFile(amend, []byte("# Amendment to your current brief\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		out, _, code := f.invoke("stats", "--json")
		if code != 0 || !strings.Contains(out, `"tasker":{"tasks":1,"briefs":1,"amendments":1,"reuses":0,"no_report":{"pending":1,"lost":0}`) || !strings.Contains(out, `"lost_briefs":{}`) {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run("a second non-amendment dispatch on the same role counts as a reuse", func(t *testing.T) { // A5 (Go fix): reuses counts every non-amendment pair whose agent had an earlier non-amendment pair, role change or not
		f := newCommandFixture(t)
		f.pair("alice", "20260928T120000", "tasker", "done\n", time.Minute, "")
		f.pair("alice", "20260928T120001", "tasker", "done\n", time.Minute, "")
		out, _, code := f.invoke("stats", "--json")
		if code != 0 || !strings.Contains(out, `"tasker":{"tasks":1,"briefs":2,"amendments":0,"reuses":1,"no_report":{"pending":0,"lost":0}`) {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run("three non-amendment dispatches to one agent count one task and two reuses", func(t *testing.T) { // A5 (Go fix): reuses follows the earlier-pair rule, with or without a role change
		f := newCommandFixture(t)
		f.pair("alice", "20260928T120000", "tasker", "done\n", time.Minute, "")
		f.pair("alice", "20260928T120001", "reviewer", "done\n", time.Minute, "")
		f.pair("alice", "20260928T120002", "scouter", "done\n", time.Minute, "")
		out, _, code := f.invoke("stats", "--json")
		if code != 0 || !strings.Contains(out, `"tasker":{"tasks":1,"briefs":1,"amendments":0,"reuses":0`) || !strings.Contains(out, `"reviewer":{"tasks":0,"briefs":1,"amendments":0,"reuses":1`) || !strings.Contains(out, `"scouter":{"tasks":0,"briefs":1,"amendments":0,"reuses":1`) {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run("a report-less prompt of an agent not in the roster stays lost", func(t *testing.T) { // A5 (Go fix): lost is unchanged when the agent is not in the roster
		f := newCommandFixture(t)
		f.pair("gone", "20260928T120000", "tasker", "", 0, "")
		out, _, code := f.invoke("stats", "--json")
		if code != 0 || !strings.Contains(out, `"tasker":{"tasks":1,"briefs":1,"amendments":0,"reuses":0,"no_report":{"pending":0,"lost":1}`) || !strings.Contains(out, `"lost_briefs":{"tasker":[`) || !strings.Contains(out, "gone-20260928T120000.md") {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
}

func TestCollectPortedCases(t *testing.T) {
	t.Run("collect outside Herdr exits with the environment diagnostic", func(t *testing.T) { // JS: "collect: refuses to run outside Herdr"
		// Mutation captured: removing collect from the RequireEnv gate permits the command outside Herdr.
		f := newCommandFixture(t)
		env := f.env()
		delete(env, "HERDR_ENV")
		env["PATH"] = ""
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var out, errOut bytes.Buffer
		platform.Stdout, platform.Stderr = &out, &errOut
		code := Run([]string{"collect", "alice"}, env)
		platform.Stdout, platform.Stderr = oldOut, oldErr
		if code != 2 || out.Len() != 0 || !strings.Contains(errOut.String(), "not running inside Herdr (HERDR_ENV != 1)") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out.String(), errOut.String())
		}
	})
	t.Run("collect rejects a report that cannot be read", func(t *testing.T) { // JS: "collect: an unreadable report is an error"
		// Mutation captured: ignoring the report read error returns success after printing only the marker.
		f := newCommandFixture(t)
		name := "alice-20260928T120000"
		report := filepath.Join(f.reports, name+".md")
		makeUnreadableReport(t, report)
		if err := os.WriteFile(filepath.Join(f.state, "ws", "last-report-alice"), []byte(report+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		out, errOut, code := f.invoke("collect", "alice")
		if code != 4 || !strings.Contains(out, "<!-- report: "+report+" -->") || !strings.Contains(errOut, "collect: cannot read "+report) {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
		}
	})
	t.Run("collect preserves CRLF bytes from the report", func(t *testing.T) { // JS: "collect without verify preserves report bytes"
		// Mutation captured: using ReadTextFile normalizes CRLF and changes the collected report bytes.
		f := newCommandFixture(t)
		name := "alice-20260928T120000"
		report := filepath.Join(f.reports, name+".md")
		content := []byte("first\r\nsecond\r\n")
		if err := os.WriteFile(report, content, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.state, "ws", "last-report-alice"), []byte(report+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		out, _, code := f.invoke("collect", "alice")
		if code != 0 || !bytes.HasSuffix([]byte(out), content) {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("collect recognizes a non-breaking space before a sha256 path", func(t *testing.T) { // JS: "collect --verify recognizes JavaScript whitespace"
		// Mutation captured: RE2's ASCII-only whitespace leaves the NBSP-separated digest line unparsed.
		f := newCommandFixture(t)
		name := "alice-20260928T120000"
		body := []byte("verified NBSP")
		workerFile := filepath.Join(f.tmp, "nbsp file.txt")
		if err := os.WriteFile(workerFile, body, 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		report := filepath.Join(f.reports, name+".md")
		if err := os.WriteFile(report, []byte(fmt.Sprintf("%x\u00a0%s\n", sum, workerFile)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.state, "ws", "last-report-alice"), []byte(report+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		out, _, code := f.invoke("collect", "alice", "--verify")
		if code != 0 || !strings.Contains(out, "ok "+workerFile+"\nverified 1: ok 1, changed 0, missing 0") {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("working agent without a report gets the wait pointer", func(t *testing.T) { // JS: "collect: a working agent without a report gets the wait pointer, not the terminal"
		f := newCommandFixture(t)
		f.roster("alice")
		fakeDir := filepath.Join(f.tmp, "fakebin")
		_, err := fakecli.Install(t, fakeDir, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "alice"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`}})
		if err != nil {
			t.Fatal(err)
		}
		env := envFrom(fakecli.Env(f.env().List(), fakeDir))
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var out, errOut bytes.Buffer
		platform.Stdout, platform.Stderr = &out, &errOut
		code := Run([]string{"collect", "alice"}, env)
		platform.Stdout, platform.Stderr = oldOut, oldErr
		if code != 4 || out.Len() != 0 || !strings.Contains(errOut.String(), "'alice' is working") || !strings.Contains(errOut.String(), "soho wait alice") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out.String(), errOut.String())
		}
	})
	t.Run("collect without verify preserves the report marker and task pointer", func(t *testing.T) { // JS: "collect: without --verify the behavior is unchanged"
		// Mutation captured: omitting the task-report pointer marker loses its observable output.
		f := newCommandFixture(t)
		name := "alice-20260928T120000"
		report := filepath.Join(f.reports, name+".md")
		content := "report body\n"
		_ = os.WriteFile(report, []byte(content), 0o600)
		_ = os.WriteFile(filepath.Join(f.state, "ws", "last-report-alice"), []byte(report+"\n"), 0o600)
		task := filepath.Join(f.reports, "task.md")
		_ = os.WriteFile(task, []byte("task report"), 0o600)
		pointer := fmt.Sprintf(`{"version":1,"task_report":%q,"current":%q,"history":[]}`, task, task)
		_ = os.WriteFile(filepath.Join(f.state, "ws", "task-report-alice.json"), []byte(pointer), 0o600)
		out, _, code := f.invoke("collect", "alice")
		if code != 0 || !strings.Contains(out, "<!-- report: "+report+" -->") || !strings.Contains(out, "<!-- task report: "+task+" -->") || !strings.HasSuffix(out, content) {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("collect verify validates hashes and emits the summary", func(t *testing.T) { // JS: "collect --verify: every file ok exits 0, relative paths use the worker cwd"
		// Mutation captured: reversing the SHA-256 equality check marks a valid file changed.
		f := newCommandFixture(t)
		name := "alice-20260928T120000"
		body := []byte("contents")
		workerFile := filepath.Join(f.tmp, "worker file.txt")
		_ = os.WriteFile(workerFile, body, 0o600)
		sum := sha256.Sum256(body)
		report := filepath.Join(f.reports, name+".md")
		_ = os.WriteFile(report, []byte(fmt.Sprintf("%x  %s\n", sum, workerFile)), 0o600)
		_ = os.WriteFile(filepath.Join(f.state, "ws", "last-report-alice"), []byte(report+"\n"), 0o600)
		f.roster("alice")
		out, _, code := f.invoke("collect", "alice", "--verify")
		if code != 0 || !strings.Contains(out, "verified 1: ok 1, changed 0, missing 0") {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run("collect verify drops a trailing comment after the path", func(t *testing.T) {
		// A report line "<sha256>  <path>  # note" (ai-memory feedback): sha256sum never
		// writes the comment, so the path without it is the one verified. A file whose
		// name really holds " # " still verifies under its own name.
		f := newCommandFixture(t)
		name := "alice-20260928T120000"
		body := []byte("contents")
		workerFile := filepath.Join(f.tmp, "worker file.txt")
		_ = os.WriteFile(workerFile, body, 0o600)
		hashFile := filepath.Join(f.tmp, "odd # name.txt")
		_ = os.WriteFile(hashFile, body, 0o600)
		sum := sha256.Sum256(body)
		report := filepath.Join(f.reports, name+".md")
		lines := fmt.Sprintf("%x  %s  # new file, reviewed\n%x  %s\n", sum, workerFile, sum, hashFile)
		_ = os.WriteFile(report, []byte(lines), 0o600)
		_ = os.WriteFile(filepath.Join(f.state, "ws", "last-report-alice"), []byte(report+"\n"), 0o600)
		f.roster("alice")
		out, _, code := f.invoke("collect", "alice", "--verify")
		if code != 0 || !strings.Contains(out, "verified 2: ok 2, changed 0, missing 0") || !strings.Contains(out, "ok "+workerFile+"\n") || !strings.Contains(out, "ok "+hashFile+"\n") {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run("collect verify keeps a comment when the bare path is missing too", func(t *testing.T) {
		f := newCommandFixture(t)
		name := "alice-20260928T120000"
		sum := sha256.Sum256([]byte("gone"))
		gone := filepath.Join(f.tmp, "gone.txt")
		report := filepath.Join(f.reports, name+".md")
		_ = os.WriteFile(report, []byte(fmt.Sprintf("%x  %s  # note\n", sum, gone)), 0o600)
		_ = os.WriteFile(filepath.Join(f.state, "ws", "last-report-alice"), []byte(report+"\n"), 0o600)
		f.roster("alice")
		out, _, code := f.invoke("collect", "alice", "--verify")
		if code != 16 || !strings.Contains(out, "missing "+gone+"  # note\n") || !strings.Contains(out, "verified 1: ok 0, changed 0, missing 1") {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run("collect verify never verifies another file through a # inside a directory", func(t *testing.T) {
		// R-RC5B: "<sha>  decoy # notes/missing.go" named a file that does not exist; cutting
		// at the first " #" verified "decoy" instead (a false ok). A cut-off text with a path
		// separator is part of the path; a name with " # " plus a note still verifies.
		f := newCommandFixture(t)
		name := "alice-20260928T120000"
		body := []byte("contents")
		sum := sha256.Sum256(body)
		decoy := filepath.Join(f.tmp, "decoy")
		_ = os.WriteFile(decoy, body, 0o600)
		odd := filepath.Join(f.tmp, "odd # name.txt")
		_ = os.WriteFile(odd, body, 0o600)
		report := filepath.Join(f.reports, name+".md")
		lines := fmt.Sprintf("%x  %s # notes/missing.go\n%x  %s  # reviewed\n", sum, decoy, sum, odd)
		_ = os.WriteFile(report, []byte(lines), 0o600)
		_ = os.WriteFile(filepath.Join(f.state, "ws", "last-report-alice"), []byte(report+"\n"), 0o600)
		f.roster("alice")
		out, _, code := f.invoke("collect", "alice", "--verify")
		if code != 16 || !strings.Contains(out, "missing "+decoy+" # notes/missing.go\n") || !strings.Contains(out, "ok "+odd+"\n") || !strings.Contains(out, "verified 2: ok 1, changed 0, missing 1") {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run("collect missing agent argument exits 1", func(t *testing.T) { // JS: "collect with no agent uses the parameter error"
		f := newCommandFixture(t)
		_, errOut, code := f.invoke("collect")
		if code != 1 || !strings.Contains(errOut, "agent: Parameter not set") {
			t.Fatalf("code=%d stderr=%s", code, errOut)
		}
	})
	t.Run("changed file is reported and exits sixteen", func(t *testing.T) { // JS: "collect --verify: a changed file is reported and exits 16"
		f := newCommandFixture(t)
		name := "alice-20260928T120000"
		body := []byte("before")
		workerFile := filepath.Join(f.tmp, "worker file.txt")
		_ = os.WriteFile(workerFile, []byte("after"), 0o600)
		sum := sha256.Sum256(body)
		report := filepath.Join(f.reports, name+".md")
		_ = os.WriteFile(report, []byte(fmt.Sprintf("%x  %s\n", sum, workerFile)), 0o600)
		_ = os.WriteFile(filepath.Join(f.state, "ws", "last-report-alice"), []byte(report+"\n"), 0o600)
		f.roster("alice")
		out, _, code := f.invoke("collect", "alice", "--verify")
		if code != 16 || !strings.Contains(out, "changed "+workerFile) || !strings.Contains(out, "changed 1, missing 0") {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("missing file is reported and exits sixteen", func(t *testing.T) { // JS: "collect --verify: a missing file is reported and exits 16"
		f := newCommandFixture(t)
		name := "alice-20260928T120000"
		missing := filepath.Join(f.tmp, "missing file.txt")
		sum := sha256.Sum256([]byte("expected"))
		report := filepath.Join(f.reports, name+".md")
		_ = os.WriteFile(report, []byte(fmt.Sprintf("%x  %s\n", sum, missing)), 0o600)
		_ = os.WriteFile(filepath.Join(f.state, "ws", "last-report-alice"), []byte(report+"\n"), 0o600)
		f.roster("alice")
		out, _, code := f.invoke("collect", "alice", "--verify")
		if code != 16 || !strings.Contains(out, "missing "+missing) || !strings.Contains(out, "missing 1") {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("no hash lines is not a pass", func(t *testing.T) { // JS exits 0 here; the Go exits 16 (PLAN §9, A17)
		f := newCommandFixture(t)
		name := "alice-20260928T120000"
		report := filepath.Join(f.reports, name+".md")
		_ = os.WriteFile(report, []byte("no hashes\n"), 0o600)
		_ = os.WriteFile(filepath.Join(f.state, "ws", "last-report-alice"), []byte(report+"\n"), 0o600)
		f.roster("alice")
		out, _, code := f.invoke("collect", "alice", "--verify")
		if code != 16 || !strings.Contains(out, "no sha256 lines in "+report+": nothing verified") {
			t.Fatalf("plain report: code=%d out=%q", code, out)
		}
		// A Markdown table of hashes is not read either.
		_ = os.WriteFile(report, []byte("| file | sha256 |\n|---|---|\n| a.go | "+strings.Repeat("a", 64)+" |\n"), 0o600)
		out, _, code = f.invoke("collect", "alice", "--verify")
		if code != 16 || !strings.Contains(out, "no sha256 lines in "+report+": nothing verified") {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("no report retains the existing collect error", func(t *testing.T) { // JS: "collect --verify: no report keeps the error collect already gives"
		f := newCommandFixture(t)
		f.roster("alice")
		_, stderr, code := f.invoke("collect", "alice", "--verify")
		if code != 4 || !strings.Contains(stderr, "no report file yet") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
	})
	t.Run("a report directory is unreadable for verification and exits four", func(t *testing.T) { // JS: "collect --verify: a report that exists but cannot be read is an error (exit 4)"
		f := newCommandFixture(t)
		name := "alice-20260928T120000"
		report := filepath.Join(f.reports, name+".md")
		makeUnreadableReport(t, report)
		if err := os.WriteFile(filepath.Join(f.state, "ws", "last-report-alice"), []byte(report+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, stderr, code := f.invoke("collect", "alice", "--verify")
		if code != 4 || !strings.Contains(stderr, "collect --verify: cannot read "+report) {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
	})
	t.Run("relative worker cwd resolves from the project root", func(t *testing.T) { // JS: "collect --verify: a relative worker cwd resolves against the project root, not the collector cwd"
		f := newCommandFixture(t)
		projectRoot, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		worker := filepath.Join(projectRoot, "work", "alice")
		file := filepath.Join(worker, "src", "source.go")
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			t.Fatal(err)
		}
		body := []byte("package sample\n")
		if err := os.WriteFile(file, body, 0o600); err != nil {
			t.Fatal(err)
		}
		name := "alice-20260928T120000"
		report := filepath.Join(f.reports, name+".md")
		sum := sha256.Sum256(body)
		if err := os.WriteFile(report, []byte(fmt.Sprintf("%x  src/source.go\n", sum)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.state, "ws", "last-report-alice"), []byte(report+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.state, "ws", "agents.tsv"), []byte("# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\n"+"alice\tp1\tgrok\timplementer\txai\t1\twork/alice\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		oldCwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(projectRoot); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Chdir(oldCwd) }()
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var stdout, stderr bytes.Buffer
		platform.Stdout, platform.Stderr = &stdout, &stderr
		code := Run([]string{"collect", "alice", "--verify"}, f.env())
		platform.Stdout, platform.Stderr = oldOut, oldErr
		if code != 0 || !strings.Contains(stdout.String(), "ok "+file+"\nverified 1: ok 1, changed 0, missing 0\n") {
			t.Fatalf("code=%d out=%q stderr=%q expected=%s", code, stdout.String(), stderr.String(), file)
		}
	})
	t.Run("missing temporary original is read from the state directory copy", func(t *testing.T) { // JS: "collect: a gone $TMPDIR original is read from the state-dir copy"
		f := newCommandFixture(t)
		name := "alice-20260928T120000.md"
		original := filepath.Join(f.tmp, "soho", "ws", "reports", name)
		copyPath := filepath.Join(f.state, "ws", "reports", name)
		if err := os.WriteFile(copyPath, []byte("state copy\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.state, "ws", "last-report-alice"), []byte(original+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		out, _, code := f.invoke("collect", "alice")
		if code != 0 || !strings.Contains(out, "<!-- report: "+copyPath+" -->") || !strings.HasSuffix(out, "state copy\n") {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
}

func TestStatsAndCollectWithHerdrInPathDoNotPanic(t *testing.T) {
	// Mutation captured: using commandConfig for stats/collect friction paths dereferences a nil config.
	f := newCommandFixture(t)
	project := t.TempDir()
	previousCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(project); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previousCwd) })
	state := filepath.Join(project, ".soho", f.workspace)
	reports := filepath.Join(state, "reports")
	if err := os.MkdirAll(reports, 0o700); err != nil {
		t.Fatal(err)
	}
	name := "alice-20260928T120000"
	report := filepath.Join(reports, name+".md")
	if err := os.WriteFile(report, []byte("report body\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "last-report-alice"), []byte(report+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := f.env()
	delete(env, "SOHO_DIR")
	invoke := func(args ...string) (string, string, int) {
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var out, errOut bytes.Buffer
		platform.Stdout, platform.Stderr = &out, &errOut
		defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
		code := Run(args, env)
		return out.String(), errOut.String(), code
	}
	statsOut, statsErr, statsCode := invoke("stats")
	collectOut, collectErr, collectCode := invoke("collect", "alice")
	if statsCode != 0 || statsErr != "" || !strings.Contains(statsOut, "no dispatches recorded under ") {
		t.Fatalf("stats code=%d stdout=%q stderr=%q", statsCode, statsOut, statsErr)
	}
	if collectCode != 0 || collectErr != "" || !strings.Contains(collectOut, "<!-- report: "+report+" -->\nreport body\n") {
		t.Fatalf("collect code=%d stdout=%q stderr=%q", collectCode, collectOut, collectErr)
	}
}

func TestStatsKindModelDimension(t *testing.T) {
	t.Run("kind-model groups two kinds of the same model separately, text and JSON", func(t *testing.T) {
		// Mutation captured: grouping by model alone mixes harnesses that run the same model.
		f := newCommandFixture(t)
		f.pair("a", "20260928T120000", "tasker", "done\n", 0, f.accepted("pi", "qwen3.8-27b", "high"))
		f.pair("b", "20260928T120001", "tasker", "done\n", 0, f.accepted("opencode", "qwen3.8-27b", "high"))
		out, _, code := f.invoke("stats", "--by", "kind-model", "--json")
		if code != 0 || !strings.Contains(out, `"by":"kind-model"`) || !strings.Contains(out, `"pi/qwen3.8-27b":{"tasks":1`) || !strings.Contains(out, `"opencode/qwen3.8-27b":{"tasks":1`) {
			t.Fatalf("code=%d out=%s", code, out)
		}
		out, _, code = f.invoke("stats", "--by", "kind-model")
		if code != 0 || !strings.Contains(out, "tasks by kind-model:") || !strings.Contains(out, "pi/qwen3.8-27b") || !strings.Contains(out, "opencode/qwen3.8-27b") {
			t.Fatalf("code=%d out=%s", code, out)
		}
		out, _, code = f.invoke("stats", "--by", "model", "--json")
		if code != 0 || !strings.Contains(out, `"qwen3.8-27b":{"tasks":2`) {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})

	t.Run("kind-model without a model groups as <kind>/-", func(t *testing.T) {
		f := newCommandFixture(t)
		f.pair("a", "20260928T120000", "tasker", "done\n", 0, f.accepted("pi", "", "high"))
		out, _, code := f.invoke("stats", "--by", "kind-model", "--json")
		if code != 0 || !strings.Contains(out, `"pi/-":{"tasks":1`) {
			t.Fatalf("code=%d out=%s", code, out)
		}
		out, _, code = f.invoke("stats", "--by", "kind-model")
		if code != 0 || !strings.Contains(out, "pi/-") {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})

	t.Run("invalid by dimension lists kind-model", func(t *testing.T) {
		f := newCommandFixture(t)
		_, stderr, code := f.invoke("stats", "--by", "banana")
		if code != 2 || !strings.Contains(stderr, "stats: --by expects one of role, kind, model, kind-model, agent, effort (got 'banana')") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
	})
}
