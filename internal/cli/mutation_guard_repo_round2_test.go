package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func review548Linked(t *testing.T) (mutationCopyFixture, string) {
	t.Helper()
	f := newMutationRepoFixture(t)
	delete(f.env, "SOHO_REGISTRY_DIR")
	source := filepath.Join(f.source, ".worktrees", "a")
	fixtureGit(t, f, f.source, "worktree", "add", "--detach", source)
	return f, source
}

func TestReview548CaseAliasGuardAndCopy(t *testing.T) {
	f, source := review548Linked(t)
	actual := filepath.Join(f.source, ".worktrees", "b")
	if e := os.MkdirAll(actual, 0700); e != nil {
		t.Fatal(e)
	}
	aliasTop := filepath.Join(filepath.Dir(f.source), strings.ToUpper(filepath.Base(f.source)))
	alias := filepath.Join(aliasTop, ".worktrees", "b")
	a, e := os.Stat(actual)
	if e != nil {
		t.Fatal(e)
	}
	b, e := os.Stat(alias)
	if e != nil || !os.SameFile(a, b) {
		t.Skipf("native volume is case-sensitive: alias %s error=%v", alias, e)
	}
	canonical, e := filepath.EvalSymlinks(alias)
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("same native directory=true; actual=%q alias=%q EvalSymlinks=%q repositoryTop=%q", actual, alias, canonical, mutationRepositoryTop(source, f.env))
	g := guardFixture{root: f.root, source: source, copy: alias, env: f.env}
	code, out, stderr := runMutationGuardFixture(t, g, []string{alias, "--source", source}, nil)
	t.Logf("guard exit=%d stdout=%s stderr=%s", code, out, stderr)
	if code != 1 || !strings.Contains(out, "fail copy-outside-repository") {
		t.Errorf("case alias bypasses new repository guard: exit=%d; want repository refusal", code)
	}
	dest := filepath.Join(aliasTop, ".worktrees", "new-copy")
	code, out, stderr = runMutationCopyCmd(t, f, source, nil, "--source", source, "--dest", dest)
	content, readErr := os.ReadFile(filepath.Join(f.source, ".worktrees", "new-copy", "tracked.txt"))
	t.Logf("copy exit=%d stdout=%s stderr=%s actual in-repository file=%q err=%v", code, out, stderr, content, readErr)
	if code != 1 || !strings.Contains(stderr, "fail copy-outside-repository") {
		t.Errorf("mutation-copy accepted differently-cased in-repository destination: exit=%d", code)
	}
	if !os.IsNotExist(readErr) {
		t.Errorf("refused alias received a file: content=%q error=%v", content, readErr)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("invocation-created refused destination remains: %v", err)
	}
}

func TestReview548ExistingEmptyDestinationLeavesNoPartialFiles(t *testing.T) {
	f, source := review548Linked(t)
	dest := filepath.Join(f.source, ".worktrees", "existing-empty-copy")
	if e := os.Mkdir(dest, 0700); e != nil {
		t.Fatal(e)
	}
	code, out, stderr := runMutationCopyCmd(t, f, source, nil, "--source", source, "--dest", dest)
	entries, e := os.ReadDir(dest)
	t.Logf("copy exit=%d stdout=%s stderr=%s remaining=%v readErr=%v", code, out, stderr, entries, e)
	if code != 1 || !strings.Contains(stderr, "fail copy-outside-repository") {
		t.Fatalf("fixture did not reach new refusal")
	}
	if e != nil {
		t.Fatal(e)
	}
	if len(entries) > 0 {
		t.Fatalf("repository refusal left %d newly copied files inside the source repository", len(entries))
	}
}

func TestMutationRepositoryIdentityMissingLeaf(t *testing.T) {
	f, source := review548Linked(t)
	alias := filepath.Join(filepath.Dir(f.source), strings.ToUpper(filepath.Base(f.source)))
	info, err := os.Stat(alias)
	actual, statErr := os.Stat(f.source)
	if err != nil || statErr != nil || !os.SameFile(actual, info) {
		t.Skipf("case-sensitive volume: alias=%q error=%v", alias, err)
	}
	missing := filepath.Join(alias, "not-created", "copy")
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("fixture leaf exists: %v", err)
	}
	if detail := mutationRepositoryRefusal(missing, source, f.env); detail == "" {
		t.Fatal("nearest existing alias ancestor was not rejected")
	}
	if detail := mutationRepositoryRefusal(filepath.Join(f.tmp, "outside", "copy"), source, f.env); detail != "" {
		t.Fatalf("outside missing leaf rejected: %s", detail)
	}
	t.Log("missing alias leaf refused via existing repository ancestor; outside missing leaf allowed")
}

func TestMutationGuardSourceIdentityAliases(t *testing.T) {
	for _, name := range []string{"copy-inside-source", "source-inside-copy", "equal"} {
		t.Run(name, func(t *testing.T) {
			f := newMutationRepoFixture(t)
			alias := filepath.Join(filepath.Dir(f.source), strings.ToUpper(filepath.Base(f.source)))
			a, err := os.Stat(alias)
			b, otherErr := os.Stat(f.source)
			if err != nil || otherErr != nil || !os.SameFile(a, b) {
				t.Skipf("case-sensitive volume: alias=%q error=%v", alias, err)
			}
			source, copyPath := f.source, alias
			if name == "copy-inside-source" {
				copyPath = filepath.Join(alias, "copy")
				if err := os.Mkdir(copyPath, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if name == "source-inside-copy" {
				source = filepath.Join(f.source, "source")
				if err := os.Mkdir(source, 0700); err != nil {
					t.Fatal(err)
				}
			}
			g := guardFixture{root: f.root, source: source, copy: copyPath, env: f.env}
			code, out, stderr := runMutationGuardFixture(t, g, []string{copyPath, "--source", source}, nil)
			if code != 1 || !strings.Contains(out, "fail copy-outside-source: copy and source directories overlap") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
			}
			t.Logf("exit=%d stdout=%s", code, out)
		})
	}
}
