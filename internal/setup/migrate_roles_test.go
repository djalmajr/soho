package setup

import (
	"errors"
	"fmt"
	"github.com/djalmajr/soho/internal/core"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAudit542SetupKeepsExternalRoleLink(t *testing.T) {
	for _, merge := range []bool{false, true} {
		t.Run(fmt.Sprintf("merge=%v", merge), func(t *testing.T) {
			root, env, ctx := codexSetupFixture(t)
			old := filepath.Join(root, ".agents", "herdr-roles")
			next := filepath.Join(root, ".agents", "soho-roles")
			target := filepath.Join(t.TempDir(), "outside-role.md")
			mustWriteFile(t, target, "outside sentinel\n")
			mustWriteFile(t, filepath.Join(old, "reviewer.md"), "inside role\n")
			link := filepath.Join(old, "outside.md")
			if err := os.Symlink(target, link); err != nil {
				t.Skipf("native symlink unavailable: %v", err)
			}
			if err := os.Mkdir(filepath.Join(old, "nested"), 0700); err != nil {
				t.Fatal(err)
			}
			if merge {
				if err := os.MkdirAll(next, 0700); err != nil {
					t.Fatal(err)
				}
			}
			code, out, msg := runSetupWrite([]string{"--no-hooks"}, ctx, env, root)
			info, err := os.Lstat(link)
			_, nextErr := os.Lstat(filepath.Join(next, "outside.md"))
			if code != 0 || err != nil || info.Mode()&os.ModeSymlink == 0 || !os.IsNotExist(nextErr) || !strings.Contains(out, "kept") {
				t.Fatalf("exit=%d old=%v err=%v new=%v out=%s stderr=%s", code, info, err, nextErr, out, msg)
			}
			if mustReadFile(t, target) != "outside sentinel\n" || mustReadFile(t, filepath.Join(next, "reviewer.md")) != "inside role\n" {
				t.Fatal("role contents changed")
			}
			if _, err := os.Stat(filepath.Join(old, "nested")); err != nil {
				t.Fatal("nonregular entry moved")
			}
			if core.RoleFile("outside", env, root) != "" {
				t.Fatal("external role link activated")
			}
		})
	}
}

func TestAudit542MigrationPreservesConcurrentDestination(t *testing.T) {
	root, env, ctx := codexSetupFixture(t)
	old := filepath.Join(root, ".agents", "herdr-roles", "reviewer.md")
	dst := filepath.Join(root, ".agents", "soho-roles", "reviewer.md")
	mustWriteFile(t, old, "source role\n")
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		t.Fatal(err)
	}
	called := false
	var created os.FileInfo
	saved := linkProjectRole
	linkProjectRole = func(r *os.Root, src, next string) error {
		if src == filepath.Join(".agents", "herdr-roles", "reviewer.md") {
			called = true
			mustWriteFile(t, dst, "concurrent local override\n")
			var err error
			created, err = os.Stat(dst)
			if err != nil {
				t.Fatal(err)
			}
		}
		return saved(r, src, next)
	}
	t.Cleanup(func() { linkProjectRole = saved })
	code, out, msg := runSetupWrite([]string{"--no-hooks"}, ctx, env, root)
	after, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !called || code != 0 || mustReadFile(t, dst) != "concurrent local override\n" || mustReadFile(t, old) != "source role\n" || !os.SameFile(created, after) || !strings.Contains(out, "already exists; merge by hand") {
		t.Fatalf("boundary=%v exit=%d same_inode=%v out=%s stderr=%s", called, code, created != nil && os.SameFile(created, after), out, msg)
	}
	t.Logf("native no-replace boundary=%v concurrent destination inode/content and source retained", called)
}

func TestAudit542MigrationKeepsUnsupportedLink(t *testing.T) {
	root, env, ctx := codexSetupFixture(t)
	src := filepath.Join(root, ".agents", "herdr-roles", "reviewer.md")
	dst := filepath.Join(root, ".agents", "soho-roles", "reviewer.md")
	mustWriteFile(t, src, "keep source\n")
	saved := linkProjectRole
	linkProjectRole = func(*os.Root, string, string) error { return errors.New("hard links unsupported") }
	t.Cleanup(func() { linkProjectRole = saved })
	code, out, msg := runSetupWrite([]string{"--no-hooks"}, ctx, env, root)
	if code != 0 || mustReadFile(t, src) != "keep source\n" || !strings.Contains(out, "atomic no-replace move unavailable") {
		t.Fatalf("exit=%d out=%s stderr=%s", code, out, msg)
	}
	if _, err := os.Lstat(dst); !os.IsNotExist(err) {
		t.Fatalf("unsupported link created destination: %v", err)
	}
}

