package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// Port of the review's public-command probe, with an explicit stdout baseline.
func TestReview540DoctorNoticesStayOnStderr(t *testing.T) {
	f, _ := resumeFixture(t, "idle")
	resumeSeed(t, f, "worker", "20261003T100000", false)
	rc, out, msg := resumeRun(t, f, "doctor")
	needle := "'worker': its task was dispatched before the machine restarted"
	platform.BootTime = func() (time.Time, error) { return time.Time{}, errors.New("unknown") }
	other, baseline, baseErr := resumeRun(t, f, "doctor")
	if rc != 0 || other != 0 || strings.Contains(out, needle) || !strings.Contains(msg, needle) || strings.Contains(baseErr, needle) || baseline != out {
		t.Fatalf("doctor rc=%d/%d stdout unchanged=%t notice stdout=%t stderr=%t baselineErr=%s", rc, other, baseline == out, strings.Contains(out, needle), strings.Contains(msg, needle), baseErr)
	}
	t.Log("doctor exit=0; stdout identical to unknown-boot baseline; restart notice only on stderr")
}

func TestReview540ClockSkewWithoutOpenTask(t *testing.T) {
	f, _ := resumeFixture(t, "idle")
	old := platform.Now
	platform.Now = func() time.Time { return time.Date(2026, 10, 3, 9, 0, 0, 0, time.Local) }
	t.Cleanup(func() { platform.Now = old })
	for _, arg := range []string{"worker", "--all"} {
		rc, out, msg := resumeRun(t, f, "resume", arg)
		if rc != 4 || !strings.Contains(msg, "the boot time is inconsistent with the current clock; nothing was sent") || len(resumePrompts(t, f)) != 0 {
			t.Fatalf("unknown clock without task: rc=%d out=%s err=%s", rc, out, msg)
		}
	}
}

func TestReview540ClockSkewSuppressesProcNotices(t *testing.T) {
	f, boot := resumeFixture(t, "idle")
	if _, err := fakecli.Install(t, f.bin, "ps", []fakecli.Rule{{AnyArgs: true, Code: 1}}); err != nil {
		t.Fatal(err)
	}
	started := boot.Add(-time.Hour).Format(time.ANSIC)
	if runtime.GOOS == "windows" {
		started = strconv.FormatInt(boot.Add(-time.Hour).Unix()*10000000+116444736000000000, 10)
	}
	row := core.ProcRow{Pid: 2147483647, Started: started, Name: "fixture-worker", Owner: "worker", Pane: "-", Created: boot.Add(-time.Hour).Format(time.RFC3339)}
	if err := core.WriteProcs(f.stateDir, []core.ProcRow{row}); err != nil {
		t.Fatal(err)
	}
	old := platform.Now
	t.Cleanup(func() { platform.Now = old })
	for _, consistent := range []bool{false, true} {
		now := boot.Add(-3 * time.Hour)
		if consistent {
			now = boot.Add(time.Hour)
		}
		platform.Now = func() time.Time { return now }
		for _, command := range []string{"status", "doctor"} {
			_, out, msg := resumeRun(t, f, command)
			needle := "ended with the restart"
			if strings.Contains(out, needle) || strings.Contains(msg, needle) != consistent {
				t.Fatalf("%s consistent=%t out=%s err=%s", command, consistent, out, msg)
			}
		}
	}
}

func TestReview540ClockSkewDoesNotResumeFutureDispatch(t *testing.T) {
	for _, tc := range []struct {
		name      string
		now, boot int
	}{{"future-boot-and-dispatch", 9, 12}, {"future-boot", 11, 12}, {"future-dispatch", 9, 8}} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := resumeFixture(t, "idle")
			resumeSeed(t, f, "worker", "20261003T100000", false)
			old := platform.Now
			platform.Now = func() time.Time { return time.Date(2026, 10, 3, tc.now, 0, 0, 0, time.Local) }
			t.Cleanup(func() { platform.Now = old })
			platform.BootTime = func() (time.Time, error) { return time.Date(2026, 10, 3, tc.boot, 0, 0, 0, time.Local), nil }
			_, _, statusErr := resumeRun(t, f, "status", "worker")
			rc, out, msg := resumeRun(t, f, "resume", "worker")
			if strings.Contains(statusErr, "run soho resume") || rc != 4 || len(resumePrompts(t, f)) != 0 || !strings.Contains(msg, "the boot time is inconsistent with the current clock; nothing was sent") {
				t.Fatalf("clock case=%s rc=%d out=%s status=%s stderr=%s prompts=%d", tc.name, rc, out, statusErr, msg, len(resumePrompts(t, f)))
			}
			t.Log("clock inconsistency: no status notice; resume exit=4; zero prompts")
		})
	}
}

func TestReview540ResumeSendsOnlyOnePromptWithArrivalCheck(t *testing.T) {
	f, _ := resumeFixture(t, "idle")
	resumeSeed(t, f, "worker", "20261003T100000", false)
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "1"
	f.bin = filepath.Join(f.root, "arrival-bin")
	if err := os.Mkdir(f.bin, 0700); err != nil {
		t.Fatal(err)
	}
	f.env["PATH"], f.env["SOHO_FAKECLI_CONFIG"] = f.bin, f.bin
	f.install(t, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: busyStateJSON("idle", 1)}, {Argv: []string{"agent", "list"}, ArgvPrefix: true, Stdout: `{"result":{"agents":[]}}`}, {Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true, Stdout: "› \n\n"}, {Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped"}, ArgvPrefix: true}, {Argv: []string{"agent", "prompt"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`}, {AnyArgs: true, Stdout: `{"result":{}}`}})
	rc, out, msg := resumeRun(t, f, "resume", "worker")
	prompts := resumePrompts(t, f)
	var rows []resumeResult
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 1 || rc != 15 || len(rows) != 1 || rows[0].Agent != "worker" || rows[0].Status != "not-received" {
		t.Fatalf("arrival check: rc=%d prompts=%d rows=%+v stderr=%s", rc, len(prompts), rows, msg)
	}
	t.Log("arrival enabled and unconfirmed: one prompt; not-received JSON; exit=15")
}
