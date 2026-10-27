package doctor

import (
	"github.com/djalmajr/soho/internal/platform"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorRolesOnlyHint(t *testing.T) {
	root := t.TempDir()
	if e := os.MkdirAll(filepath.Join(root, ".agents", "herdr-roles"), 0700); e != nil {
		t.Fatal(e)
	}
	var out strings.Builder
	s := &Say{Out: &out}
	doctorProjectRoles(platform.Env{"HOME": t.TempDir(), "SOHO_SKILL_DIR": t.TempDir()}, root, s)
	want := ".agents/herdr-roles/ is the old project roles directory; run soho setup --roles to move it to .agents/soho-roles/"
	if s.Warn != 1 || !strings.Contains(out.String(), want) {
		t.Fatalf("warnings=%d out=%s", s.Warn, out.String())
	}
	t.Log(out.String())
}
