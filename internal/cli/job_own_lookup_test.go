package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

// ownFix is a jobFix carrying a "starter" checkout: a real git repository
// with an initial commit, the job worktree <co>/.worktrees/job-9 (branch
// job/job-9) and the store <co>/.soho/jobs/job-9 with a valid
// state.json (schema 1, running, worktree mode, the measured checkout and
// job directory recorded). underRoot selects where the checkout lives:
// under job_repos_root (the root scan hits its store too) or outside it
// (only the own-checkout anchor can find it). The caller's user
// configuration never names the checkout: job_repos_root stays at the
// fixture's repos.
type ownFix struct {
	f        *jobFix
	env      platform.Env
	gitEnv   platform.Env
	co       string
	worktree string
	store    string
}

// ownJobID is the starter job's id; the worktree top the anchor measures
// is <co>/.worktrees/job-<id>.
const ownJobID = "job-9"

// newOwnFix builds the starter at a path with a space in it (the hostile
// path case for the anchor's git query and its state.json fields). git is
// on the fixture PATH through linkSystemGit, and every git call the fixture
// or the production anchor makes runs with empty GIT_CONFIG_GLOBAL /
// GIT_CONFIG_SYSTEM files and -c user.name/user.email, so no global
// configuration is read or written.
func newOwnFix(t *testing.T, underRoot bool) *ownFix {
	t.Helper()
	f := newJobFix(t)
	bin := filepath.Join(f.root, "bin")
	linkSystemGit(t, bin)

	globalCfg := filepath.Join(f.root, "gitconfig.global")
	systemCfg := filepath.Join(f.root, "gitconfig.system")
	for _, cfg := range []string{globalCfg, systemCfg} {
		if err := os.WriteFile(cfg, []byte{}, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Resolve the skill from the captured package directory even when
	// t.Chdir moves these runs into a fixture checkout.
	skillDir, err := filepath.Abs(filepath.Join(packageDir, "..", "..", "skills", "soho"))
	if err != nil {
		t.Fatal(err)
	}
	env := withEnv(f.env,
		"SOHO_SKILL_DIR", skillDir,
		"GIT_CONFIG_GLOBAL", globalCfg,
		"GIT_CONFIG_SYSTEM", systemCfg)
	gitEnv := withEnv(env, "GIT_CONFIG_NOSYSTEM", "1")

	co := filepath.Join(f.root, "starter", "starter org", "starter repo")
	if underRoot {
		co = filepath.Join(f.repos, "starter org", "starter repo")
	}
	if err := os.MkdirAll(co, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, gitEnv, co, "init", "-q")
	runGit(t, gitEnv, co,
		"-c", "user.name=fixture", "-c", "user.email=fixture@example.org",
		"commit", "-q", "--allow-empty", "-m", "init")
	worktree := filepath.Join(co, ".worktrees", "job-"+ownJobID)
	// The worktree name must be job-<id>: the anchor's worktree mode
	// requires the measured top to be <checkout>/.worktrees/job-<id> and
	// the .git / gitdir files to name each other (worktreeRegistered).
	runGit(t, gitEnv, co, "worktree", "add", "-q", worktree, "-b", "job/"+ownJobID)

	store := filepath.Join(co, ".soho")
	jobDir := filepath.Join(store, "jobs", ownJobID)
	if err := os.MkdirAll(jobDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// The valid state.json the anchor reads: schema 1, id, modo equal to
	// the measured worktree mode, and checkout/dir absolute and equal by
	// real path to the measured checkout and worktree top. The anchor
	// returns nil when any of those checks fails, so this body is exactly
	// what makes the anchor fire.
	state := fmt.Sprintf(`{"schema":1,"id":%q,"status":"running","brief_sha256":"x","decisions_acked_seq":0,"motivo":null,"checkout":%q,"dir":%q,"modo":"worktree"}`+"\n",
		ownJobID, co, worktree)
	if err := os.WriteFile(filepath.Join(jobDir, "state.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	return &ownFix{f: f, env: env, gitEnv: gitEnv, co: co, worktree: worktree, store: store}
}

// TestJobOwnLookupAnchorOnly: the anchor alone resolves the job. From the
// job worktree's cwd the read subcommands and the note/checkpoint writes
// succeed against the starter store although the caller's job_repos_root
// does not contain the checkout; from a cwd outside the job's directory
// the same id is not_found and the lookup writes nothing anywhere.
func TestJobOwnLookupAnchorOnly(t *testing.T) {
	o := newOwnFix(t, false)

	// The starter job's status line before the note appends the first
	// event.
	const wantStatus = `{"id":"job-9","status":"running","motivo":null,"eventos":{"total":0,"ultimo_seq":0},"decisions_acked_seq":0,"decisions_pendentes":[],"partial":false,"partial_collaborations":[],"held_controls":[]}`
	// The status line once the note event is stored.
	const wantStatusAfterNote = `{"id":"job-9","status":"running","motivo":null,"eventos":{"total":1,"ultimo_seq":1},"decisions_acked_seq":0,"decisions_pendentes":[],"partial":false,"partial_collaborations":[],"held_controls":[]}`

	t.Run("status from the worktree cwd", func(t *testing.T) {
		t.Chdir(o.worktree)
		code, out, errOut := o.f.runEnv(t, o.env, "status", "--id", ownJobID)
		// Rule pinned: locateOwnJob gathers the own-checkout anchor on
		// every lookup, not only on a root-scan miss. The root scan finds
		// no store (the checkout is outside job_repos_root), so without
		// the anchor this would be not_found with exit 3.
		if code != 0 || out != wantStatus+"\n" || errOut != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
	})

	t.Run("events from the worktree cwd", func(t *testing.T) {
		t.Chdir(o.worktree)
		code, out, errOut := o.f.runEnv(t, o.env, "events", "--id", ownJobID)
		// Same anchor rule for the events stream: the starter store is
		// the one the worktree cwd resolves.
		if code != 0 || out != `{"eventos":"fim","ultimo_seq":0,"estado":"running"}`+"\n" || errOut != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
	})

	t.Run("note appends to the anchor store", func(t *testing.T) {
		t.Chdir(o.worktree)
		code, out, errOut := o.f.runEnv(t, o.env, "note", "--id", ownJobID, "--tipo", "checkpoint", "anchor note")
		// The write goes through the anchor store the lookup returned,
		// not a store under job_repos_root.
		if code != 0 || errOut != "" || !strings.Contains(out, `"seq":1`) || !strings.Contains(out, `"tipo":"checkpoint"`) {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		data, err := os.ReadFile(filepath.Join(o.store, "jobs", ownJobID, "events.jsonl"))
		if err != nil {
			t.Fatalf("starter store events: %v", err)
		}
		if !strings.Contains(string(data), `"tipo":"checkpoint"`) || !strings.Contains(string(data), `"resumo":"anchor note"`) {
			t.Fatalf("starter store events = %s", data)
		}
		// The root-scan side of the machine holds nothing: no checkout or
		// store was created under job_repos_root by the anchor lookup.
		if _, err := os.Stat(o.f.repos); !os.IsNotExist(err) {
			t.Fatalf("job_repos_root gained entries: %v", err)
		}
	})

	t.Run("events show the appended note", func(t *testing.T) {
		t.Chdir(o.worktree)
		code, out, errOut := o.f.runEnv(t, o.env, "events", "--id", ownJobID)
		// The note event is read back from the starter store's stream.
		if code != 0 || errOut != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
		if len(lines) != 2 || !strings.Contains(lines[0], `"seq":1`) || !strings.Contains(lines[0], `"resumo":"anchor note"`) ||
			lines[1] != `{"eventos":"fim","ultimo_seq":1,"estado":"running"}` {
			t.Fatalf("lines=%q", lines)
		}
	})

	t.Run("checkpoint queues on the anchor store", func(t *testing.T) {
		t.Chdir(o.worktree)
		code, out, errOut := o.f.runEnv(t, o.env, "checkpoint", "--id", ownJobID)
		// The control marker is written by the anchor store the lookup
		// returned; the status line is printed after the queue.
		if code != 0 || out != wantStatusAfterNote+"\n" || errOut != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		marker := filepath.Join(o.store, "jobs", ownJobID, "control", "checkpoint")
		info, err := os.Stat(marker)
		if err != nil || info.Size() != 0 {
			t.Fatalf("checkpoint marker: %v err=%v", info, err)
		}
	})

	t.Run("the same id from a cwd outside the job directory is not_found", func(t *testing.T) {
		before := treeSnapshot(t, o.f.root)
		t.Chdir(o.f.root)
		code, out, errOut := o.f.runEnv(t, o.env, "status", "--id", ownJobID)
		// Rule pinned: the anchor requires the cwd to lie in the job's
		// own worktree top, and the root scan holds no store for the id,
		// so the lookup is not_found — and it is write-free: nothing on
		// the fixture changes, no store is created anywhere.
		if code != 3 || out != `{"status":"not_found"}`+"\n" || errOut != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		assertJobTreeUnchanged(t, before, treeSnapshot(t, o.f.root), "the not_found lookup wrote a file")
	})
}

// TestJobOwnLookupSameStoreBothWays: the starter checkout also lives under
// job_repos_root, so the root scan and the anchor both hit the same store;
// the lookup dedupes by the store root's real path and returns the single
// store (exit 0), never the "more than one checkout" refusal.
func TestJobOwnLookupSameStoreBothWays(t *testing.T) {
	o := newOwnFix(t, true)

	t.Run("the root scan really sees the store", func(t *testing.T) {
		t.Chdir(o.worktree)
		code, out, errOut := o.f.runEnv(t, o.env, "list")
		// Control: the root scan counts the starter job, so the exit-0
		// resolutions below come from a deduped root hit plus anchor hit,
		// not from the anchor alone.
		if code != 0 || out != `{"total":1,"por_estado":{"running":1}}`+"\n" || errOut != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
	})

	t.Run("the direct worktree cwd resolves the one store", func(t *testing.T) {
		t.Chdir(o.worktree)
		code, out, errOut := o.f.runEnv(t, o.env, "status", "--id", ownJobID)
		// Rule pinned: root-scan hits plus the anchor are merged by the
		// store root's real path (storeIdentity). The scan's root and the
		// anchor's root name the same directory, so the id is not "more
		// than one checkout": without the real-path dedup this exits 2.
		if code != 0 || !strings.Contains(out, `"id":"job-9","status":"running"`) || errOut != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
	})

	t.Run("a symlinked view of the worktree resolves the same store", func(t *testing.T) {
		linkDir := filepath.Join(o.f.root, "link")
		if err := os.MkdirAll(linkDir, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(linkDir, "starter org")
		if err := os.Symlink(o.co, link); err != nil {
			t.Fatal(err)
		}
		t.Chdir(filepath.Join(link, ".worktrees", "job-"+ownJobID))
		code, out, errOut := o.f.runEnv(t, o.env, "status", "--id", ownJobID)
		// Rule pinned: the anchor resolves its git output, the cwd and
		// the state.json fields by real path, so a cwd reached through a
		// symlink still names the same store the root scan hits, and the
		// real-path dedup keeps it one.
		if code != 0 || !strings.Contains(out, `"id":"job-9","status":"running"`) || errOut != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
	})
}

// TestJobOwnLookupAmbiguityRefuses: a distinct root-scan store under
// job_repos_root holds the same id as the anchor's starter store; every
// --id subcommand refuses with exit 2 before reading or writing either
// store.
func TestJobOwnLookupAmbiguityRefuses(t *testing.T) {
	o := newOwnFix(t, false)

	// A distinct store under job_repos_root holding the same id: the root
	// scan hits it, the anchor hits the starter store.
	co2 := o.f.checkout(t, "example-org", "example-repo")
	brief := strings.Replace(jobStartBrief, `"id":"job-1"`, `"id":"job-9"`, 1)
	if _, err := o.f.store(t, co2).Start(ownJobID, []byte(brief)); err != nil {
		t.Fatal(err)
	}
	// Both bodies would otherwise be accepted (under the 64/16 KiB caps,
	// and both jobs non-terminal), so the refusal below is the lookup's,
	// not a body validation's.
	bodyDir := t.TempDir()
	amendBody := filepath.Join(bodyDir, "amend.md")
	if err := os.WriteFile(amendBody, []byte("amend body\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sendBody := filepath.Join(bodyDir, "send.md")
	if err := os.WriteFile(sendBody, []byte("send note\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The starter job is running and the root-scan job is accepted, so
	// each subcommand would exit 0 and write into whichever store won the
	// lookup; the refusal must beat that.
	before := treeSnapshot(t, o.f.root)
	t.Chdir(o.worktree)
	for _, args := range [][]string{
		{"status", "--id", ownJobID},
		{"wait", "--id", ownJobID, "--timeout", "0"},
		{"events", "--id", ownJobID},
		{"collect", "--id", ownJobID},
		{"ack", "--id", ownJobID, "--upto", "1"},
		{"close", "--id", ownJobID},
		{"note", "--id", ownJobID, "--tipo", "checkpoint", "checkpoint note"},
		{"amend", "--id", ownJobID, amendBody},
		{"send", "--id", ownJobID, sendBody},
		{"cancel", "--id", ownJobID},
		{"checkpoint", "--id", ownJobID},
	} {
		code, out, errOut := o.f.runEnv(t, o.env, args...)
		// Rule pinned: the anchor is gathered even on a root hit, and
		// two distinct store roots refuse with the fixed message, exit 2
		// and empty stdout, before the subcommand changes anything: the
		// lookup itself only reads (the checkouts' configuration, git and
		// the anchor's state.json), so the refusal leaves both stores
		// unchanged.
		if code != 2 || out != "" || !strings.Contains(errOut, "job: id "+ownJobID+" exists in more than one checkout") {
			t.Fatalf("%v code=%d out=%q err=%q", args, code, out, errOut)
		}
	}
	// No control file, event, report or lock in either store (or anywhere
	// else on the fixture): the refusal lands before every write.
	assertJobTreeUnchanged(t, before, treeSnapshot(t, o.f.root), "the ambiguous lookup changed the trees")

	t.Run("outside the worktree the id resolves to the root store alone", func(t *testing.T) {
		t.Chdir(o.f.root)
		code, out, errOut := o.f.runEnv(t, o.env, "status", "--id", ownJobID)
		// Control: with the cwd outside every repository the anchor is
		// nil and the root scan holds the id once, so the lookup
		// resolves (exit 0, the accepted root-scan job) — the exit 2
		// above is the anchor merge on top of the root hit, not an
		// ambiguity inside the root scan.
		if code != 0 || !strings.Contains(out, `"id":"job-9","status":"accepted"`) || errOut != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
	})
}

// TestJobOwnLookupUnknownIdInWorktree: a different valid id from the job
// worktree's cwd is not_found: the anchor matches only the job whose
// recorded worktree top (<checkout>/.worktrees/job-<id>) the cwd lies in,
// never another job of the same repository — even one whose state.json
// claims this worktree.
func TestJobOwnLookupUnknownIdInWorktree(t *testing.T) {
	o := newOwnFix(t, false)
	t.Chdir(o.worktree)
	for _, args := range [][]string{
		{"status", "--id", "job-8"},
		{"collect", "--id", "job-8"},
	} {
		code, out, errOut := o.f.runEnv(t, o.env, args...)
		// Rule pinned: the worktree mode requires the measured top to be
		// <checkout>/.worktrees/job-<id> for the looked-up id; for job-8
		// that check fails, the anchor yields nil, and no root-scan store
		// holds job-8 either — the job-9 store in the same repository is
		// never substituted.
		if code != 3 || out != `{"status":"not_found"}`+"\n" || errOut != "" {
			t.Fatalf("%v code=%d out=%q err=%q", args, code, out, errOut)
		}
	}

	// A state.json for another id that passes every anchor state check
	// (schema, id, the measured checkout and job directory, the measured
	// mode) while claiming this worktree is still not the anchor of that
	// id: the top's own name is the invariant that separates the jobs.
	job8Dir := filepath.Join(o.store, "jobs", "job-8")
	if err := os.MkdirAll(job8Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	claim := fmt.Sprintf(`{"schema":1,"id":"job-8","status":"running","brief_sha256":"x","decisions_acked_seq":0,"motivo":null,"checkout":%q,"dir":%q,"modo":"worktree"}`+"\n", o.co, o.worktree)
	if err := os.WriteFile(filepath.Join(job8Dir, "state.json"), []byte(claim), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"status", "--id", "job-8"},
		{"collect", "--id", "job-8"},
	} {
		code, out, errOut := o.f.runEnv(t, o.env, args...)
		// Rule pinned: the worktree-mode check requires the measured top
		// to be <checkout>/.worktrees/job-<id>; the claimed state.json
		// cannot anchor job-8 from job-9's worktree, so the lookup stays
		// not_found and the claiming store is never substituted for the
		// worktree's own job.
		if code != 3 || out != `{"status":"not_found"}`+"\n" || errOut != "" {
			t.Fatalf("%v code=%d out=%q err=%q", args, code, out, errOut)
		}
	}
}

// TestJobOwnLookupRetainedAfterWorkspaceClosed: the anchor is a
// structural lookup from the cwd alone; after the job's state says the
// workspace is closed and with no HERDR_ENV in the env, a status from
// the worktree's cwd still finds the job — the anchor neither consults a
// live Herdr nor reopens the workspace.
func TestJobOwnLookupRetainedAfterWorkspaceClosed(t *testing.T) {
	o := newOwnFix(t, false)
	// The job's state now says the workspace is closed.
	jobDir := filepath.Join(o.store, "jobs", ownJobID)
	body := fmt.Sprintf(`{"schema":1,"id":%q,"status":"canceled","brief_sha256":"x","decisions_acked_seq":0,"motivo":null,"checkout":%q,"dir":%q,"modo":"worktree","workspace_closed":true}`+"\n", ownJobID, o.co, o.worktree)
	if err := os.WriteFile(filepath.Join(jobDir, "state.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// o.env carries no HERDR_ENV: the lookup must not need a live Herdr.
	t.Chdir(o.worktree)
	code, out, errOut := o.f.runEnv(t, o.env, "status", "--id", ownJobID)
	// Rule pinned: the anchor resolves the job from the worktree cwd
	// alone; workspace_closed is not part of its checks and no Herdr
	// workspace is consulted, so the closed job still resolves with
	// exit 0.
	if code != 0 || !strings.Contains(out, `"id":"job-9","status":"canceled"`) || errOut != "" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
}

// TestJobOwnLookupListAndStartDryRunIgnoreTheAnchor: job list counts only
// the root-scan stores, and the start's --dry-run plan is built without
// the anchor: from the worktree cwd the anchor's job stays invisible to
// both.
func TestJobOwnLookupListAndStartDryRunIgnoreTheAnchor(t *testing.T) {
	o := newOwnFix(t, false)
	o.f.setMachine(t)
	o.f.setTeam(t)
	// The only root-scan job: job-1, accepted.
	co2 := o.f.checkout(t, "example-org", "example-repo")
	o.f.startJob(t, co2)

	t.Run("list counts only the root-scan store", func(t *testing.T) {
		t.Chdir(o.worktree)
		code, out, errOut := o.f.runEnv(t, o.env, "list")
		// Rule pinned: list resolves its stores with allJobStores (the
		// root scan) and never with the anchor: the running job-9 of the
		// starter store, which the worktree cwd would anchor, is not
		// counted. If the anchor leaked into list, total would be 2.
		if code != 0 || out != `{"total":1,"por_estado":{"accepted":1}}`+"\n" || errOut != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
	})

	t.Run("start --dry-run plans without the anchor", func(t *testing.T) {
		before := treeSnapshot(t, o.store)
		t.Chdir(o.worktree)
		brief := o.f.briefFile(t, strings.Replace(jobStartBrief, `"id":"job-1"`, `"id":"job-9"`, 1))
		code, out, errOut := o.f.runEnv(t, o.env, "start", "--id", ownJobID,
			"--repo", "example-org/example-repo", "--brief", brief, "--dry-run")
		// Rule pinned: the start does not merge the anchor into its
		// lookup. If it did, the starter store would already hold job-9
		// and the start would take the duplicate path (that job's status
		// line) instead of printing the plan; the dry run writes no job
		// state, so the starter store stands untouched.
		if code != 0 || errOut != "" || !strings.Contains(out, `"status":"dry_run"`) || !strings.Contains(out, `"id":"job-9"`) {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		assertJobTreeUnchanged(t, before, treeSnapshot(t, o.store), "the dry run touched the anchor store")
	})
}
