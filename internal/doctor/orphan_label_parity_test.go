package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// R429 F1: session membership on the shared snapshot is the explicit
// soho.session label, as the labeled listing selected it. Both terminals
// were released and are still live; only the labeled one is a member, and
// the unlabeled one, whose hc-<address> fallback equals the caller's
// session, is never reported as an orphan.
func TestOrphanMembershipIsTheExplicitLabel(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	state := filepath.Join(root, "state", "hc-t3")
	for _, d := range []string{bin, state, filepath.Join(root, "skill")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(state, "released-panes.tsv"), []byte("released\tt3\tpi\tnow\nlabeled\tt4\tpi\tnow\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, bin, "hc", []fakecli.Rule{
		{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","state":"live","labels":{"soho.session":"hc-t3"}}`},
		{Argv: []string{"ls", "--label", "soho.session=hc-t3", "--json"}, Stdout: `{"terminals":[{"address":"t4","name":"labeled","state":"live","labels":{"soho.session":"hc-t3"},"status":{"state":"idle"}}]}`},
		{Argv: []string{"ls", "--json"}, Stdout: `{"terminals":[{"address":"t3","name":"released","state":"live","status":{"state":"idle"}},{"address":"t4","name":"labeled","state":"live","labels":{"soho.session":"hc-t3"},"status":{"state":"idle"}}]}`},
	}); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{"HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root, "PATH": bin, "SOHO_FAKECLI_CONFIG": bin, "SOHO_DIR": filepath.Join(root, "state"), "SOHO_TRANSPORT": "hc", "SOHO_SKILL_DIR": filepath.Join(root, "skill"), "HC_TERMINAL_ID": "t1", "SOHO_NOWRITE": "1"}
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	old := core.Orphans(ctx, env, root)
	shared := core.OrphansWithServices(ctx, env, root, backend.New(env))
	names := func(list []core.Orphan) string {
		out := []string{}
		for _, o := range list {
			out = append(out, o.Name+"@"+o.Pane)
		}
		return strings.Join(out, ",")
	}
	if names(old) != "labeled@t4" || names(shared) != names(old) {
		t.Fatalf("membership parity: labeled listing=%q shared snapshot=%q", names(old), names(shared))
	}
}
