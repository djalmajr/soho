package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

func TestFallbackConfigKeys(t *testing.T) {
	for _, key := range []string{"fallback_edit", "fallback_review"} {
		if !ConfigKeyOk(key) {
			t.Fatalf("ConfigKeyOk(%q) = false", key)
		}
		for _, tc := range []struct {
			value string
			ok    bool
		}{
			{"", true},
			{"codex gpt-5 high", true},
			{"claude sonnet max", true},
			{"pi gpt-5.1-codex", false},
			{"nope gpt-5 high", false},
			{"codex gpt-5 enormous", false},
			{"codex gpt-5", false},
			{"codex  gpt-5 high", true},
		} {
			if got := ConfigValueOk(key, tc.value, platform.Env{}, ""); got != tc.ok {
				t.Errorf("ConfigValueOk(%q, %q) = %v, want %v", key, tc.value, got, tc.ok)
			}
		}
	}
}

func TestFallbackConfigSetWritesAndRefuses(t *testing.T) {
	root := t.TempDir()
	env := platform.Env{"SOHO_DIR": filepath.Join(root, ".soho")}
	project := filepath.Join(root, ".agents", "soho.conf")
	if err := os.MkdirAll(filepath.Dir(project), 0o700); err != nil {
		t.Fatal(err)
	}
	CmdConfigSet([]string{"fallback_edit", "codex gpt-5 high"}, nil, env, root)
	raw, err := os.ReadFile(project)
	if err != nil || !strings.Contains(string(raw), "fallback_edit=codex gpt-5 high") {
		t.Fatalf("config=%q err=%v", raw, err)
	}
	func() {
		defer func() {
			value := recover()
			exit, ok := value.(*platform.ExitError)
			if !ok || exit.Code != 2 || !strings.Contains(exit.Msg, "invalid value 'codex gpt-5' for fallback_edit") {
				t.Fatalf("invalid fallback refusal=%#v", value)
			}
		}()
		CmdConfigSet([]string{"fallback_edit", "codex gpt-5"}, nil, env, root)
		t.Fatal("an invalid fallback value was accepted")
	}()
	raw2, err := os.ReadFile(project)
	if err != nil || string(raw2) != string(raw) {
		t.Fatalf("a refused config set changed the file: %q -> %q (err %v)", raw, raw2, err)
	}
}
