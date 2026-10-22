package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// All .env files in these repositories are empty synthetic fixtures.
func mutationExcludeFixture(t *testing.T) mutationCopyFixture {
	t.Helper()
	f := newMutationCopyFixture(t)
	for _, rel := range []string{".env", "sub/.env.local", "scripts/x.sh", "scripts/keep.sh", "sub/scripts/x.sh"} {
		path := filepath.Join(f.source, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fixtureGit(t, f, f.source, "add", "--", ".env", "sub/.env.local", "scripts", "sub/scripts")
	for _, rel := range []string{".env.extra", "sub/.env.untracked"} {
		if err := os.WriteFile(filepath.Join(f.source, filepath.FromSlash(rel)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func TestMutationCopyExcludes(t *testing.T) {
	for _, tc := range []struct {
		name, config  string
		flags, absent []string
		excluded      int
	}{
		{name: "recursive-env", flags: []string{"--exclude", "**/.env*"}, absent: []string{".env", "sub/.env.local", ".env.extra", "sub/.env.untracked"}, excluded: 4},
		{name: "root-only-env", flags: []string{"--exclude", ".env*"}, absent: []string{".env", ".env.extra"}, excluded: 2},
		{name: "exact-script", flags: []string{"--exclude", "scripts/x.sh"}, absent: []string{"scripts/x.sh"}, excluded: 1},
		{name: "config-only", config: "**/.env*", absent: []string{".env", "sub/.env.local", ".env.extra", "sub/.env.untracked"}, excluded: 4},
		{name: "config-and-flag", config: "**/.env*", flags: []string{"--exclude", "scripts/x.sh"}, absent: []string{".env", "sub/.env.local", ".env.extra", "sub/.env.untracked", "scripts/x.sh"}, excluded: 5},
		{name: "config-list", config: "**/.env*, scripts/x.sh", absent: []string{".env", "sub/.env.local", ".env.extra", "sub/.env.untracked", "scripts/x.sh"}, excluded: 5},
		{name: "repeated-and-overlapping", flags: []string{"--exclude", "**/.env*", "--exclude", "scripts/x.sh", "--exclude", ".env*"}, absent: []string{".env", "sub/.env.local", ".env.extra", "sub/.env.untracked", "scripts/x.sh"}, excluded: 5},
		{name: "no-excludes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := mutationExcludeFixture(t)
			if tc.config != "" {
				writeConfigFile(t, filepath.Join(f.source, ".agents", "soho.conf"), "mutation_copy_exclude="+tc.config+"\n")
				fixtureGit(t, f, f.source, "add", "--", ".agents/soho.conf")
			}
			dest := filepath.Join(f.root, "copy")
			args := append([]string{"--source", f.source, "--dest", dest}, tc.flags...)
			code, out, errOut := runMutationCopyCmd(t, f, f.source, nil, args...)
			if code != 0 {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
			}
			var result struct {
				Copy, Source string
				Files        int
				Excluded     *int
				Links        []mutationCopyLink
			}
			if err := json.Unmarshal([]byte(out), &result); err != nil {
				t.Fatal(err)
			}
			wantFiles := 10 - tc.excluded
			if tc.config != "" {
				wantFiles++
			}
			if result.Excluded == nil || *result.Excluded != tc.excluded || result.Files != wantFiles || result.Copy != dest || result.Source != f.source || len(result.Links) != 0 {
				t.Fatalf("JSON=%s want files=%d excluded=%d", out, wantFiles, tc.excluded)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(out), &fields); err != nil || len(fields) != 5 {
				t.Fatalf("result fields=%v err=%v", fields, err)
			}
			for _, rel := range []string{".gitignore", "tracked.txt", "sub/deep.txt", ".env", "sub/.env.local", ".env.extra", "sub/.env.untracked", "scripts/x.sh", "scripts/keep.sh", "sub/scripts/x.sh"} {
				absent := false
				for _, p := range tc.absent {
					absent = absent || p == rel
				}
				_, err := os.Lstat(filepath.Join(dest, filepath.FromSlash(rel)))
				if absent && !os.IsNotExist(err) || !absent && err != nil {
					t.Errorf("path=%s absent=%v stat=%v", rel, absent, err)
				}
				if absent && strings.Contains(out+errOut, rel) {
					t.Errorf("excluded path leaked in output: %s", rel)
				}
			}
		})
	}
}

func TestMutationCopyExcludeSourceSubdirectory(t *testing.T) {
	f := mutationExcludeFixture(t)
	source := filepath.Join(f.source, "sub")
	dest := filepath.Join(f.root, "copy")
	code, out, errOut := runMutationCopyCmd(t, f, f.source, nil, "--source", source, "--dest", dest, "--exclude", "**/.env*")
	var got struct{ Files, Excluded int }
	err := json.Unmarshal([]byte(out), &got)
	if code != 0 || err != nil || got.Files != 2 || got.Excluded != 2 {
		t.Fatalf("code=%d JSON=%s stderr=%s err=%v", code, out, errOut, err)
	}
	for _, rel := range []string{".env.local", ".env.untracked"} {
		if _, err := os.Lstat(filepath.Join(dest, rel)); !os.IsNotExist(err) {
			t.Fatalf("excluded subdirectory path present: %s %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "deep.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestMutationCopyExcludeInvalidBeforeDestination(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		flags        []string
	}{
		{name: "empty-flag", flags: []string{"--exclude", ""}},
		{name: "magic-flag", flags: []string{"--exclude", ":(top)**/*"}},
		{name: "empty-config-item", config: "ok,,other"},
		{name: "magic-config", config: ":(top)**/*"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMutationCopyFixture(t)
			if tc.config != "" {
				writeConfigFile(t, filepath.Join(f.source, ".agents", "soho.conf"), "mutation_copy_exclude="+tc.config+"\n")
			}
			dest := filepath.Join(f.root, "must-not-exist")
			args := append([]string{"--source", f.source, "--dest", dest}, tc.flags...)
			code, out, errOut := runMutationCopyCmd(t, f, f.source, nil, args...)
			if code != 2 || out != "" || !strings.Contains(errOut, "usage: mutation-copy") {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
			}
			if _, err := os.Lstat(dest); !os.IsNotExist(err) {
				t.Fatalf("invalid exclusion created destination: %v", err)
			}
			if entries, err := os.ReadDir(f.tmp); err != nil || len(entries) != 0 {
				t.Fatalf("invalid exclusion created temp copy/marker: %v %v", entries, err)
			}
		})
	}
}

func TestMutationCopyExcludeUnreadableFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 000 does not enforce Unix read permissions on Windows")
	}
	f := newMutationCopyFixture(t)
	file := filepath.Join(f.source, "blocked.txt")
	if err := os.WriteFile(file, []byte("synthetic fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, f, f.source, "add", "--", "blocked.txt")
	if err := os.Chmod(file, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(file, 0600) })
	if handle, err := os.Open(file); err == nil {
		_ = handle.Close()
		t.Skip("effective privileges bypass chmod 000; no unreadability proof on this runner")
	} else if !os.IsPermission(err) {
		t.Fatal(err)
	}
	dest := filepath.Join(f.root, "control")
	code, _, errOut := runMutationCopyCmd(t, f, f.source, nil, "--source", f.source, "--dest", dest)
	if code != 4 {
		t.Fatalf("unfiltered control code=%d stderr=%q want read failure 4", code, errOut)
	}
	if _, err := os.Lstat(dest); !os.IsNotExist(err) {
		t.Fatalf("failed control not cleaned: %v", err)
	}
	dest = filepath.Join(f.root, "filtered")
	code, out, errOut := runMutationCopyCmd(t, f, f.source, nil, "--source", f.source, "--dest", dest, "--exclude", "blocked.txt")
	var got struct{ Excluded int }
	err := json.Unmarshal([]byte(out), &got)
	if code != 0 || err != nil || got.Excluded != 1 {
		t.Fatalf("excluded unreadable file: code=%d out=%q stderr=%q err=%v", code, out, errOut, err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "blocked.txt")); !os.IsNotExist(err) {
		t.Fatalf("unreadable file copied: %v", err)
	}
}

func TestMutationCopyExcludeExplicitLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating directory symlinks may require Windows privileges")
	}
	f := newMutationCopyFixture(t)
	external := filepath.Join(f.root, "dependencies")
	if err := os.Mkdir(external, 0700); err != nil {
		t.Fatal(err)
	}
	writeConfigFile(t, filepath.Join(f.source, "deps", "item.txt"), "synthetic\n")
	fixtureGit(t, f, f.source, "add", "--", "deps/item.txt")
	dest := filepath.Join(f.root, "copy")
	code, out, errOut := runMutationCopyCmd(t, f, f.source, nil, "--source", f.source, "--dest", dest, "--exclude", "**", "--link", "deps="+external)
	var got struct {
		Files, Excluded int
		Links           []mutationCopyLink
	}
	err := json.Unmarshal([]byte(out), &got)
	link, linkErr := os.Readlink(filepath.Join(dest, "deps"))
	if code != 0 || err != nil || got.Files != 0 || got.Excluded != 4 || len(got.Links) != 1 || linkErr != nil || link != external {
		t.Fatalf("code=%d JSON=%s stderr=%s link=%s err=%v/%v", code, out, errOut, link, err, linkErr)
	}
}

func TestMutationCopyExcludeConfigSet(t *testing.T) {
	f := newMutationCopyFixture(t)
	f.env["SOHO_NOWRITE"] = "0"
	code, out, errOut := runIn(t, []string{"config", "set", "mutation_copy_exclude=**/.env*", "--project"}, f.env, f.source)
	if code != 0 {
		t.Fatalf("config set code=%d out=%q stderr=%q", code, out, errOut)
	}
	if b, err := os.ReadFile(filepath.Join(f.source, ".agents", "soho.conf")); err != nil || string(b) != "mutation_copy_exclude=**/.env*\n" {
		t.Fatalf("project config=%q err=%v", b, err)
	}
}

func TestMutationCopyExcludeListingCount(t *testing.T) {
	for _, filtered := range []bool{false, true} {
		t.Run(map[bool]string{false: "unfiltered", true: "filtered"}[filtered], func(t *testing.T) {
			f := newMutationCopyFixture(t)
			trace := filepath.Join(f.root, "git.trace")
			f.env["GIT_TRACE"] = trace
			args := []string{"--source", f.source, "--dest", filepath.Join(f.root, "copy")}
			want := 1
			if filtered {
				args = append(args, "--exclude", "tracked.txt")
				want = 2
			}
			code, out, errOut := runMutationCopyCmd(t, f, f.source, nil, args...)
			if code != 0 {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
			}
			b, err := os.ReadFile(trace)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Count(string(b), "built-in: git ls-files "); got != want {
				t.Fatalf("whole command ran %d ls-files calls, want %d; trace=%s", got, want, b)
			}
		})
	}
}

func TestMutationCopyExcludeEnvironmentLayer(t *testing.T) {
	f := newMutationCopyFixture(t)
	writeConfigFile(t, filepath.Join(f.source, ".agents", "soho.conf"), "mutation_copy_exclude=.gitignore\n")
	f.env["SOHO_MUTATION_COPY_EXCLUDE"] = "sub/deep.txt"
	dest := filepath.Join(f.root, "copy")
	code, out, errOut := runMutationCopyCmd(t, f, f.source, nil, "--source", f.source, "--dest", dest, "--exclude", "tracked.txt")
	var got struct{ Files, Excluded int }
	err := json.Unmarshal([]byte(out), &got)
	if code != 0 || err != nil || got.Files != 2 || got.Excluded != 2 {
		t.Fatalf("code=%d JSON=%s stderr=%q err=%v", code, out, errOut, err)
	}
	if _, err := os.Stat(filepath.Join(dest, ".gitignore")); err != nil {
		t.Fatal("environment did not override the project's exclusion: ", err)
	}
}
