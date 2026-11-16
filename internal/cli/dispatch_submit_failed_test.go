package cli

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestDispatchSubmitFailureRestoredAndAccepted(t *testing.T) {
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0", fakecli.Rule{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Call: 1, Code: 7, Stderr: "refused"})
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
	sd := filepath.Join(f.state, "ws")
	report := filepath.Join(sd, "reports", "previous report.md")
	if err := os.WriteFile(report, []byte("previous report\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := taskreport.WriteTaskReportPointer(sd, "worker", jsonjs.O("version", 1, "task_report", report, "current", report, "history", []any{})); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sd, "last-report-worker"), []byte(report+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
	if code != 4 {
		t.Fatalf("failed send: code=%d out=%s stderr=%s", code, out, stderr)
	}
	var waitOut, waitErr bytes.Buffer
	oldOut, oldErr := platform.Stdout, platform.Stderr
	platform.Stdout, platform.Stderr = &waitOut, &waitErr
	waitCode := Run([]string{"wait", "worker", "--timeout", "5s"}, f.env)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	if waitCode != 15 || !strings.Contains(waitOut.String(), `"status":"not-received"`) || !strings.Contains(waitOut.String(), `"cause":"submission-failed"`) {
		t.Fatalf("wait after failed send code=%d out=%s stderr=%s", waitCode, waitOut.String(), waitErr.String())
	}
	marker := filepath.Join(sd, "wait", "worker.submit-failed")
	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	brief, err := os.ReadFile(f.brief)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(brief)
	if !strings.HasSuffix(string(raw), fmt.Sprintf(" %x %s\n", sum, report)) {
		t.Fatalf("marker=%q", raw)
	}
	pointer := taskreport.ReadTaskReportPointer(sd, "worker")
	v, _ := pointer.Get("task_report")
	if v != report {
		t.Fatalf("restored task_report=%v", v)
	}
	bin := t.TempDir()
	if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{
		{Argv: []string{"agent", "list"}, ArgvPrefix: true, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "get", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":2}}}`},
		{Argv: []string{"agent", "read", "worker"}, ArgvPrefix: true},
		{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"submitted":true}}`},
	}); err != nil {
		t.Fatal(err)
	}
	f.env["PATH"], f.env["SOHO_FAKECLI_CONFIG"] = bin, bin
	code, out, stderr = f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 {
		t.Fatalf("accepted send: code=%d out=%s stderr=%s", code, out, stderr)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("accepted send retained marker: %v", err)
	}
}

func TestDispatchAcceptedCursorClearsMarkerAfterSidecar(t *testing.T) {
	f := newCursorDispatchFixture(t, "cursor", cursorTUIScreen, "0", false)
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
	marker := filepath.Join(f.state, "ws", "wait", "worker.submit-failed")
	original := writeDispatchSidecar
	writeDispatchSidecar = func(path, kind, model, effort, submission, arrival, session string) error {
		if submission == "accepted" {
			if err := os.WriteFile(marker, []byte("prior marker\n"), 0600); err != nil {
				return err
			}
		}
		return original(path, kind, model, effort, submission, arrival, session)
	}
	t.Cleanup(func() { writeDispatchSidecar = original })
	code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("cursor accepted marker remains: %v", err)
	}
}
