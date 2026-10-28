package gittree_test

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/cli"
	"github.com/djalmajr/soho/internal/gittree"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

type diffFixture struct {
	tree, state, report, sidecar, head string
	env                                platform.Env
}

func diffWrite(t *testing.T, file, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

// Construct a real SHA-1 repository and v2 index directly in the fixture.
// This keeps all Git commands, including fixture commands, strictly read-only.
func diffRepository(t *testing.T, tree string, files map[string]string) string {
	t.Helper()
	object := func(kind string, body []byte) [20]byte {
		raw := append([]byte(fmt.Sprintf("%s %d\x00", kind, len(body))), body...)
		id := sha1.Sum(raw)
		name := hex.EncodeToString(id[:])
		var buf bytes.Buffer
		z := zlib.NewWriter(&buf)
		if _, err := z.Write(raw); err != nil {
			t.Fatal(err)
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(tree, ".git", "objects", name[:2], name[2:])
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, buf.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		return id
	}
	ids := map[string][20]byte{}
	names := []string{}
	for name, body := range files {
		diffWrite(t, filepath.Join(tree, filepath.FromSlash(name)), body)
		ids[name] = object("blob", []byte(body))
		names = append(names, name)
	}
	sort.Strings(names)
	var treeObject func(string) [20]byte
	treeObject = func(dir string) [20]byte {
		children := map[string]bool{}
		for _, name := range names {
			if strings.HasPrefix(name, dir) {
				rest := strings.TrimPrefix(name, dir)
				first, _, nested := strings.Cut(rest, "/")
				children[first] = nested
			}
		}
		ordered := []string{}
		for name, nested := range children {
			if nested {
				name += "/"
			}
			ordered = append(ordered, name)
		}
		sort.Strings(ordered)
		var body bytes.Buffer
		for _, entry := range ordered {
			name := strings.TrimSuffix(entry, "/")
			id, mode := ids[dir+name], "100644"
			if strings.HasSuffix(entry, "/") {
				id = treeObject(dir + entry)
				mode = "40000"
			}
			fmt.Fprintf(&body, "%s %s\x00", mode, name)
			body.Write(id[:])
		}
		return object("tree", body.Bytes())
	}
	treeID := treeObject("")
	commit := object("commit", []byte(fmt.Sprintf("tree %x\nauthor Fixture <fixture@example.invalid> 1700000000 +0000\ncommitter Fixture <fixture@example.invalid> 1700000000 +0000\n\ninitial\n", treeID)))
	head := fmt.Sprintf("%x", commit)
	diffWrite(t, filepath.Join(tree, ".git", "HEAD"), "ref: refs/heads/main\n")
	diffWrite(t, filepath.Join(tree, ".git", "refs", "heads", "main"), head+"\n")
	diffWrite(t, filepath.Join(tree, ".git", "config"), "[core]\nrepositoryformatversion = 0\nbare = false\nfilemode = false\nautocrlf = false\n")
	var index bytes.Buffer
	index.WriteString("DIRC")
	binary.Write(&index, binary.BigEndian, uint32(2))
	binary.Write(&index, binary.BigEndian, uint32(len(names)))
	for _, name := range names {
		start := index.Len()
		for _, field := range []uint32{0, 0, 0, 0, 0, 0, 0100644, 0, 0, uint32(len(files[name]))} {
			binary.Write(&index, binary.BigEndian, field)
		}
		id := ids[name]
		index.Write(id[:])
		binary.Write(&index, binary.BigEndian, uint16(len(name)))
		index.WriteString(name)
		index.WriteByte(0)
		for (index.Len()-start)%8 != 0 {
			index.WriteByte(0)
		}
	}
	checksum := sha1.Sum(index.Bytes())
	index.Write(checksum[:])
	if err := os.WriteFile(filepath.Join(tree, ".git", "index"), index.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return head
}

func newDiffFixture(t *testing.T, owned, report, role string) *diffFixture {
	t.Helper()
	root := t.TempDir()
	tree := filepath.Join(root, "tree with spaces")
	state := filepath.Join(root, "state", "ws")
	head := diffRepository(t, tree, map[string]string{"src/a.go": "package a\n", "src/b.go": "package b\n", "forbidden.go": "package forbidden\n", "README.md": "fixture\n"})
	bin := filepath.Join(root, "bin")
	if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
		{Argv: []string{"agent", "read", "worker"}, ArgvPrefix: true},
	}); err != nil {
		t.Fatal(err)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("real git unavailable")
	}
	target := filepath.Join(bin, filepath.Base(git))
	if err := os.Symlink(git, target); err != nil {
		b, e := os.ReadFile(git)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(target, b, 0755); e != nil {
			t.Fatal(e)
		}
	}
	roles := filepath.Join(root, "roles")
	diffWrite(t, filepath.Join(roles, "implementer.md"), "---\nmode: edit\n---\nRole\n")
	if role == "custom-edit" {
		diffWrite(t, filepath.Join(roles, role+".md"), "---\nmode: edit\n---\nCustom\n")
	}
	reportPath := filepath.Join(state, "reports", "worker-fixture.md")
	side := filepath.Join(state, "briefs", "worker-fixture.dispatch.json")
	diffWrite(t, reportPath, report)
	diffWrite(t, filepath.Join(state, "briefs", "worker-fixture.md"), "# Role\n# Owned files\n- ignored-role.go\n# Brief\n"+owned+"\n# Report contract\nComplete.\n")
	// Fix the dispatch cutoff independently of filesystem timestamp granularity.
	cutoff := time.Unix(1700000100, 0)
	if err := os.Chtimes(filepath.Join(state, "briefs", "worker-fixture.md"), cutoff, cutoff); err != nil {
		t.Fatal(err)
	}
	diffWrite(t, filepath.Join(state, "last-report-worker"), reportPath+"\n")
	// The recorded role, rather than this intentionally mismatched roster, wins.
	diffWrite(t, filepath.Join(state, "agents.tsv"), "# header\nworker\tp1\tcodex\treviewer\topenai\t0\t"+tree+"\tnow\n")
	if role == "reviewer" {
		diffWrite(t, filepath.Join(state, "agents.tsv"), "# header\nworker\tp1\tcodex\timplementer\topenai\t0\t"+tree+"\tnow\n")
	}
	diffWrite(t, side, fmt.Sprintf(`{"git_tree":%q,"git_head":%q,"git_branch":"main","role":%q}`, tree, head, role))
	env := platform.Env{"PATH": bin, "SOHO_FAKECLI_CONFIG": bin, "HERDR_ENV": "1", "HERDR_WORKSPACE_ID": "ws", "SOHO_DIR": filepath.Dir(state), "SOHO_SKILL_DIR": filepath.Join(root, "skill"), "SOHO_ROLES": roles, "SOHO_SETTLED_GRACE": "0", "SOHO_WAIT_POLL_MS": "1", "SOHO_METRICS": "off", "HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root, "GIT_CONFIG_NOSYSTEM": "1", "GIT_DIR": "bad-dir", "GIT_WORK_TREE": "bad-tree", "GIT_INDEX_FILE": "bad-index"}
	env["GORACE"] = fakecli.RaceOptions(env.List())
	// Audit assertions must not depend on the host's available disk or swap.
	env["SOHO_PRESSURE_DISK_FREE_PERCENT"] = "0"
	env["SOHO_PRESSURE_SWAP_PERCENT"] = "0"
	if err := os.MkdirAll(filepath.Join(state, "wait"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(tree)
	return &diffFixture{tree, state, reportPath, side, head, env}
}

func (f *diffFixture) wait(t *testing.T) (map[string]any, string, string) {
	t.Helper()
	last, err := os.ReadFile(filepath.Join(f.state, "last-report-worker"))
	if err != nil {
		t.Fatal(err)
	}
	if err := diffReportAfterEdits(f, strings.TrimSpace(string(last))); err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
	code := cli.Run([]string{"wait", "worker", "--timeout", "2s"}, f.env)
	if code != 0 {
		t.Fatalf("wait code=%d out=%s stderr=%s", code, out.String(), stderr.String())
	}
	var value map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &value); err != nil {
		t.Fatalf("out=%s err=%v", out.String(), err)
	}
	if value["status"] != "done" {
		t.Fatalf("wait changed status=%v", value)
	}
	return value, out.String(), stderr.String()
}

func TestDiffReportSettle(t *testing.T) {
	for _, name := range []string{"outside", "cited", "consistent", "glob", "directory", "readonly", "legacy", "no-owned", "fences-forbidden", "untracked", "deleted", "custom-edit", "invalid-head"} {
		t.Run(name, func(t *testing.T) {
			owned := "# Owned files\n- `src/b.go`\n"
			body := "Completed `src/b.go`.\n"
			role := "implementer"
			switch name {
			case "outside":
				owned = "# Owned files\n- src/a.go\n"
			case "cited":
				body = "Completed `src/a.go` and `src/b.go`.\n"
			case "glob":
				owned = "# Owned files\n- `src/*.go`\n"
			case "directory":
				owned = "# Owned files\n- src/\n"
			case "readonly":
				role = "reviewer"
				body = "findings: 0 (P0 0, P1 0, P2 0, P3 0) | verdict: pass\nCompleted `src/a.go`.\n"
			case "no-owned":
				owned = "# Goal\nDo it.\n"
			case "fences-forbidden":
				body = "Completed `src/b.go`.\n```go\n`src/a.go`\n```\n~~~~text\n`README.md`\n~~~\n~~~~\n# Forbidden\n`forbidden.go`\n## Nested\n`src/a.go`\n# Validation\npassed\n"
			case "untracked":
				owned = "# Owned files\n- src/\n"
			case "custom-edit":
				role = "custom-edit"
			}
			f := newDiffFixture(t, owned, body, role)
			// Snapshot .git and prove the audit's read-only commands leave it intact.
			before := diffGitFiles(t, filepath.Join(f.tree, ".git"))
			diffWrite(t, filepath.Join(f.tree, "src", "b.go"), "package b\n// changed\n")
			if name == "deleted" {
				if err := os.Remove(filepath.Join(f.tree, "src", "b.go")); err != nil {
					t.Fatal(err)
				}
			}
			if name == "untracked" {
				diffWrite(t, filepath.Join(f.tree, "new file.txt"), "new\n")
			}
			if name == "legacy" {
				diffWrite(t, f.sidecar, `{"role":"implementer"}`)
			}
			if name == "invalid-head" {
				diffWrite(t, f.sidecar, fmt.Sprintf(`{"role":"implementer","git_checked":true,"git_tree":%q,"git_head":"--output=do-not-write"}`, f.tree))
			}
			value, _, stderr := f.wait(t)
			_, hasStat := value["diff_stat"]
			if name == "deleted" {
				// A missing file has no mtime proving it changed after dispatch.
				if hasStat || value["outside_owned"] != nil || value["cited_unchanged"] != nil || stderr != "" {
					t.Fatalf("missing-file audit=%v stderr=%s", value, stderr)
				}
			} else if name == "readonly" || name == "legacy" || name == "invalid-head" {
				if hasStat || value["outside_owned"] != nil || value["cited_unchanged"] != nil || stderr != "" {
					t.Fatalf("unexpected audit: %v stderr=%s", value, stderr)
				}
				if _, err := os.Lstat(f.sidecar + ".diff-checked"); !os.IsNotExist(err) {
					t.Fatalf("ineligible marker: %v", err)
				}
			} else {
				artifact := strings.TrimSuffix(f.report, ".md") + ".diff-stat.txt"
				if value["diff_stat"] != artifact {
					t.Fatalf("stat path=%v want=%s", value, artifact)
				}
				stat, err := os.ReadFile(artifact)
				if err != nil || !strings.Contains(string(stat), "src/b.go") {
					t.Fatalf("stat=%q err=%v", stat, err)
				}
				outside := []any(nil)
				if name == "outside" {
					outside = []any{"src/b.go"}
				}
				if name == "untracked" {
					outside = []any{"new file.txt"}
					if !strings.Contains(string(stat), "new file.txt") {
						t.Fatalf("new file missing from stat: %q", stat)
					}
				}
				if !reflect.DeepEqual(value["outside_owned"], any(outside)) && !(outside == nil && value["outside_owned"] == nil) {
					t.Fatalf("outside=%v want=%v", value["outside_owned"], outside)
				}
				if name == "cited" {
					if !reflect.DeepEqual(value["cited_unchanged"], []any{"src/a.go"}) || !strings.Contains(stderr, "report of 'worker' cites files the tree shows unchanged: src/a.go") {
						t.Fatalf("value=%v stderr=%s", value, stderr)
					}
				} else if value["cited_unchanged"] != nil {
					t.Fatalf("unexpected cited=%v", value)
				}
				if outside != nil {
					if !strings.Contains(stderr, "'worker' changed files outside its Owned files: "+outside[0].(string)) {
						t.Fatal(stderr)
					}
				} else if name != "cited" && stderr != "" {
					t.Fatalf("consistent warning=%s", stderr)
				}
				if name == "consistent" && len(value) != 4 {
					t.Fatalf("unexpected JSON keys: %v", value)
				}
				second, _, again := f.wait(t)
				if second["diff_stat"] != nil || second["outside_owned"] != nil || second["cited_unchanged"] != nil || again != "" {
					t.Fatalf("repeat=%v stderr=%s", second, again)
				}
			}
			if after := diffGitFiles(t, filepath.Join(f.tree, ".git")); !reflect.DeepEqual(before, after) {
				t.Fatalf("Git metadata changed before=%v after=%v", before, after)
			}
			t.Logf("case=%s exit=0 status=done diff_stat=%v outside_owned=%v cited_unchanged=%v stderr=%q", name, hasStat, value["outside_owned"], value["cited_unchanged"], stderr)
		})
	}
}

func diffGitFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, e := os.ReadFile(p)
		if e != nil {
			return e
		}
		rel, e := filepath.Rel(root, p)
		if e != nil {
			return e
		}
		files[rel] = fmt.Sprintf("%x", sha1.Sum(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestDiffReportMarkerSurvivesSidecarRewrite(t *testing.T) {
	f := newDiffFixture(t, "# Owned files\n- src/*.go\n", "Completed.\n", "implementer")
	diffWrite(t, filepath.Join(f.tree, "src", "b.go"), "changed\n")
	f.wait(t)
	// Simulate a legacy sidecar rewrite that did not preserve the readable flag.
	diffWrite(t, f.sidecar, fmt.Sprintf(`{"git_tree":%q,"git_head":%q,"git_branch":"main","git_checked":true,"role":"implementer"}`, f.tree, f.head))
	value, _, stderr := f.wait(t)
	if value["diff_stat"] != nil || stderr != "" {
		t.Fatalf("marker did not stop repeat=%v stderr=%s", value, stderr)
	}
}

func TestDiffReportMirroredFlag(t *testing.T) {
	f := newDiffFixture(t, "# Owned files\n- src/*.go\n", "Completed.\n", "implementer")
	diffWrite(t, filepath.Join(f.tree, "src", "b.go"), "changed\n")
	f.wait(t)
	// A distinct sidecar path carries the successful check, without its marker.
	copySide := filepath.Join(t.TempDir(), "mirror.dispatch.json")
	diffWrite(t, copySide, `{"role":"implementer"}`)
	gittree.Preserve(copySide, gittree.Fields(f.sidecar))
	originalBrief := filepath.Join(f.state, "briefs", "worker-fixture.md")
	brief, err := os.ReadFile(originalBrief)
	if err != nil {
		t.Fatal(err)
	}
	diffWrite(t, strings.TrimSuffix(copySide, ".dispatch.json")+".md", string(brief))
	info, err := os.Stat(originalBrief)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(strings.TrimSuffix(copySide, ".dispatch.json")+".md", info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if got := gittree.DiffReport(copySide, "worker", f.report, f.env, "", "wait"); got != nil {
		t.Fatalf("mirrored flag repeated audit: %v", got)
	}
	if _, err := os.Lstat(copySide + ".diff-checked"); !os.IsNotExist(err) {
		t.Fatalf("mirrored flag created marker: %v", err)
	}
}

func TestDiffReportReadOnlyDirect(t *testing.T) {
	f := newDiffFixture(t, "# Owned files\n- src/*.go\n", "Completed.\n", "implementer")
	diffWrite(t, filepath.Join(f.tree, "src", "b.go"), "changed\n")
	before := diffGitFiles(t, filepath.Join(f.tree, ".git"))
	if result := gittree.DiffReport(f.sidecar, "worker", f.report, f.env, "", "wait"); result == nil {
		t.Fatal("no result")
	}
	if after := diffGitFiles(t, filepath.Join(f.tree, ".git")); !reflect.DeepEqual(before, after) {
		t.Fatalf("direct audit changed index: before=%s after=%s", before["index"], after["index"])
	}
}

func TestDiffReportConcurrentClaim(t *testing.T) {
	f := newDiffFixture(t, "# Owned files\n- src/*.go\n", "Completed.\n", "implementer")
	diffWrite(t, filepath.Join(f.tree, "src", "b.go"), "changed\n")
	start := make(chan struct{})
	results := make(chan bool, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- gittree.DiffReport(f.sidecar, "worker", f.report, f.env, "", "wait") != nil
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	count := 0
	for result := range results {
		if result {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("concurrent audits=%d want=1", count)
	}
	t.Log("8 concurrent claims: one audit/artifact result")
}

func TestDiffReportWarningCap(t *testing.T) {
	f := newDiffFixture(t, "# Owned files\n- src/\n", "Completed.\n", "implementer")
	for i := 0; i < 12; i++ {
		diffWrite(t, filepath.Join(f.tree, fmt.Sprintf("other/f%02d.txt", i)), "new\n")
	}
	value, _, stderr := f.wait(t)
	outside, ok := value["outside_owned"].([]any)
	if !ok || len(outside) != 12 || !strings.Contains(stderr, ", … (+2 more)") || strings.Contains(stderr, "f10.txt") {
		t.Fatalf("value=%v stderr=%s", value, stderr)
	}
	t.Logf("full JSON names=%d capped warning=%q", len(outside), stderr)
}

func TestDiffReportWaitingDispatchProducer(t *testing.T) {
	f := newDiffFixture(t, "# Owned files\n- src/*.go\n", "Previous.\n", "implementer")
	diffWrite(t, filepath.Join(f.tree, "src", "b.go"), "changed\n")
	f.env["SOHO_PROMPT_CHECK_SECONDS"], f.env["SOHO_PROMPT_SETTLE_SECONDS"] = "0", "0"
	input := filepath.Join(filepath.Dir(f.state), "input.md")
	diffWrite(t, input, "# Goal\nDo it.\n# Owned files\n- src/*.go\n")
	bin := f.env["SOHO_FAKECLI_CONFIG"]
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
		{Argv: []string{"agent", "read", "worker"}, ArgvPrefix: true},
		{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
		{Argv: []string{"pane", "report-metadata"}, ArgvPrefix: true},
	}
	config, err := json.Marshal(struct {
		Log   string         `json:"log"`
		Rules []fakecli.Rule `json:"rules"`
	}{filepath.Join(bin, "herdr.calls.jsonl"), rules})
	if err != nil {
		t.Fatal(err)
	}
	diffWrite(t, filepath.Join(bin, "herdr.json"), string(config))
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
				b, e := os.ReadFile(filepath.Join(f.state, "last-report-worker"))
				if e == nil {
					report := strings.TrimSpace(string(b))
					if report != "" && report != f.report {
						if err := diffAfterCurrentDispatch(f, report, false); err != nil {
							return
						}
						return
					}
				}
			}
		}
	}()
	t.Cleanup(func() { close(stop); <-done })
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
	code := cli.Run([]string{"dispatch", "worker", input, "--role", "implementer", "--timeout", "2s"}, f.env)
	if code != 0 {
		t.Fatalf("dispatch code=%d out=%s stderr=%s", code, out.String(), stderr.String())
	}
	var value map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &value); err != nil {
		t.Fatal(err)
	}
	report, _ := value["report"].(string)
	artifact := strings.TrimSuffix(report, ".md") + ".diff-stat.txt"
	if _, err := os.Stat(artifact); err != nil || !strings.Contains(stderr.String(), "cites files the tree shows unchanged: src/a.go") {
		t.Fatalf("producer artifact error=%v out=%s stderr=%s", err, out.String(), stderr.String())
	}
	// Forwarding belongs to two CLI formatters outside this slice's ownership.
	t.Logf("waiting dispatch exit=%d status=%v artifact exists; diff_stat forwarded=%t cited_unchanged forwarded=%t", code, value["wait_status"], value["diff_stat"] != nil, value["cited_unchanged"] != nil)
}
