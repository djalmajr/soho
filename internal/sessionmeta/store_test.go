package sessionmeta

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
)

// fsFix is an actual-filesystem fixture with HOME and USERPROFILE pinned
// to it. The state base and session directory already exist; the record
// does not.
type fsFix struct {
	t    *testing.T
	raw  string // fixture root as TempDir returned it
	root string // canonical fixture root
	base string // state base under raw
	sess string
	st   Store
}

func newFS(t *testing.T, session string) *fsFix {
	t.Helper()
	raw := t.TempDir()
	root, err := filepath.EvalSymlinks(raw)
	if err != nil {
		t.Fatalf("fixture root cannot be canonical: %v", err)
	}
	home := filepath.Join(raw, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("fixture home cannot be made: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	base := filepath.Join(raw, "state")
	if err := os.MkdirAll(filepath.Join(base, session), 0o750); err != nil {
		t.Fatalf("fixture session cannot be made: %v", err)
	}
	return &fsFix{t: t, raw: raw, root: root, base: base, sess: session, st: Store{StateBase: base, SessionID: session}}
}

func (f *fsFix) recPath() string {
	return filepath.Join(f.base, f.sess, RecordName)
}

// wantJSON builds the expected record bytes independently of the
// implementation's encoder: the declared field order, JSON-encoded values.
func wantJSON(r Record) string {
	return fmt.Sprintf(`{"schema":%d,"project_root":%q,"session_id":%q,"terminal":%q,"name":%q,"kind":%q,"started_ms":%d,"task":%q}`,
		r.Schema, r.ProjectRoot, r.SessionID, r.Terminal, r.Name, r.Kind, r.StartedMS, r.Task)
}

func (f *fsFix) checkBytes(stage string, want Record) {
	f.t.Helper()
	data, err := os.ReadFile(f.recPath())
	if err != nil {
		f.t.Fatalf("%s: the record cannot be read back: %v", stage, err)
	}
	if string(data) != wantJSON(want) {
		f.t.Fatalf("%s: record bytes = %s, want %s", stage, data, wantJSON(want))
	}
}

type fileSnap struct {
	perm   uint32
	kind   string
	size   int64
	digest string
}

// snapTree walks dir and records every entry's permission, kind, size and
// content digest: the reader/writer no-write, no-repair snapshot.
func snapTree(t *testing.T, dir string) map[string]fileSnap {
	t.Helper()
	out := map[string]fileSnap{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		entry := fileSnap{perm: uint32(info.Mode().Perm()), size: info.Size()}
		switch {
		case d.IsDir():
			entry.kind = "dir"
		case d.Type()&os.ModeSymlink != 0:
			entry.kind = "symlink"
		case d.Type()&os.ModeNamedPipe != 0:
			entry.kind = "fifo"
		case d.Type().IsRegular():
			entry.kind = "file"
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			entry.digest = fmt.Sprintf("%x", sha256.Sum256(data))
		default:
			entry.kind = d.Type().String()
		}
		out[strings.TrimPrefix(path, dir)] = entry
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot cannot walk %s: %v", dir, err)
	}
	return out
}

func (f *fsFix) checkUnchanged(stage string, before map[string]fileSnap) {
	f.t.Helper()
	after := snapTree(f.t, f.raw)
	if !reflect.DeepEqual(before, after) {
		f.t.Fatalf("%s: the fixture tree changed:\nbefore: %#v\nafter:  %#v", stage, before, after)
	}
}

func (f *fsFix) sessionListing() []string {
	f.t.Helper()
	entries, err := os.ReadDir(filepath.Join(f.base, f.sess))
	if err != nil {
		f.t.Fatalf("the session listing cannot be read: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func (f *fsFix) requireOnlyRecord(stage string) {
	f.t.Helper()
	if got := f.sessionListing(); !reflect.DeepEqual(got, []string{RecordName}) {
		f.t.Fatalf("%s: the session holds %v, want only %v", stage, got, []string{RecordName})
	}
}

func projDir(t *testing.T) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("project dir cannot be canonical: %v", err)
	}
	return p
}

// TestRegisterLifecycle is the committed consumer for the full private
// register lifecycle: first init, task update, re-init, explicit clear,
// re-init, replacement, old-terminal refusal and a fresh different root —
// each asserted on the exact committed record bytes.
func TestRegisterLifecycle(t *testing.T) {
	f := newFS(t, "s1")
	projA, projB := projDir(t), projDir(t)
	st := f.st

	rec1 := Record{Schema: Schema, ProjectRoot: projA, SessionID: "s1", Terminal: "t1", Name: "orch-1", Kind: "pi", StartedMS: 1000, Task: "first"}
	got, err := st.Register(rec1)
	if err != nil || !reflect.DeepEqual(got, rec1) {
		f.t.Fatalf("first register got=%+v err=%v, want %+v nil", got, err, rec1)
	}
	f.checkBytes("first", rec1)
	if info, err := os.Stat(f.recPath()); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		f.t.Fatalf("first record mode = %v err=%v, want 0600", info, err)
	}

	if ok, err := st.SetTask(projA, "t1", "second"); err != nil || !ok {
		f.t.Fatalf("task update ok=%v err=%v, want true nil", ok, err)
	}
	rec2 := rec1
	rec2.Task = "second"
	f.checkBytes("task", rec2)

	re := Record{Schema: Schema, ProjectRoot: projA, SessionID: "s1", Terminal: "t1", Name: "orch-1", Kind: "pi", StartedMS: 2000, Task: "ignored"}
	if got, err := st.Register(re); err != nil || !reflect.DeepEqual(got, rec2) {
		f.t.Fatalf("re-init got=%+v err=%v, want the first start and the kept task %+v nil", got, err, rec2)
	}
	f.checkBytes("re-init", rec2)

	if ok, err := st.SetTask(projA, "t1", ""); err != nil || !ok {
		f.t.Fatalf("clear ok=%v err=%v, want true nil", ok, err)
	}
	rec2.Task = ""
	f.checkBytes("clear", rec2)
	if got, err := st.Register(re); err != nil || got.StartedMS != 1000 || got.Task != "" {
		f.t.Fatalf("re-init after clear got=%+v err=%v, want the explicit clear preserved", got, err)
	}
	f.checkBytes("re-init clear", rec2)

	rep := Record{Schema: Schema, ProjectRoot: projA, SessionID: "s1", Terminal: "t2", Name: "orch-2", Kind: "codex", StartedMS: 3000, Task: "replaced"}
	wantRep := rep
	wantRep.StartedMS = 1000
	if got, err := st.Register(rep); err != nil || !reflect.DeepEqual(got, wantRep) {
		f.t.Fatalf("replacement got=%+v err=%v, want the session start with the new identity and task %+v nil", got, err, wantRep)
	}
	f.checkBytes("replacement", wantRep)

	if ok, err := st.SetTask(projA, "t1", "stale"); err != nil || ok {
		f.t.Fatalf("old terminal set ok=%v err=%v, want false nil", ok, err)
	}
	f.checkBytes("old-terminal refused", wantRep)
	f.requireOnlyRecord("old-terminal refusal")

	fresh := Record{Schema: Schema, ProjectRoot: projB, SessionID: "s1", Terminal: "t3", Name: "orch-3", Kind: "grok", StartedMS: 4000, Task: "fresh"}
	if got, err := st.Register(fresh); err != nil || !reflect.DeepEqual(got, fresh) {
		f.t.Fatalf("different root got=%+v err=%v, want fresh from input %+v nil", got, err, fresh)
	}
	f.checkBytes("fresh root", fresh)
}

func TestReadAbsentAndMissing(t *testing.T) {
	f := newFS(t, "s1")
	before := snapTree(f.t, f.raw)

	rec, ok, err := f.st.Read()
	if err != nil || ok {
		f.t.Fatalf("absent record got %+v ok=%v err=%v, want zero false nil", rec, ok, err)
	}

	stNoSess := Store{StateBase: f.base, SessionID: "absent"}
	if rec, ok, err := stNoSess.Read(); err != nil || ok {
		f.t.Fatalf("absent session got %+v ok=%v err=%v, want zero false nil", rec, ok, err)
	}
	stNoBase := Store{StateBase: filepath.Join(f.base, "ghost"), SessionID: "s1"}
	if rec, ok, err := stNoBase.Read(); err != nil || ok {
		f.t.Fatalf("absent base got %+v ok=%v err=%v, want zero false nil", rec, ok, err)
	}
	stEmptyBase := Store{StateBase: "", SessionID: "s1"}
	if rec, ok, err := stEmptyBase.Read(); err != nil || ok {
		f.t.Fatalf("empty base got %+v ok=%v err=%v, want zero false nil", rec, ok, err)
	}

	fileBase := filepath.Join(f.raw, "filebase")
	if err := os.WriteFile(fileBase, []byte("not a dir"), 0o600); err != nil {
		f.t.Fatalf("file base cannot be made: %v", err)
	}
	stFileBase := Store{StateBase: fileBase, SessionID: "s1"}
	if _, _, err := stFileBase.Read(); err == nil {
		f.t.Fatalf("file base read, want an error")
	}

	var regErr error
	_, regErr = stNoSess.Register(Record{Schema: Schema, ProjectRoot: "/p", SessionID: "absent", Terminal: "t", Name: "n", Kind: "k", StartedMS: 1})
	if !errors.Is(regErr, ErrNotInitialized) {
		f.t.Fatalf("register on absent session err=%v, want ErrNotInitialized", regErr)
	}
	if ok, err := stNoSess.SetTask("/p", "t", "x"); err != nil || ok {
		f.t.Fatalf("set task on absent session ok=%v err=%v, want false nil", ok, err)
	}
	// The filebase above is this test's own fixture; the store operations
	// after the snapshot must create nothing.
	before = snapTree(f.t, f.raw)
	if _, err := stNoSess.Register(Record{Schema: Schema, ProjectRoot: "/p", SessionID: "absent", Terminal: "t", Name: "n", Kind: "k", StartedMS: 1}); !errors.Is(err, ErrNotInitialized) {
		f.t.Fatalf("second register on absent session err=%v, want ErrNotInitialized", err)
	}
	if ok, err := stNoSess.SetTask("/p", "t", "x"); err != nil || ok {
		f.t.Fatalf("second set task on absent session ok=%v err=%v, want false nil", ok, err)
	}
	f.checkUnchanged("absence and missing", before)
}

func TestReadBaseSymlinkAlias(t *testing.T) {
	f := newFS(t, "s1")
	alias := filepath.Join(f.raw, "alias")
	if err := os.Symlink(f.base, alias); err != nil {
		f.t.Skipf("symlinks unsupported here: %v", err)
	}
	st := Store{StateBase: alias, SessionID: "s1"}
	rec := Record{Schema: Schema, ProjectRoot: "/p", SessionID: "s1", Terminal: "t1", Name: "n", Kind: "k", StartedMS: 7, Task: "x"}
	if got, err := st.Register(rec); err != nil || got.StartedMS != 7 {
		f.t.Fatalf("register through alias got=%+v err=%v, want %s nil", got, err, wantJSON(rec))
	}
	if _, err := os.Stat(filepath.Join(f.base, "s1", RecordName)); err != nil {
		f.t.Fatalf("the record is not at the canonical base: %v", err)
	}
	if got, ok, err := st.Read(); err != nil || !ok || got.Task != "x" {
		f.t.Fatalf("read through alias got=%+v ok=%v err=%v", got, ok, err)
	}
}

func TestReadUnsafeRecords(t *testing.T) {
	proj := "/p"
	valid := `{"schema":1,"project_root":"/p","session_id":"s1","terminal":"t1","name":"n","kind":"k","started_ms":1,"task":"x"}`
	cases := []struct{ name, raw string }{
		{"invalid json", `{nope`},
		{"empty", ``},
		{"array", `[1]`},
		{"missing field", `{"schema":1,"project_root":"/p","session_id":"s1","terminal":"t1","name":"n","kind":"k","started_ms":1}`},
		{"null field", `{"schema":1,"project_root":"/p","session_id":"s1","terminal":"t1","name":"n","kind":"k","started_ms":1,"task":null}`},
		{"unknown field", `{"schema":1,"project_root":"/p","session_id":"s1","terminal":"t1","name":"n","kind":"k","started_ms":1,"task":"x","role":"orchestrator"}`},
		{"schema 2", `{"schema":2,"project_root":"/p","session_id":"s1","terminal":"t1","name":"n","kind":"k","started_ms":1,"task":"x"}`},
		{"negative start", `{"schema":1,"project_root":"/p","session_id":"s1","terminal":"t1","name":"n","kind":"k","started_ms":-1,"task":"x"}`},
		{"float start", `{"schema":1,"project_root":"/p","session_id":"s1","terminal":"t1","name":"n","kind":"k","started_ms":1.5,"task":"x"}`},
		{"trailing data", valid + "x"},
		{"control terminal", `{"schema":1,"project_root":"/p","session_id":"s1","terminal":"t\x01","name":"n","kind":"k","started_ms":1,"task":"x"}`},
		{"control name", `{"schema":1,"project_root":"/p","session_id":"s1","terminal":"t1","name":"n\x00","kind":"k","started_ms":1,"task":"x"}`},
		{"session mismatch", `{"schema":1,"project_root":"/p","session_id":"s9","terminal":"t1","name":"n","kind":"k","started_ms":1,"task":"x"}`},
		{"empty root", `{"schema":1,"project_root":"","session_id":"s1","terminal":"t1","name":"n","kind":"k","started_ms":1,"task":"x"}`},
		{"invalid utf8 task", `{"schema":1,"project_root":"/p","session_id":"s1","terminal":"t1","name":"n","kind":"k","started_ms":1,"task":"x\xff"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFS(t, "s1")
			raw := []byte(tc.raw)
			if err := os.WriteFile(f.recPath(), raw, 0o600); err != nil {
				t.Fatalf("the raw record cannot be written: %v", err)
			}
			before := snapTree(t, f.raw)
			if rec, ok, err := f.st.Read(); err == nil {
				t.Fatalf("unsafe record read got %+v ok=%v, want an error", rec, ok)
			}
			f.checkUnchanged("read", before)
		})
	}

	t.Run("oversized record", func(t *testing.T) {
		f := newFS(t, "s1")
		big := strings.Replace(valid, `"task":"x"`, `"task":"`+strings.Repeat("a", 70000)+`"`, 1)
		if err := os.WriteFile(f.recPath(), []byte(big), 0o600); err != nil {
			t.Fatalf("the oversized record cannot be written: %v", err)
		}
		if _, _, err := f.st.Read(); err == nil {
			t.Fatalf("oversized record read, want an error")
		}
		// The write bound refuses the same oversize record before any I/O.
		before := snapTree(t, f.raw)
		rec := Record{Schema: Schema, ProjectRoot: proj, SessionID: "s1", Terminal: "t1", Name: "n", Kind: "k", StartedMS: 1, Task: strings.Repeat("a", 70000)}
		if _, err := f.st.Register(rec); err == nil {
			t.Fatalf("oversized register, want an error")
		}
		f.checkUnchanged("oversize", before)
	})

	t.Run("record is a directory", func(t *testing.T) {
		f := newFS(t, "s1")
		if err := os.Mkdir(f.recPath(), 0o700); err != nil {
			t.Fatalf("the record directory cannot be made: %v", err)
		}
		if _, ok, err := f.st.Read(); err == nil || ok {
			t.Fatalf("directory record read ok=%v err=%v, want an error", ok, err)
		}
		if _, err := f.st.Register(Record{Schema: Schema, ProjectRoot: "/p", SessionID: "s1", Terminal: "t", Name: "n", Kind: "k", StartedMS: 1}); err == nil {
			t.Fatalf("register over a directory record, want an error")
		}
		if info, err := os.Stat(f.recPath()); err != nil || !info.IsDir() {
			t.Fatalf("the record directory changed: %v %v", info, err)
		}
		f.requireOnlyRecord("directory record")
	})
}

// TestReaderNeverModifies proves the reader's snapshot: no write, repair,
// permission change, lock or temp after a successful or a failed read, no
// cache, and the pre-existing 0644 permission is retained.
func TestReaderNeverModifies(t *testing.T) {
	f := newFS(t, "s1")
	rec := Record{Schema: Schema, ProjectRoot: "/p", SessionID: "s1", Terminal: "t1", Name: "n", Kind: "k", StartedMS: 1, Task: "old"}
	if err := os.WriteFile(f.recPath(), []byte(wantJSON(rec)), 0o644); err != nil {
		f.t.Fatalf("the record cannot be written: %v", err)
	}
	before := snapTree(f.t, f.raw)
	got, ok, err := f.st.Read()
	if err != nil || !ok || got.Task != "old" {
		f.t.Fatalf("read got %+v ok=%v err=%v", got, ok, err)
	}
	f.checkUnchanged("successful read", before)
	if info, _ := os.Stat(f.recPath()); runtime.GOOS != "windows" && info.Mode().Perm() != 0o644 {
		f.t.Fatalf("the reader changed the record mode to %v", info.Mode().Perm())
	}

	corrupt := `{"schema":1`
	if err := os.WriteFile(f.recPath(), []byte(corrupt), 0o644); err != nil {
		f.t.Fatalf("the corrupt record cannot be written: %v", err)
	}
	before = snapTree(f.t, f.raw)
	if _, ok, err := f.st.Read(); err == nil {
		f.t.Fatalf("corrupt record read ok=%v, want an error", ok)
	}
	f.checkUnchanged("failed read", before)

	// No cache: a direct external rewrite is visible on the next read.
	rec.Task = "new"
	if err := os.WriteFile(f.recPath(), []byte(wantJSON(rec)), 0o644); err != nil {
		f.t.Fatalf("the rewritten record cannot be written: %v", err)
	}
	got, ok, err = f.st.Read()
	if err != nil || !ok || got.Task != "new" {
		f.t.Fatalf("re-read got %+v ok=%v err=%v, want the new bytes (no cache)", got, ok, err)
	}
}

func TestRegisterSessionMismatch(t *testing.T) {
	f := newFS(t, "s1")
	before := snapTree(f.t, f.raw)
	rec := Record{Schema: Schema, ProjectRoot: "/p", SessionID: "other", Terminal: "t1", Name: "n", Kind: "k", StartedMS: 1}
	if _, err := f.st.Register(rec); err == nil {
		f.t.Fatalf("mismatched session register, want an error")
	}
	if ok, err := f.st.SetTask("", "", "x"); err != nil || ok {
		f.t.Fatalf("empty identity set task ok=%v err=%v, want false nil", ok, err)
	}
	if ok, err := f.st.SetTask("/p", "t1", "\xff"); err == nil {
		f.t.Fatalf("invalid utf8 task ok=%v, want an error", ok)
	}
	f.checkUnchanged("mismatch", before)
}

func TestSetTaskMismatchNoInit(t *testing.T) {
	f := newFS(t, "s1")
	proj := projDir(t)
	rec := Record{Schema: Schema, ProjectRoot: proj, SessionID: "s1", Terminal: "t1", Name: "n", Kind: "k", StartedMS: 1, Task: "base"}
	if _, err := f.st.Register(rec); err != nil {
		f.t.Fatalf("register: %v", err)
	}
	before := snapTree(f.t, f.raw)
	if ok, err := f.st.SetTask(filepath.Join(proj, "else"), "t1", "x"); err != nil || ok {
		f.t.Fatalf("different root ok=%v err=%v, want false nil", ok, err)
	}
	if ok, err := f.st.SetTask(proj, "t2", "x"); err != nil || ok {
		f.t.Fatalf("different terminal ok=%v err=%v, want false nil", ok, err)
	}
	f.checkBytes("mismatch", rec)
	f.requireOnlyRecord("mismatch left nothing behind")
	f.checkUnchanged("mismatch", before)
}

// TestPermission0600 pins the publish permission: fresh records are 0600
// and a previously permissive record is replaced at 0600.
func TestPermission0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX owner/group mode bits; reader snapshots verify native attributes separately")
	}
	f := newFS(t, "s1")
	rec := Record{Schema: Schema, ProjectRoot: "/p", SessionID: "s1", Terminal: "t1", Name: "n", Kind: "k", StartedMS: 1, Task: "base"}
	if err := os.WriteFile(f.recPath(), []byte(wantJSON(rec)), 0o644); err != nil {
		f.t.Fatalf("the permissive record cannot be written: %v", err)
	}
	rec.Task = "next"
	if _, err := f.st.Register(rec); err != nil {
		f.t.Fatalf("register over a permissive record: %v", err)
	}
	if info, err := os.Stat(f.recPath()); err != nil || info.Mode().Perm() != 0o600 {
		f.t.Fatalf("replaced record mode = %v err=%v, want 0600", info, err)
	}
}

// TestPublishRenameFailure is the white-box failure path: the record name
// holds a directory, so the atomic replace must fail, the own temp must be
// removed with retained identity, and the directory must stay untouched.
func TestPublishRenameFailure(t *testing.T) {
	f := newFS(t, "s1")
	if err := os.Mkdir(f.recPath(), 0o700); err != nil {
		f.t.Fatalf("the record directory cannot be made: %v", err)
	}
	sessRoot, missing, err := f.st.openSession()
	if err != nil || missing {
		f.t.Fatalf("session open missing=%v err=%v", missing, err)
	}
	defer sessRoot.Close()
	data, err := marshalRecord(Record{Schema: Schema, ProjectRoot: "/p", SessionID: "s1", Terminal: "t1", Name: "n", Kind: "k", StartedMS: 1})
	if err != nil {
		f.t.Fatalf("marshal: %v", err)
	}
	if err := publishRecord(sessRoot, data); err == nil {
		f.t.Fatalf("rename over a directory succeeded, want a failure")
	}
	if info, err := os.Stat(f.recPath()); err != nil || !info.IsDir() {
		f.t.Fatalf("the record directory changed: %v %v", info, err)
	}
	f.requireOnlyRecord("rename failure left nothing behind")
}

// TestLockNotStolen pre-creates the lock directory as another writer: the
// bounded wait must end in ErrLocked, must not delete the foreign lock,
// and must not write the record.
func TestLockNotStolen(t *testing.T) {
	f := newFS(t, "s1")
	if err := os.Mkdir(filepath.Join(f.base, f.sess, lockName), 0o700); err != nil {
		f.t.Fatalf("the foreign lock cannot be made: %v", err)
	}
	start := time.Now()
	_, err := f.st.Register(Record{Schema: Schema, ProjectRoot: "/p", SessionID: "s1", Terminal: "t1", Name: "n", Kind: "k", StartedMS: 1})
	elapsed := time.Since(start)
	if !errors.Is(err, ErrLocked) {
		f.t.Fatalf("locked register err=%v, want ErrLocked", err)
	}
	if elapsed < 4*time.Second || elapsed > 15*time.Second {
		f.t.Fatalf("the bounded wait took %v, want between 4s and 15s", elapsed)
	}
	if info, err := os.Stat(filepath.Join(f.base, f.sess, lockName)); err != nil || !info.IsDir() {
		f.t.Fatalf("the foreign lock was touched: %v %v", info, err)
	}
	if _, err := os.Stat(f.recPath()); !os.IsNotExist(err) {
		f.t.Fatalf("a record appeared under a foreign lock: %v", err)
	}
}

// TestConcurrentWritersReaders releases a barrier of concurrent
// Register/SetTask writers and readers over the same record; every reader
// must see a valid record or an explicit identity-change refusal, the final bytes must satisfy the
// preservation rules, and no lock or temp may remain.
func TestConcurrentWritersReaders(t *testing.T) {
	f := newFS(t, "s1")
	proj := projDir(t)
	base := Record{Schema: Schema, ProjectRoot: proj, SessionID: "s1", Terminal: "t0", Name: "orch-0", Kind: "pi", StartedMS: 1000, Task: "base"}
	if _, err := f.st.Register(base); err != nil {
		f.t.Fatalf("initial register: %v", err)
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	run := func(fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			fn()
		}()
	}
	mk := func(terminal, name, kind string, start int64, task string) Record {
		return Record{Schema: Schema, ProjectRoot: proj, SessionID: "s1", Terminal: terminal, Name: name, Kind: kind, StartedMS: start, Task: task}
	}
	run(func() {
		if _, err := f.st.Register(mk("t1", "orch-1", "pi", 2000, "a")); err != nil {
			f.t.Errorf("concurrent register t1: %v", err)
		}
	})
	run(func() {
		if _, err := f.st.Register(mk("t2", "orch-2", "codex", 3000, "b")); err != nil {
			f.t.Errorf("concurrent register t2: %v", err)
		}
	})
	run(func() {
		if _, err := f.st.SetTask(proj, "t1", "x1"); err != nil {
			f.t.Errorf("concurrent set task t1: %v", err)
		}
	})
	run(func() {
		if _, err := f.st.SetTask(proj, "t2", "x2"); err != nil {
			f.t.Errorf("concurrent set task t2: %v", err)
		}
	})
	for i := 0; i < 4; i++ {
		run(func() {
			valid := 0
			for j := 0; j < 50; j++ {
				rec, ok, err := f.st.Read()
				if errors.Is(err, ErrRecordChanged) {
					if ok || rec != (Record{}) {
						f.t.Errorf("an unstable read returned a record: %+v ok=%v", rec, ok)
					}
					continue
				}
				if err != nil || !ok {
					f.t.Errorf("concurrent read got %+v ok=%v err=%v, want a valid record", rec, ok, err)
					return
				}
				valid++
				if rec.ProjectRoot != proj || rec.SessionID != "s1" || rec.StartedMS != 1000 {
					f.t.Errorf("concurrent read returned the wrong identity: %+v", rec)
				}
			}
			if valid == 0 {
				f.t.Error("the concurrent reader never observed a stable valid record")
			}
		})
	}
	close(start)
	wg.Wait()

	got, ok, err := f.st.Read()
	if err != nil || !ok {
		f.t.Fatalf("final read got ok=%v err=%v", ok, err)
	}
	if got.ProjectRoot != proj {
		f.t.Fatalf("final root = %q, want %q", got.ProjectRoot, proj)
	}
	if got.StartedMS != 1000 {
		f.t.Fatalf("final start = %d, want the first recorded 1000", got.StartedMS)
	}
	byTerminal := map[string][]string{"t1": {"a", "x1"}, "t2": {"b", "x2"}}
	if !contains(byTerminal[got.Terminal], got.Task) {
		f.t.Fatalf("final terminal %q task %q is not a committed combination", got.Terminal, got.Task)
	}
	f.requireOnlyRecord("concurrent writers left no lock or temp")
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// TestReplacementBetweenCheckAndOpen swaps the record after the identity
// check and before the open: the stale handle must be refused, never
// consumed.
func TestReplacementBetweenCheckAndOpen(t *testing.T) {
	f := newFS(t, "s1")
	rec := Record{Schema: Schema, ProjectRoot: "/p", SessionID: "s1", Terminal: "t1", Name: "n", Kind: "k", StartedMS: 1, Task: "kept"}
	if _, err := f.st.Register(rec); err != nil {
		f.t.Fatalf("register: %v", err)
	}
	sessRoot, missing, err := f.st.openSession()
	if err != nil || missing {
		f.t.Fatalf("session open missing=%v err=%v", missing, err)
	}
	defer sessRoot.Close()
	pre, err := sessRoot.Lstat(RecordName)
	if err != nil || !pre.Mode().IsRegular() {
		f.t.Fatalf("pre lstat: %v %v", pre, err)
	}
	other := filepath.Join(f.base, f.sess, "other.json")
	if err := os.Rename(f.recPath(), other); err != nil {
		f.t.Fatalf("the record cannot be moved aside: %v", err)
	}
	if err := os.WriteFile(f.recPath(), []byte(`{"schema":1,"project_root":"/p","session_id":"s9","terminal":"t9","name":"n","kind":"k","started_ms":9,"task":"other"}`), 0o600); err != nil {
		f.t.Fatalf("the replacement cannot be written: %v", err)
	}
	if _, ok, err := readRecordHandle(sessRoot, pre, "s1"); err == nil {
		f.t.Fatalf("stale-handle read ok=%v, want the identity refusal", ok)
	}
	// The fresh path sees the replacement as an identity-mismatched record.
	if _, _, err := readRecord(sessRoot, "s1"); err == nil {
		f.t.Fatalf("replacement read, want the session mismatch error")
	}
	if err := os.Rename(f.recPath(), other+"-swapped"); err != nil {
		f.t.Fatalf("the replacement cannot be moved: %v", err)
	}
	if err := os.Rename(other, f.recPath()); err != nil {
		f.t.Fatalf("the original cannot be restored: %v", err)
	}
	if got, ok, err := f.st.Read(); err != nil || !ok || got.Task != "kept" {
		f.t.Fatalf("restored read got %+v ok=%v err=%v, want the original record", got, ok, err)
	}
}

// TestSessionAndRecordLinks: linked session or record components are
// refused, and the link target outside the session is left untouched.
func TestSessionAndRecordLinks(t *testing.T) {
	f := newFS(t, "s1")

	t.Run("session symlink", func(t *testing.T) {
		outside := filepath.Join(f.raw, "outside")
		if err := os.MkdirAll(outside, 0o750); err != nil {
			t.Fatalf("the outside dir cannot be made: %v", err)
		}
		marker := filepath.Join(outside, "marker")
		if err := os.WriteFile(marker, []byte("m"), 0o600); err != nil {
			t.Fatalf("the marker cannot be written: %v", err)
		}
		if err := os.Symlink(outside, filepath.Join(f.base, "linked")); err != nil {
			t.Skipf("symlinks unsupported here: %v", err)
		}
		st := Store{StateBase: f.base, SessionID: "linked"}
		if rec, ok, err := st.Read(); err == nil {
			t.Fatalf("linked session read got %+v ok=%v, want an error", rec, ok)
		}
		if _, err := st.Register(Record{Schema: Schema, ProjectRoot: "/p", SessionID: "linked", Terminal: "t", Name: "n", Kind: "k", StartedMS: 1}); err == nil {
			t.Fatalf("linked session register, want an error")
		}
		if data, err := os.ReadFile(marker); err != nil || string(data) != "m" {
			t.Fatalf("the outside target changed: %q %v", data, err)
		}
		if _, err := os.Stat(filepath.Join(outside, RecordName)); !os.IsNotExist(err) {
			t.Fatalf("a record appeared at the outside target: %v", err)
		}
	})

	t.Run("session regular file", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(f.base, "file-sess"), []byte("x"), 0o600); err != nil {
			t.Fatalf("the session file cannot be written: %v", err)
		}
		st := Store{StateBase: f.base, SessionID: "file-sess"}
		if _, _, err := st.Read(); err == nil {
			t.Fatalf("file session read, want an error")
		}
		if _, err := st.Register(Record{Schema: Schema, ProjectRoot: "/p", SessionID: "file-sess", Terminal: "t", Name: "n", Kind: "k", StartedMS: 1}); err == nil {
			t.Fatalf("file session register, want an error")
		}
	})

	t.Run("record symlink", func(t *testing.T) {
		if err := os.MkdirAll(filepath.Join(f.base, "s3"), 0o750); err != nil {
			t.Fatalf("the session cannot be made: %v", err)
		}
		outside := filepath.Join(f.raw, "outside-rec")
		if err := os.MkdirAll(outside, 0o750); err != nil {
			t.Fatalf("the outside dir cannot be made: %v", err)
		}
		target := filepath.Join(outside, RecordName)
		if err := os.WriteFile(target, []byte(`{"schema":1,"project_root":"/p","session_id":"s3","terminal":"t1","name":"n","kind":"k","started_ms":1,"task":"x"}`), 0o600); err != nil {
			t.Fatalf("the outside record cannot be written: %v", err)
		}
		if err := os.Symlink(target, filepath.Join(f.base, "s3", RecordName)); err != nil {
			t.Skipf("symlinks unsupported here: %v", err)
		}
		st := Store{StateBase: f.base, SessionID: "s3"}
		if rec, ok, err := st.Read(); err == nil {
			t.Fatalf("linked record read got %+v ok=%v, want an error", rec, ok)
		}
		if _, err := st.Register(Record{Schema: Schema, ProjectRoot: "/p", SessionID: "s3", Terminal: "t", Name: "n", Kind: "k", StartedMS: 1}); err == nil {
			t.Fatalf("linked record register, want an error")
		}
		if data, err := os.ReadFile(target); err != nil || !strings.Contains(string(data), `"task":"x"`) {
			t.Fatalf("the outside record target changed: %q %v", data, err)
		}
	})
}

func TestValidSessionID(t *testing.T) {
	accepted := []string{"s1", "hc-1.2.3.4-8080", "sèsion-42", "session_1", "COM10"}
	rejected := []string{"", ".", "..", "a/b", `a\b`, "a:b", `a"b`, `a<b`, "a|b", "a*b", "a?", "a.", "a ", "a\x01", "CON", "prn", "AUX.txt", "NUL.json", "COM1", "LPT9", "COM¹", "\xff"}
	for _, value := range accepted {
		if !ValidSessionID(value) {
			t.Fatalf("ValidSessionID(%q) = false, want true", value)
		}
	}
	for _, value := range rejected {
		if ValidSessionID(value) {
			t.Fatalf("ValidSessionID(%q) = true, want false", value)
		}
	}
	// The helper is a thin call to the shared platform validator.
	all := append(append([]string{}, accepted...), rejected...)
	for _, value := range all {
		if ValidSessionID(value) != platform.ValidStateComponent(value) {
			t.Fatalf("ValidSessionID(%q) diverges from the platform validator", value)
		}
	}
}

// TestRequiredNames: Name and Kind are required safe identities on both
// writes and stored reads: an empty or control-bearing value is refused
// before publication, and stored bytes with an empty required identity are
// refused by Read. Catalog membership stays the caller's concern, and the
// ordinary nonempty lifecycle is the passing control.
func TestRequiredNames(t *testing.T) {
	rec := Record{Schema: Schema, ProjectRoot: "/p", SessionID: "s1", Terminal: "t1", Name: "orchestrator", Kind: "codex", StartedMS: 1, Task: "task"}
	for _, field := range []string{"Name", "Kind"} {
		t.Run(field, func(t *testing.T) {
			f := newFS(t, "s1")
			empty := rec
			if field == "Name" {
				empty.Name = ""
			} else {
				empty.Kind = ""
			}
			if got, err := f.st.Register(empty); err == nil {
				t.Fatalf("Register accepted empty %s: %+v", field, got)
			}
			bad := rec
			if field == "Name" {
				bad.Name = "a\x01"
			} else {
				bad.Kind = "b\x00"
			}
			if _, err := f.st.Register(bad); err == nil {
				t.Fatalf("Register accepted a control-bearing %s", field)
			}
			stored := `{"schema":1,"project_root":"/p","session_id":"s1","terminal":"t1","name":"n","kind":"k","started_ms":1,"task":"x"}`
			if field == "Kind" {
				stored = `{"schema":1,"project_root":"/p","session_id":"s1","terminal":"t1","name":"n","kind":"","started_ms":1,"task":"x"}`
			} else {
				stored = `{"schema":1,"project_root":"/p","session_id":"s1","terminal":"t1","name":"","kind":"k","started_ms":1,"task":"x"}`
			}
			if err := os.WriteFile(f.recPath(), []byte(stored), 0o600); err != nil {
				t.Fatalf("the stored record cannot be written: %v", err)
			}
			if got, ok, err := f.st.Read(); err == nil {
				t.Fatalf("Read accepted empty %s: %+v ok=%v", field, got, ok)
			}
		})
	}
	f := newFS(t, "s1")
	if got, err := f.st.Register(rec); err != nil || got.Name != rec.Name || got.Kind != rec.Kind {
		t.Fatalf("nonempty identities got %+v err=%v, want the record unchanged", got, err)
	}
}

// TestRawUTF8Bytes: an actual invalid UTF-8 byte in the bounded raw record
// is refused before JSON decoding can replace it, in a Name and in a Task;
// the reader leaves the stored bytes untouched, and valid multi-byte UTF-8
// still registers and reads.
func TestRawUTF8Bytes(t *testing.T) {
	for _, field := range []string{"name", "task"} {
		t.Run(field, func(t *testing.T) {
			f := newFS(t, "s1")
			raw := []byte(wantJSON(Record{Schema: Schema, ProjectRoot: "/p", SessionID: "s1", Terminal: "t1", Name: "orchestrator", Kind: "codex", StartedMS: 1, Task: "task"}))
			needle := []byte(`"` + field + `":"`)
			at := bytes.Index(raw, needle) + len(needle)
			if at <= len(needle) {
				t.Fatalf("the %s value cannot be located in %s", field, raw)
			}
			raw = append(append(append([]byte{}, raw[:at]...), 0xff), raw[at:]...)
			if err := os.WriteFile(f.recPath(), raw, 0o600); err != nil {
				t.Fatalf("the raw record cannot be written: %v", err)
			}
			before := snapTree(t, f.raw)
			if got, ok, err := f.st.Read(); err == nil || ok {
				t.Fatalf("raw invalid UTF-8 accepted: ok=%v err=%v record=%+v", ok, err, got)
			}
			f.checkUnchanged("invalid UTF-8 read", before)
		})
	}
	t.Run("valid multi-byte utf8", func(t *testing.T) {
		f := newFS(t, "s1")
		rec := Record{Schema: Schema, ProjectRoot: "/p", SessionID: "s1", Terminal: "t1", Name: "orçestrator", Kind: "cödex", StartedMS: 1, Task: "tâsk"}
		if got, err := f.st.Register(rec); err != nil || got.Name != rec.Name || got.Kind != rec.Kind || got.Task != rec.Task {
			t.Fatalf("valid multi-byte register got %+v err=%v, want the record unchanged", got, err)
		}
	})
}

// TestTempOwnership: the temp cleanup decides by the retained identity of
// the opened temp, never by size alone: a same-size replacement is left
// exactly as found, the owned temp is removed, and a missing temp is not a
// failure.
func TestTempOwnership(t *testing.T) {
	for _, replace := range []bool{false, true} {
		name := "owned-control"
		if replace {
			name = "replaced"
		}
		t.Run(name, func(t *testing.T) {
			f := newFS(t, "s1")
			r, _, err := f.st.openSession()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			p := "owned.tmp"
			own, err := r.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				t.Fatalf("the own temp cannot be created: %v", err)
			}
			if _, err := own.Write([]byte("aa")); err != nil {
				t.Fatalf("the own temp cannot be written: %v", err)
			}
			opened, err := own.Stat()
			if err != nil {
				t.Fatalf("the own temp identity cannot be retained: %v", err)
			}
			own.Close()
			if replace {
				if err := r.Rename(p, p+"-saved"); err != nil {
					t.Fatalf("the own temp cannot be moved aside: %v", err)
				}
				if err := r.WriteFile(p, []byte("bb"), 0o600); err != nil {
					t.Fatalf("the replacement cannot be placed: %v", err)
				}
			}
			if err := removeOwn(r, p, opened); err != nil {
				t.Fatalf("removeOwn err=%v, want nil (an unowned or missing temp is left as found, not a failure)", err)
			}
			info, err := r.Lstat(p)
			if replace {
				if err != nil || !info.Mode().IsRegular() {
					t.Fatalf("cleanup deleted an unowned replacement with the same size: %v", err)
				}
				data, err := r.ReadFile(p)
				if err != nil || string(data) != "bb" {
					t.Fatalf("the replacement bytes changed: %q %v", data, err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("owned cleanup control err=%v", err)
			}
		})
	}
}

// TestSessionCheckOpenBinding: a staged swap of the checked session entry
// to a relative symlink for a sibling is refused at the check/open seam
// before any read or write reaches the sibling; the ordinary restored
// session is the passing control, and the public seam refuses the swapped
// session as well.
func TestSessionCheckOpenBinding(t *testing.T) {
	f := newFS(t, "s1")
	base, err := os.OpenRoot(f.base)
	if err != nil {
		t.Fatalf("the base root cannot be opened: %v", err)
	}
	defer base.Close()
	before, err := base.Lstat("s1")
	if err != nil || !before.IsDir() {
		t.Fatalf("the checked session cannot be read: %v", err)
	}
	// Stage the swap: the checked entry becomes a relative symlink for a
	// sibling before the actual open.
	if err := base.Rename("s1", "s1-saved"); err != nil {
		t.Fatalf("the session cannot be moved aside: %v", err)
	}
	if err := base.Mkdir("sibling", 0o700); err != nil {
		t.Fatalf("the sibling cannot be made: %v", err)
	}
	if err := os.Symlink("sibling", filepath.Join(f.base, "s1")); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}

	if root, err := openSessionDir(base, "s1", before); err == nil {
		_ = root.Close()
		t.Fatalf("the staged session swap opened without refusal")
	}
	entries, err := os.ReadDir(filepath.Join(f.base, "sibling"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("the sibling received entries after the refused swap: %v %v", entries, err)
	}
	if _, _, err := f.st.Read(); err == nil {
		t.Fatalf("public read through the swapped session, want an error")
	}

	// Positive control: restoring the checked directory works again.
	if err := os.RemoveAll(filepath.Join(f.base, "s1")); err != nil {
		t.Fatalf("the swapped link cannot be removed: %v", err)
	}
	if err := os.Rename(filepath.Join(f.base, "s1-saved"), filepath.Join(f.base, "s1")); err != nil {
		t.Fatalf("the session cannot be restored: %v", err)
	}
	if _, _, err := f.st.Read(); err != nil {
		t.Fatalf("restored session read: %v", err)
	}
}

// TestReleaseFailureOwnLock: an unresolved own lock after publication is an
// explicit error from the writer transaction, the published record stays
// readable (no rollback claim), the retained lock blocks a later writer
// with no stale prune, and a primary failure joins the cleanup failure
// without discarding either.
func TestReleaseFailureOwnLock(t *testing.T) {
	for _, operation := range []string{"Register", "SetTask"} {
		t.Run(operation, func(t *testing.T) {
			f := newFS(t, "s1")
			rec := Record{Schema: Schema, ProjectRoot: "/p", SessionID: "s1", Terminal: "t1", Name: "orchestrator", Kind: "codex", StartedMS: 1, Task: "base"}
			if operation == "SetTask" {
				if _, err := f.st.Register(rec); err != nil {
					t.Fatalf("the initial register: %v", err)
				}
			}
			sessRoot, missing, err := f.st.openSession()
			if err != nil || missing {
				t.Fatalf("session open missing=%v err=%v", missing, err)
			}
			defer sessRoot.Close()
			err = withLockedRecord(sessRoot, func() error {
				// Plant an owned child inside the verified own lock: the retained
				// lock can no longer be removed, a genuine cleanup failure.
				if err := sessRoot.WriteFile(lockName+"/owned-blocker", []byte("x"), 0o600); err != nil {
					t.Errorf("the blocker cannot be planted: %v", err)
					return err
				}
				cur, present, err := readRecord(sessRoot, f.sess)
				if err != nil {
					return err
				}
				merged := rec
				if operation == "SetTask" {
					if !present || cur.Terminal != rec.Terminal {
						return errors.New("the transaction target is missing")
					}
					merged = cur
					merged.Task = "next"
				} else {
					merged = mergeRecord(cur, present, rec)
				}
				data, err := marshalRecord(merged)
				if err != nil {
					return err
				}
				return publishRecord(sessRoot, data)
			})
			if err == nil {
				t.Fatalf("%s returned success with an unresolved own lock", operation)
			}
			if !strings.Contains(err.Error(), "lock") {
				t.Fatalf("the unresolved own lock is not named in the result: %v", err)
			}
			got, ok, readErr := f.st.Read()
			if readErr != nil || !ok {
				t.Fatalf("the published bytes are unavailable: %+v %v", got, readErr)
			}
			if operation == "SetTask" && got.Task != "next" {
				t.Fatalf("the published task = %q, want the transaction's next", got.Task)
			}
			if info, err := os.Stat(filepath.Join(f.base, f.sess, lockName, "owned-blocker")); err != nil || !info.Mode().IsRegular() {
				t.Fatalf("the unresolved own lock is gone: %v %v", info, err)
			}
			// No stale prune: a later writer sees the retained lock and waits it out.
			if _, lockErr := f.st.SetTask("/p", "t1", "again"); !errors.Is(lockErr, ErrLocked) {
				t.Fatalf("a later writer err=%v, want ErrLocked on the retained lock", lockErr)
			}
		})
	}

	t.Run("primary and cleanup join", func(t *testing.T) {
		f := newFS(t, "s1")
		sessRoot, missing, err := f.st.openSession()
		if err != nil || missing {
			t.Fatalf("session open missing=%v err=%v", missing, err)
		}
		defer sessRoot.Close()
		primary := errors.New("the transaction failed for its own reason")
		err = withLockedRecord(sessRoot, func() error {
			if err := sessRoot.WriteFile(lockName+"/owned-blocker", []byte("x"), 0o600); err != nil {
				return err
			}
			return primary
		})
		if err == nil || !strings.Contains(err.Error(), "the transaction failed for its own reason") || !strings.Contains(err.Error(), "lock") {
			t.Fatalf("the joined error must keep the primary and the cleanup failure: %v", err)
		}
	})

	t.Run("release controls", func(t *testing.T) {
		f := newFS(t, "s1")
		sessRoot, missing, err := f.st.openSession()
		if err != nil || missing {
			t.Fatalf("session open missing=%v err=%v", missing, err)
		}
		defer sessRoot.Close()
		// The own empty lock releases cleanly and disappears.
		saved, err := acquireLock(sessRoot)
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		if err := releaseLock(sessRoot, saved); err != nil {
			t.Fatalf("own lock release err=%v, want nil", err)
		}
		if _, err := sessRoot.Lstat(lockName); !os.IsNotExist(err) {
			t.Fatalf("the own lock remains after release: %v", err)
		}
		// A missing lock is a clean release.
		savedMissing, err := acquireLock(sessRoot)
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		if err := sessRoot.Remove(lockName); err != nil {
			t.Fatalf("the lock cannot be removed for the missing case: %v", err)
		}
		if err := releaseLock(sessRoot, savedMissing); err != nil {
			t.Fatalf("release of a missing lock err=%v, want nil", err)
		}
		// A replaced entry is never authorized for removal under a stale
		// identity: the current directory stays exactly as found.
		savedStale, err := acquireLock(sessRoot)
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		if err := sessRoot.Remove(lockName); err != nil {
			t.Fatalf("the lock cannot be removed for the replacement case: %v", err)
		}
		savedCurrent, err := acquireLock(sessRoot)
		if err != nil {
			t.Fatalf("acquire replacement: %v", err)
		}
		if err := releaseLock(sessRoot, savedStale); err != nil {
			t.Fatalf("release with a stale identity err=%v, want nil", err)
		}
		cur, err := sessRoot.Lstat(lockName)
		if err != nil || !os.SameFile(cur, savedCurrent) {
			t.Fatalf("the current lock was touched by a stale release: %v", err)
		}
		if err := releaseLock(sessRoot, savedCurrent); err != nil {
			t.Fatalf("final own release err=%v, want nil", err)
		}
	})
}
