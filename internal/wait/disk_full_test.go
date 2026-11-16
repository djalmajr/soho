package wait

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

func TestWaitDiskFullWarning(t *testing.T) {
	line := "soho: gate: cannot write /tmp/soho/hc-t1/reports/crit-kas283c2-unit1.result.json: disk quota exceeded"
	f := newQueuedProbeFixture(t, "idle", "5", line+"\n", nil)
	f.env["SOHO_DIR"] = filepath.Dir(f.sd)
	f.env["HERDR_WORKSPACE_ID"] = filepath.Base(f.sd)
	f.ctx = &core.Config{Entries: map[string]core.ConfigEntry{"settled_grace": {Value: "0"}}}
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	code := CmdWait([]string{"worker", "--timeout", "5000"}, f.ctx, f.env, "")
	warning := "soho: warning: worker 'worker' hit a disk error: " + line + "; free space (soho gc removes registered copies) and dispatch again"
	if code != 6 || !strings.Contains(out.String(), `"status":"settled-no-report"`) || strings.Count(stderr.String(), warning) != 1 {
		t.Fatalf("code=%d stdout=%q stderr=%q, want settled and exactly one %q", code, out.String(), stderr.String(), warning)
	}
}
