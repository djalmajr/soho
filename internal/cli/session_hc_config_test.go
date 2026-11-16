package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestHcSessionProjectTransportConfig(t *testing.T) {
	f := newMutationGuardHcFixture(t)
	registrantReinstallHc(t, f.bin, fakecli.Rule{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","state":"live"}`})
	if err := os.MkdirAll(filepath.Join(f.repo, ".agents"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.repo, ".agents", "soho.conf"), []byte("transport=hc\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.env["HC_TERMINAL_ID"] = "t1"
	delete(f.env, "SOHO_TRANSPORT")
	t.Chdir(f.repo)
	code, out, stderr := runMutationGuardHc(t, []string{"session", "set", "max_workers", "9"}, f.env.Clone())
	if code != 0 {
		t.Fatalf("set exit=%d out=%q stderr=%q", code, out, stderr)
	}
	path := filepath.Join(f.state, "hc-t1", "session.conf")
	if data, err := os.ReadFile(path); err != nil || !strings.Contains(string(data), "max_workers=9") {
		t.Fatalf("session file %s=%q err=%v", path, data, err)
	}
	code, out, stderr = runMutationGuardHc(t, []string{"config", "get", "max_workers"}, f.env.Clone())
	if code != 0 || out != "9\n" {
		t.Fatalf("config exit=%d out=%q stderr=%q", code, out, stderr)
	}
}