func TestSetupProjectRolesMigration(t *testing.T) {
	for _, mode := range []string{"normal", "local", "plan"} {
		t.Run(mode, func(t *testing.T) {
			root, env, ctx := codexSetupFixture(t)
			old := filepath.Join(root, ".agents", "herdr-roles")
			next := filepath.Join(root, ".agents", "soho-roles")
			mustWriteFile(t, filepath.Join(old, "reviewer.md"), "review rules\n")
			info, err := os.Stat(filepath.Join(old, "reviewer.md"))
			if err != nil {
				t.Fatal(err)
			}
			// Windows reads the file id through the path on first use; load
			// it now, while the old path still exists.
			os.SameFile(info, info)
			before := treeListing(t, root)
			args := []string{"--no-hooks"}
			if mode == "local" {
				args = append(args, "--local")
			}
			if mode == "plan" {
				args = append(args, "--plan")
			}
			code, out, msg := runSetupWrite(args, ctx, env, root)
			if code != 0 {
				t.Fatalf("mode=%s exit=%d out=%s stderr=%s", mode, code, out, msg)
			}
			verb := "moved"
			if mode == "plan" {
				verb = "would move"
			}
			if !strings.Contains(out, "migrate: "+verb+" "+filepath.Join(old, "reviewer.md")+" to "+filepath.Join(next, "reviewer.md")) {
				t.Fatalf("move not listed: %s", out)
			}
			if mode == "plan" {
				if after := treeListing(t, root); after != before {
					t.Fatalf("plan mutated tree\nbefore=%s\nafter=%s", before, after)
				}
				return
			}
			mustNotExist(t, old)
			if body := mustReadFile(t, filepath.Join(next, "reviewer.md")); body != "review rules\n" {
				t.Fatal(body)
			}
			nextInfo, err := os.Stat(filepath.Join(next, "reviewer.md"))
			if err != nil || !os.SameFile(info, nextInfo) {
				t.Fatalf("role inode was not preserved by the move: %v", err)
			}
		})
	}
}

func TestSetupProjectRolesMigrationConflict(t *testing.T) {
	env, root := migrateEnv(t)
	old := filepath.Join(root, ".agents", "herdr-roles")
	next := filepath.Join(root, ".agents", "soho-roles")
	mustWriteFile(t, filepath.Join(old, "reviewer.md"), "old reviewer")
	mustWriteFile(t, filepath.Join(old, "implementer.md"), "old implementer")
	mustWriteFile(t, filepath.Join(next, "reviewer.md"), "new reviewer")
	ctx := core.LoadConfig(env, root)
	before := treeListing(t, root)
	plan := strings.Join(migrateNames(&ctx, env, root, true), "\n")
	if treeListing(t, root) != before || !strings.Contains(plan, "already exists; merge by hand") || !strings.Contains(plan, "would move "+filepath.Join(old, "implementer.md")) {
		t.Fatalf("plan=%s", plan)
	}
	out := strings.Join(migrateNames(&ctx, env, root, false), "\n")
	if !strings.Contains(out, filepath.Join(old, "reviewer.md")) || !strings.Contains(out, "already exists; merge by hand") {
		t.Fatalf("conflict missing: %s", out)
	}
	if mustReadFile(t, filepath.Join(old, "reviewer.md")) != "old reviewer" || mustReadFile(t, filepath.Join(next, "reviewer.md")) != "new reviewer" || mustReadFile(t, filepath.Join(next, "implementer.md")) != "old implementer" {
		t.Fatal("role data changed")
	}
	mustNotExist(t, filepath.Join(old, "implementer.md"))
	mustExist(t, old)
	after := treeListing(t, root)
	_ = migrateNames(&ctx, env, root, false)
	if treeListing(t, root) != after {
		t.Fatal("retry changed the conflicts")
	}
}

func TestSetupProjectRolesMigrationLinks(t *testing.T) {
	for _, kind := range []string{"old", "agents", "new"} {
		t.Run(kind, func(t *testing.T) {
			env, root := migrateEnv(t)
			outside := t.TempDir()
			mustWriteFile(t, filepath.Join(outside, "sentinel.md"), "outside unchanged")
			old := filepath.Join(root, ".agents", "herdr-roles")
			next := filepath.Join(root, ".agents", "soho-roles")
			link := old
			if kind == "agents" {
				link = filepath.Join(root, ".agents")
			}
			if kind == "new" {
				link = next
				mustWriteFile(t, filepath.Join(old, "inside.md"), "inside unchanged")
			}
			if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, link); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			before := treeListing(t, outside)
			ctx := core.LoadConfig(env, root)
			lines := migrateNames(&ctx, env, root, false)
			if treeListing(t, outside) != before {
				t.Fatalf("escaped project: %v", lines)
			}
			if _, err := os.Lstat(link); err != nil {
				t.Fatalf("link changed: %v", err)
			}
			if kind == "new" && mustReadFile(t, filepath.Join(old, "inside.md")) != "inside unchanged" {
				t.Fatal("moved through destination link")
			}
		})
	}
}
