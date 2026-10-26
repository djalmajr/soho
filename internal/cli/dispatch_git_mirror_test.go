package cli

import (
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAudit338ScratchMirrorKeepsSingleAttempt(t *testing.T) {
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
	f.env["PATH"] += string(os.PathListSeparator) + waitingGitOnlyDir(t)
	f.env["SOHO_SETTLED_GRACE"], f.env["SOHO_WAIT_POLL_MS"] = "0", "1"
	f.env["SOHO_METRICS"] = "off"
	f.env["SOHO_PRESSURE_REFUSE"], f.env["SOHO_PRESSURE_WARN"] = "off", "off"
	old := headInit(t, f.env, f.root)
	headGit(t, f.env, f.root, "commit", "--allow-empty", "-m", "worker commit")
	dir, e := core.PrivateScratchReports(f.env, "ws", core.ScratchCreate)
	if e != nil {
		t.Fatal(e)
	}
	stem := filepath.Join(dir, "worker-20261026T000000")
	side := stem + ".dispatch.json"
	if e = core.WriteScratchFile(side, jsonjs.Stringify(jsonjs.O("git_head", old, "git_branch", "main", "git_tree", f.root))); e != nil {
		t.Fatal(e)
	}
	for path, body := range map[string]string{stem + ".md": "Completed scratch task.\n", stem + ".brief.md": "Do the task.\n"} {
		if e = core.WriteScratchFile(path, body); e != nil {
			t.Fatal(e)
		}
	}
	reopenDispatchWrite(t, filepath.Join(f.state, "ws", "last-report-worker"), stem+".md\n")
	firstCode, firstOut, firstErr := reopenDispatchRun(t, f.env, "wait", "worker", "--timeout", "1s")
	mirror := filepath.Join(f.state, "ws", "briefs", filepath.Base(side))
	_, firstMarker := os.Stat(side + ".git-checked")
	_, mirrorMarkerBefore := os.Stat(mirror + ".git-checked")
	mirrored := headSidecar(t, mirror)
	secondCode, secondOut, secondErr := reopenDispatchRun(t, f.env, "wait", "worker", "--timeout", "1s")
	_, mirrorMarkerAfter := os.Stat(mirror + ".git-checked")
	t.Logf("first exit=%d stdout=%s stderr=%s", firstCode, firstOut, firstErr)
	t.Logf("scratch_marker_error=%v mirrored_git_checked=%v mirror_marker_before=%v after=%v", firstMarker, mirrored["git_checked"], mirrorMarkerBefore, mirrorMarkerAfter)
	t.Logf("second exit=%d stdout=%s stderr=%s", secondCode, secondOut, secondErr)
	if firstCode != 0 || !strings.Contains(firstErr, "left a local commit") {
		t.Fatal("scratch wait did not reach the warning producer")
	}
	if secondCode != 0 || strings.Contains(secondErr, "left a local commit") || strings.Contains(secondOut, "head_moved") {
		t.Fatal("mirroring an already checked scratch sidecar allowed the same task to warn twice")
	}
}
