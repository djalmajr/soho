package doctor

// SOH-458 under hc: doctor names live assistant children of the caller that
// have no roster row and are recorded in the released-panes registry, once
// per terminal. The legacy released-panes orphan check is a separate warning
// and is covered by doctor_orphan_test.go.

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

func TestDoctorNativeOrphanChildren(t *testing.T) {
	root := t.TempDir()
	bin, state := filepath.Join(root, "bin"), filepath.Join(root, "state", "ws")
	for _, dir := range []string{bin, state, filepath.Join(root, "skill")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(state, "agents.tsv"), []byte("rostered\tt6\tclaude\timplementer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Only child-assistant/t2 has a released-panes row. The wake-named and
	// unregistered assistant children are live, assistant-looking children
	// of the caller but release never recorded them, so the suggested
	// `soho release <name> --close` would refuse for them.
	writeDoctorReleasedPanes(t, root, "child-assistant\tt2\tcodex\tnow", "assistant-2\tt11\tcodex\tnow")
	listing := `{"terminals":[
		{"address":"t2","name":"child-assistant","state":"live","parent":"t1","labels":{"soho.session":"ws"},"status":{"state":"idle","app":"claude-code"}},
		{"address":"t2","name":"child-assistant","state":"live","parent":"t1","labels":{"soho.session":"ws"},"status":{"state":"idle","app":"claude-code"}},
		{"address":"t3","name":"child-shell","state":"live","parent":"t1","labels":{"soho.session":"ws"},"foreground":{"name":"zsh"}},
		{"address":"t4","name":"child-wake","state":"live","parent":"t1","labels":{"soho.session":"ws"},"argv":["/bin/sh","-c","soho wait worker --wake"]},
		{"address":"t5","name":"foreign-assistant","state":"live","parent":"t9","labels":{"soho.session":"ws"},"status":{"state":"idle","app":"claude-code"}},
		{"address":"t6","name":"rostered","state":"live","parent":"t1","labels":{"soho.session":"ws"},"status":{"state":"idle","app":"claude-code"}},
		{"address":"t7","name":"finished-child","state":"finished","parent":"t1","labels":{"soho.session":"ws"},"status":{"state":"idle","app":"claude-code"}},
		{"address":"t8","name":"wake-w1","state":"live","parent":"t1","labels":{"soho.session":"ws"},"status":{"state":"idle","app":"claude-code"},"argv":["/bin/sh","-c","soho wait w1 --wake"]},
		{"address":"t9","name":"assistant-1","state":"live","parent":"t1","labels":{"soho.session":"ws"},"status":{"state":"idle","app":"claude-code"}},
		{"address":"t10","name":"assistant-2","state":"live","parent":"t1","labels":{"soho.session":"ws"},"status":{"state":"idle","app":"claude-code"}}
	]}`
	if _, err := fakecli.Install(t, bin, "hc", []fakecli.Rule{
		{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","state":"live","labels":{"soho.session":"ws"}}`},
		{Argv: []string{"ls", "--json"}, Stdout: listing},
	}); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{"HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root, "PATH": bin, "SOHO_FAKECLI_CONFIG": bin, "SOHO_DIR": filepath.Join(root, "state"), "SOHO_TRANSPORT": "hc", "SOHO_SKILL_DIR": filepath.Join(root, "skill"), "HC_TERMINAL_ID": "t1"}
	var out strings.Builder
	s := &Say{Out: &out}
	doctorNativeOrphanChildren(&core.Config{Entries: map[string]core.ConfigEntry{}}, backend.New(env), env, root, s)
	want := "warn   orphan terminal: 'child-assistant' (t2, idle) is not in the roster; close it with: soho release child-assistant --close\n"
	if out.String() != want {
		t.Fatalf("warnings=%q want %q", out.String(), want)
	}
}

func TestDoctorNativeOrphanChildrenUnreadableRegistry(t *testing.T) {
	root := t.TempDir()
	bin, state := filepath.Join(root, "bin"), filepath.Join(root, "state", "ws")
	for _, dir := range []string{bin, state, filepath.Join(root, "skill")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// An unreadable registry registers nothing: no close advice.
	if err := os.Mkdir(filepath.Join(state, "released-panes.tsv"), 0o700); err != nil {
		t.Fatal(err)
	}
	listing := `{"terminals":[{"address":"t2","name":"child-assistant","state":"live","parent":"t1","labels":{"soho.session":"ws"},"status":{"state":"idle","app":"claude-code"}}]}`
	if _, err := fakecli.Install(t, bin, "hc", []fakecli.Rule{
		{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","state":"live","labels":{"soho.session":"ws"}}`},
		{Argv: []string{"ls", "--json"}, Stdout: listing},
	}); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{"HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root, "PATH": bin, "SOHO_FAKECLI_CONFIG": bin, "SOHO_DIR": filepath.Join(root, "state"), "SOHO_TRANSPORT": "hc", "SOHO_SKILL_DIR": filepath.Join(root, "skill"), "HC_TERMINAL_ID": "t1"}
	var out strings.Builder
	s := &Say{Out: &out}
	doctorNativeOrphanChildren(&core.Config{Entries: map[string]core.ConfigEntry{}}, backend.New(env), env, root, s)
	if out.String() != "" {
		t.Fatalf("warnings=%q want none", out.String())
	}
}
