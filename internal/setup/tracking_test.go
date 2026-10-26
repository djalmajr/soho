package setup

import (
	"crypto/sha256"
	"github.com/djalmajr/soho/internal/setuptext"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReview342TrackedWithoutGit(t *testing.T) {
	root, env, ctx := tm5LocalFixture(t)
	seed := setuptext.SettingsHooksResult(nil)
	writeLocalSetupFixtureFile(t, root, ".claude/settings.json", *seed)
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(git, "-C", root, "add", ".claude/settings.json")
	cmd.Env = env.List()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %s %v", out, err)
	}
	tracked, ok := gitOutput(env, root, "ls-files", "--error-unmatch", "--", ".claude/settings.json")
	if !ok {
		t.Fatal("fixture not tracked")
	}
	t.Logf("git ls-files --error-unmatch: success=%v output=%q", ok, tracked)
	env["PATH"] = t.TempDir()
	_, lookup := gitOutput(env, root, "ls-files", "--error-unmatch", "--", ".claude/settings.json")
	code, out, msg := runSetupWrite([]string{"--local"}, ctx, env, root)
	after, err := os.ReadFile(filepath.Join(root, ".claude/settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("git_unavailable_lookup=%v setup_code=%d shared_before_sha256=%x shared_after_sha256=%x shared_after=%q stdout=%q stderr=%q", lookup, code, sha256.Sum256([]byte(*seed)), sha256.Sum256(after), after, out, msg)
	local, localErr := os.ReadFile(filepath.Join(root, ".claude/settings.local.json"))
	if localErr != nil || !strings.Contains(string(local), "soho hook doctor") || !strings.Contains(msg, "tracking status could not be determined") {
		t.Fatalf("local=%s err=%v warning=%s", local, localErr, msg)
	}
	if code != 0 || string(after) != *seed {
		t.Fatal("tracked shared settings changed when git could not verify ownership")
	}
}

func TestSetupUnknownTrackingAndPreviews(t *testing.T) {
	for _, where := range []string{"missing-git", "non-repo"} {
		for _, mode := range []string{"write", "--plan", "--dry-run"} {
			t.Run(where+"/"+mode, func(t *testing.T) {
				root, env, ctx := tm5LocalFixture(t)
				env["TMPDIR"] = t.TempDir()
				if where == "non-repo" {
					root = t.TempDir()
				} else {
					env["PATH"] = t.TempDir()
				}
				seed := setuptext.SettingsHooksResult(nil)
				writeLocalSetupFixtureFile(t, root, ".claude/settings.json", *seed)
				before := setupFilesystemSnapshot(t, root)
				args := []string{"--local"}
				if mode != "write" {
					args = append(args, mode)
				}
				code, out, msg := runSetupWrite(args, ctx, env, root)
				shared, e := os.ReadFile(filepath.Join(root, ".claude/settings.json"))
				if code != 0 || e != nil || string(shared) != *seed || !strings.Contains(out+msg, "tracking status could not be determined") {
					t.Fatalf("code=%d shared=%s err=%v out=%s warning=%s", code, shared, e, out, msg)
				}
				if mode != "write" && !reflect.DeepEqual(before, setupFilesystemSnapshot(t, root)) {
					t.Fatal("preview changed files")
				}
				if strings.Contains(out, "would remove soho-managed") || strings.Contains(out, "-      \"hooks\"") {
					t.Fatalf("unsafe migration preview: %s", out)
				}
				for _, rel := range []string{claudeLocalHookRel, codexHookRel, piInboundRel} {
					if strings.Contains(out, "ignores: "+"/"+rel) {
						t.Fatalf("unknown exclude preview: %s", out)
					}
				}
				if where == "missing-git" {
					b, e := os.ReadFile(filepath.Join(root, ".git/info/exclude"))
					if e != nil {
						t.Fatal(e)
					}
					for _, rel := range []string{claudeLocalHookRel, codexHookRel, piInboundRel} {
						if strings.Contains(string(b), "/"+rel) {
							t.Fatalf("unknown excluded: %s", b)
						}
					}
				}
				t.Logf("where=%s mode=%s shared_unchanged=true warning=true", where, mode)
			})
		}
	}
}

func TestSetupHookTrackingStates(t *testing.T) {
	root, env, _ := tm5LocalFixture(t)
	rels := []string{claudeLocalHookRel, codexHookRel, piInboundRel}
	for _, rel := range rels {
		if got := trackingStatus(root, rel, env); got != trackingUntracked {
			t.Fatalf("untracked %s=%v", rel, got)
		}
	}
	// A real Git index read error must not authorize exclusions.
	env["GIT_INDEX_FILE"] = root
	for _, rel := range rels {
		if got := trackingStatus(root, rel, env); got != trackingUnknown {
			t.Fatalf("failed-index %s=%v", rel, got)
		}
	}
	if got := untrackedLocalHooks(root, rels, env); len(got) != 0 {
		t.Fatalf("failed query excludes=%v", got)
	}
	delete(env, "GIT_INDEX_FILE")
	env["PATH"] = t.TempDir()
	if got := untrackedLocalHooks(root, rels, env); len(got) != 0 {
		t.Fatalf("missing git excludes=%v", got)
	}
}
