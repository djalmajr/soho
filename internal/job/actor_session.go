package job

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// actor_session.go makes the job's agents resolve the job's team and state
// root. The job start writes the team to <job store root>/<workspace>/
// session.conf, and the supervisor's team commands read it through the
// starter's selection. A job orchestrator, or a worker it spawns, runs with
// the workspace's own environment (the supervisor's original one): its user
// layer is that environment's, and it looks its session file up under the
// state root that environment resolves before any session is read.
//
// So a privately launched supervisor, after the job lane and before the
// orchestrator spawn:
//
//  1. snapshots into the start's session file the team settings the
//     starter's selection resolves for every role of the team's lanes
//     (kind, model and effort, on the lane or role keys that already
//     exist) and max_workers, where the session does not already decide
//     them: resolved values, so the layer rules (a lower layer's model is
//     discarded under a higher layer's kind) apply before the snapshot,
//     not after. When the agents' own non-session configuration layers
//     set a team lane's kind and every role of that lane resolves the
//     same kind for the starter, the lane's kind is pinned too: a
//     session-layer lane kind lifts the agents' kind layer, so their
//     own lower-layer lane model and effort no longer outrank the
//     session's role pins. Approvals are permission policy, not team
//     selection:
//     only the team file's own (already in the session) reach the agents;
//     an agent's other approvals stay its own configuration's;
//  2. when the workspace environment's session file is another file (a
//     custom state_dir on either side), writes it as that session plus
//     state_dir="<job store root>": the existing key moves the agents'
//     state root, and with it their roster, team commands and job lookup;
//  3. resolves both views again, the starter's and the workspace
//     environment's, and requires the same team and state root.
//
// Any failure is errActorSession, before an agent is spawned. No other key
// (no approvals, auto-approval, args, credentials, machine policy or
// unrelated setting), file kind or environment value is involved.
//
// Writes are confined (confinedDir): the nearest existing ancestor of a
// state root is trusted as the configuration names it (resolved, so an
// alias such as a linked home or temp directory stays usable); the levels
// the pin creates, the state root, the workspace directory and the session
// file are opened through an os.Root at that ancestor, are never links or
// other kinds of files, and cannot leave it.

// maxSessionBytes caps the session files the pin reads.
const maxSessionBytes = 64 << 10

// maxExcludeBytes caps the info/exclude file the pin reads.
const maxExcludeBytes = 1 << 20

// errActorSession is the fixed failure of a pin the job needs but cannot
// make; the reason goes to friction.
var errActorSession = errUsage("job: cannot pin the job workspace session")

// teamLaneRolesRE is a team lane's roles key (the job lane is the
// supervisor's own and is not part of the team).
var teamLaneRolesRE = regexp.MustCompile(`^lane\.([a-z][a-z0-9_-]*)\.roles$`)

// teamView is what an agent's spawn resolves for the team: per role its
// lane, kind, model and effort, and max_workers.
type teamView struct {
	roles      map[string]core.RoleSettings
	maxWorkers string
}

func (v teamView) equal(o teamView) bool { return v.diff(o) == "" }

// diff names the first setting v and o resolve differently ("" when none):
// the role and attribute, never a value.
func (v teamView) diff(o teamView) string {
	if v.maxWorkers != o.maxWorkers {
		return "max_workers"
	}
	roles := make([]string, 0, len(v.roles))
	for role := range v.roles {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	if len(v.roles) != len(o.roles) {
		return "roles"
	}
	for _, role := range roles {
		a := v.roles[role]
		b, ok := o.roles[role]
		switch {
		case !ok:
			return "role " + role
		case a.Lane != b.Lane:
			return "role " + role + " lane"
		case a.Kind != b.Kind:
			return "role " + role + " kind"
		case a.ModelSpec != b.ModelSpec || !slices.Equal(a.LaneModels, b.LaneModels):
			return "role " + role + " model"
		case a.Effort != b.Effort:
			return "role " + role + " effort"
		}
	}
	return ""
}

func resolveTeamView(roles []string, ctx *core.Config, env platform.Env, dir string) teamView {
	view := teamView{roles: map[string]core.RoleSettings{}, maxWorkers: core.Cfg(ctx, "max_workers", "", env)}
	for _, role := range roles {
		view.roles[role] = core.ResolveRoleSettings(role, ctx, env, dir, core.RoleFlags{})
	}
	return view
}

// pinActorSession runs steps 1 to 3 above; a supervisor without the
// private launch has nothing to pin and returns nil.
func (s *Supervisor) pinActorSession() error {
	if !s.PrivateLaunch || s.actorEnv == nil {
		return nil
	}
	snap, err := s.Store.Snapshot(s.ID)
	if err != nil || snap.State.Dir == "" || !validWorkspaceID(snap.State.WorkspaceID) {
		s.frictionf("job: the job workspace session cannot be located")
		return errActorSession
	}
	workspace, dir := snap.State.WorkspaceID, snap.State.Dir
	data, ok, err := readPrivateFile(filepath.Join(s.Store.Root, workspace, "session.conf"), maxSessionBytes)
	if err != nil || !ok {
		s.frictionf("job: the job session file is unreadable")
		return errActorSession
	}
	pairs := core.ConfigTextPairs(data)
	roles := teamRoles(pairs)
	// The environment of an agent in the job workspace: the supervisor's
	// own, with the job workspace's id (the session layer's key).
	agentEnv := s.actorEnv.Clone()
	agentEnv["HERDR_WORKSPACE_ID"] = workspace

	// 1. The starter's team, resolved, into the start's session file.
	starter := core.LoadConfigFor(agentEnv, dir, s.launchSel)
	if !sameEntryPath(core.StateRootPath(&starter, agentEnv, dir), s.Store.Root) {
		s.frictionf("job: the starter's selection does not resolve the job state root")
		return errActorSession
	}
	want := resolveTeamView(roles, &starter, agentEnv, dir)
	agentCfg := core.LoadConfigThrough(agentEnv, dir, "project")
	team := teamSessionView(pairs, roles, &starter, agentEnv, dir)
	if conflicts := actorGroupConflicts(roles, &agentCfg, team); len(conflicts) != 0 {
		for _, conflict := range conflicts {
			s.frictionf(conflict)
		}
		return errActorSession
	}
	body := appendSessionLines(string(data), teamPins(pairs, want, agentLaneKinds(&agentCfg, want)))
	if body != string(data) {
		if !s.writeSessionFile(s.Store.Root, workspace, body, false) {
			return errActorSession
		}
		again := core.LoadConfigFor(agentEnv, dir, s.launchSel)
		if d := resolveTeamView(roles, &again, agentEnv, dir).diff(want); d != "" {
			s.frictionf("job: the team snapshot would change the supervisor's team (" + d + ")")
			return errActorSession
		}
	}

	// 2. The workspace environment's own session file.
	actorRoot := core.SessionStateRootPath(s.actorEnv, dir)
	if !sameEntryPath(actorRoot, s.Store.Root) {
		if !sessionStateDirValue(s.Store.Root) {
			s.frictionf("job: the job state root cannot be written in a session file (an absolute path without '#' or line breaks)")
			return errActorSession
		}
		if core.StatePathInSkill(s.actorEnv, actorRoot) != "" {
			s.frictionf("job: the job workspace session would be inside the skill")
			return errActorSession
		}
		if !s.pinExclude(actorRoot, workspace, dir) {
			return errActorSession
		}
		if !s.writeSessionFile(actorRoot, workspace, pinnedSessionBody(body, s.Store.Root), true) {
			return errActorSession
		}
	}

	// 3. What the agents will resolve.
	actor := core.LoadConfig(agentEnv, dir)
	if !sameEntryPath(core.StateRootPath(&actor, agentEnv, dir), s.Store.Root) {
		s.frictionf("job: the job's agents would not resolve the job state root")
		return errActorSession
	}
	if d := resolveTeamView(roles, &actor, agentEnv, dir).diff(want); d != "" {
		s.frictionf("job: the job's agents would not resolve the job team (" + d + ")")
		return errActorSession
	}
	return nil
}

// teamRoles are the roles of the session's team lanes (every lane but the
// job lane), in order, once each.
func teamRoles(pairs []core.ConfigPair) []string {
	var roles []string
	seen := map[string]bool{}
	for _, pair := range pairs {
		m := teamLaneRolesRE.FindStringSubmatch(pair.Original)
		if m == nil || m[1] == "job" {
			continue
		}
		for _, role := range strings.Split(pair.Value, ",") {
			role = strings.TrimSpace(role)
			if role != "" && !seen[role] {
				seen[role] = true
				roles = append(roles, role)
			}
		}
	}
	return roles
}

// agentLaneKinds reports the lanes of view's roles whose kind the
// agents' own non-session configuration layers set: a layer that is not
// the session and read without an error, with a non-empty entry under
// the lane's kind key. Those are the lanes step 1 can anchor on their
// lane kind (teamPins), preserving the agreed team kind at group scope.
func agentLaneKinds(ctx *core.Config, view teamView) map[string]bool {
	set := map[string]bool{}
	for _, rs := range view.roles {
		if rs.Lane == "" || set[rs.Lane] {
			continue
		}
		key := core.LaneKey(rs.Lane, "kind")
		for _, layer := range ctx.Layers {
			if layer.Source == "session" || layer.ReadError != nil {
				continue
			}
			if e, ok := layer.Entries[key]; ok && e.Value != "" {
				set[rs.Lane] = true
				break
			}
		}
	}
	return set
}

// teamPins fills settings left open by the selected session. Group values
// are written on group keys and other values on role keys. A common lane
// kind is also pinned when an actor's base configuration supplies one.
func teamPins(pairs []core.ConfigPair, view teamView, agentKinds map[string]bool) []string {
	inSession := map[string]bool{}
	for _, pair := range pairs {
		inSession[pair.Original] = true
	}
	var lines []string
	add := func(key, value string) {
		if value == "" || inSession[key] {
			return
		}
		inSession[key] = true
		lines = append(lines, key+"="+value)
	}
	roles := make([]string, 0, len(view.roles))
	for role := range view.roles {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	for _, role := range roles {
		rs := view.roles[role]
		for _, attr := range []struct{ name, value, from string }{
			{"kind", rs.Kind, rs.KindFrom},
			{"model", rs.ModelSpec, rs.ModelFrom},
			{"effort", rs.Effort, rs.EffortFrom},
		} {
			if strings.HasSuffix(attr.from, "(session)") || attr.from == "flag" {
				continue
			}
			if rs.Lane != "" && strings.HasPrefix(attr.from, "lane ") {
				add("lane."+rs.Lane+"."+attr.name, attr.value)
				continue
			}
			add("role."+role+"."+attr.name, attr.value)
		}
	}
	// The lanes the agents' own non-session configuration layers set the
	// kind of, in sorted lane order: pinned only when the lane's roles
	// all resolve the same non-empty kind (a lane whose roles resolve
	// different kinds is left to step 3's refusal).
	lanes := make([]string, 0, len(agentKinds))
	for lane := range agentKinds {
		lanes = append(lanes, lane)
	}
	sort.Strings(lanes)
	for _, lane := range lanes {
		kind := ""
		conflict := false
		for _, role := range roles {
			rs := view.roles[role]
			if rs.Lane != lane {
				continue
			}
			if rs.Kind == "" || (kind != "" && rs.Kind != kind) {
				conflict = true
				break
			}
			kind = rs.Kind
		}
		if !conflict {
			add("lane."+lane+".kind", kind)
		}
	}
	add("max_workers", view.maxWorkers)
	return lines
}

// appendSessionLines is text with lines appended, one per line.
func appendSessionLines(text string, lines []string) string {
	if len(lines) == 0 {
		return text
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text + strings.Join(lines, "\n") + "\n"
}

// pinnedSessionBody is the session text without any state_dir line, plus
// state_dir="<root>" as its last line: the config parser strips one pair
// of quotes, so spaces at either end and quotes inside the path survive.
func pinnedSessionBody(text, root string) string {
	var lines []string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if key, _, found := strings.Cut(line, "="); found && core.NormalizeKey(key) == "state_dir" {
			continue
		}
		lines = append(lines, line)
	}
	lines = append(lines, `state_dir="`+root+`"`)
	return strings.Join(lines, "\n") + "\n"
}

// sessionStateDirValue reports whether root can be one session file line:
// absolute, without '#' (the parser cuts a comment there, quoted or not)
// and without line breaks.
func sessionStateDirValue(root string) bool {
	return filepath.IsAbs(root) && !strings.ContainsAny(root, "#\n\r")
}

// confinedDir is <state root>/<workspace> as the pin reaches it: trusted
// is the real path of the state root's nearest existing proper ancestor,
// and rel the levels below it, the state root's own last and the
// workspace's after it.
type confinedDir struct {
	trusted string
	rel     []string
}

// confine resolves root/workspace into a confinedDir.
func confine(root, workspace string) (confinedDir, bool) {
	if !filepath.IsAbs(root) || !validWorkspaceID(workspace) {
		return confinedDir{}, false
	}
	root = filepath.Clean(root)
	base := filepath.Dir(root)
	var rest []string
	for {
		if _, err := os.Lstat(base); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return confinedDir{}, false
		}
		rest = append([]string{filepath.Base(base)}, rest...)
		next := filepath.Dir(base)
		if next == base {
			return confinedDir{}, false
		}
		base = next
	}
	trusted, err := filepath.EvalSymlinks(base)
	if err != nil {
		return confinedDir{}, false
	}
	if info, err := os.Stat(trusted); err != nil || !info.IsDir() {
		return confinedDir{}, false
	}
	return confinedDir{trusted: trusted, rel: append(rest, filepath.Base(root), workspace)}, true
}

// path is the directory's path below its trusted ancestor.
func (c confinedDir) path() string {
	return filepath.Join(append([]string{c.trusted}, c.rel...)...)
}

// open opens the directory, creating its missing levels when create is
// set: every refusal comes first, before anything is created (an existing
// level must be a plain directory at its own path, and with check an
// existing name in the directory a regular file at its own path); missing
// levels are then created with Mkdir through the os.Root, one at a time,
// and checked again. The returned os.Root is the directory's.
func (c confinedDir) open(create bool, check string) (*os.Root, bool) {
	top, err := os.OpenRoot(c.trusted)
	if err != nil {
		return nil, false
	}
	defer top.Close()
	missing := len(c.rel)
	for i := range c.rel {
		info, err := top.Lstat(filepath.Join(c.rel[:i+1]...))
		if os.IsNotExist(err) {
			missing = i
			break
		}
		if err != nil || info.Mode().Type() != os.ModeDir {
			return nil, false
		}
	}
	if missing == len(c.rel) && check != "" {
		info, err := top.Lstat(filepath.Join(append(append([]string{}, c.rel...), check)...))
		if err != nil && !os.IsNotExist(err) || err == nil && !info.Mode().IsRegular() {
			return nil, false
		}
	}
	if missing < len(c.rel) && !create {
		return nil, false
	}
	for i := missing; i < len(c.rel); i++ {
		level := filepath.Join(c.rel[:i+1]...)
		if err := top.Mkdir(level, 0o700); err != nil && !os.IsExist(err) {
			return nil, false
		}
		info, err := top.Lstat(level)
		if err != nil || info.Mode().Type() != os.ModeDir {
			return nil, false
		}
	}
	dir, err := top.OpenRoot(filepath.Join(c.rel...))
	if err != nil {
		return nil, false
	}
	return dir, true
}

// pinExclude adds the git-local exclude line the workspace session at
// <actorRoot>/<ws> needs, before it is written. Outside the checkout, or
// under the checkout's state directory, .soho or the legacy .herdr-soho
// (the job prepare excludes both), none;
// under the checkout's .worktrees it is refused (a worktree has its own
// root, which a checkout exclude line does not reach); elsewhere in the
// checkout the line /<path from the checkout>/<ws>/ goes to the checkout's
// .git/info/exclude, refused when git already tracks a file there or the
// path holds a gitignore pattern character. Inside or outside is decided
// on the real location (confine) and by file identity.
func (s *Supervisor) pinExclude(actorRoot, workspace, dir string) bool {
	c, ok := confine(actorRoot, workspace)
	if !ok {
		s.frictionf("job: the job workspace session path cannot be resolved")
		return false
	}
	checkout, err := filepath.EvalSymlinks(platform.StateProjectRoot(s.actorEnv, dir))
	if err != nil {
		s.frictionf("job: the job checkout cannot be resolved")
		return false
	}
	// The trusted ancestor's levels below the checkout, by identity.
	var above []string
	inside := false
	for p := c.trusted; ; {
		if sameEntryPath(p, checkout) {
			inside = true
			break
		}
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
		above = append([]string{filepath.Base(p)}, above...)
		p = parent
	}
	if !inside {
		return true
	}
	rel := append(above, c.rel...)
	first := filepath.Join(checkout, rel[0])
	// Both state directory names are excluded by the job prepare.
	if rel[0] == ".soho" || rel[0] == ".herdr-soho" || sameEntryPath(first, filepath.Join(checkout, ".soho")) || sameEntryPath(first, filepath.Join(checkout, ".herdr-soho")) {
		return true
	}
	if rel[0] == ".worktrees" || sameEntryPath(first, filepath.Join(checkout, ".worktrees")) {
		s.frictionf("job: the job workspace session would be inside a job worktree")
		return false
	}
	line := "/" + filepath.ToSlash(filepath.Join(rel...)) + "/"
	if strings.ContainsAny(line, "*?[]\\#!\n\r") || strings.TrimSpace(line) != line {
		s.frictionf("job: the job workspace session path cannot be excluded from git")
		return false
	}
	out, ok := boundedGit(s.actorEnv, checkout, "ls-files", "-z", "--", strings.Trim(line, "/"))
	if !ok || out != "" {
		s.frictionf("job: the job workspace session path is tracked by git or cannot be checked")
		return false
	}
	if !appendExclude(checkout, line) {
		s.frictionf("job: the checkout's local git exclude file cannot be updated")
		return false
	}
	return true
}

// appendExclude adds line to <checkout>/.git/info/exclude unless it is
// there, through an os.Root at the checkout: .git and info are plain
// directories at their own paths (info created when missing); a missing
// exclude file is created exclusively; an existing one is opened
// read-write without blocking, and only a regular file that is the entry
// the path named before the open and at most maxExcludeBytes is read,
// appended to and read back through that one handle.
func appendExclude(checkout, line string) bool {
	top, err := os.OpenRoot(checkout)
	if err != nil {
		return false
	}
	defer top.Close()
	info := filepath.Join(".git", "info")
	file := filepath.Join(info, "exclude")
	gitInfo, err := top.Lstat(".git")
	if err != nil || gitInfo.Mode().Type() != os.ModeDir {
		return false
	}
	infoInfo, err := top.Lstat(info)
	if err != nil && !os.IsNotExist(err) || err == nil && infoInfo.Mode().Type() != os.ModeDir {
		return false
	}
	pre, err := top.Lstat(file)
	if err != nil && !os.IsNotExist(err) || err == nil && (!pre.Mode().IsRegular() || pre.Size() > maxExcludeBytes) {
		return false
	}
	if infoInfo == nil {
		if err := top.Mkdir(info, 0o755); err != nil && !os.IsExist(err) {
			return false
		}
		if again, err := top.Lstat(info); err != nil || again.Mode().Type() != os.ModeDir {
			return false
		}
	}
	if pre == nil {
		// Another job in the same checkout may create the file first: that
		// is not a failure, the line then goes through the append below.
		// The creator appends too: another job can add its line between
		// this creation and this first write.
		f, err := top.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, 0o644)
		switch {
		case err == nil:
			_, werr := f.WriteString(line + "\n")
			if cerr := f.Close(); werr != nil || cerr != nil {
				return false
			}
		case !os.IsExist(err):
			return false
		}
		pre, err = top.Lstat(file)
		if err != nil || !pre.Mode().IsRegular() || pre.Size() > maxExcludeBytes {
			return false
		}
	}
	// O_APPEND makes every write land at the current end of the file, so
	// jobs that share the checkout never overwrite each other's lines: a
	// seek to the end followed by a separate write could.
	f, err := top.OpenFile(file, platform.ReadWriteNonBlocking|os.O_APPEND, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(pre, opened) || opened.Size() > maxExcludeBytes {
		return false
	}
	existing, err := io.ReadAll(io.LimitReader(f, maxExcludeBytes+1))
	if err != nil || len(existing) > maxExcludeBytes {
		return false
	}
	if excludeHas(existing, line) {
		return true
	}
	add := line + "\n"
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		add = "\n" + add
	}
	if _, err := f.WriteString(add); err != nil {
		return false
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false
	}
	back, err := io.ReadAll(io.LimitReader(f, maxExcludeBytes+int64(len(add))+1))
	return err == nil && excludeHas(back, line)
}

// excludeHas reports whether data holds line as one of its lines.
func excludeHas(data []byte, line string) bool {
	for _, have := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if strings.TrimSpace(have) == line {
			return true
		}
	}
	return false
}

// writeSessionFile writes body to <root>/<workspace>/session.conf through
// the directory's os.Root (confinedDir.open: refusals first, then the
// missing levels): a fresh temporary file created exclusively, written,
// synced and renamed over session.conf inside the directory; then read
// back through one handle, opened without blocking, that is the regular
// file the name holds, at most maxSessionBytes, as the same bytes (for the
// workspace pin, pinsRoot, bytes whose state_dir the config parser reads
// as the job store root).
func (s *Supervisor) writeSessionFile(root, workspace, body string, pinsRoot bool) bool {
	fail := func(reason string) bool {
		s.frictionf("job: " + reason)
		return false
	}
	c, ok := confine(root, workspace)
	if !ok {
		return fail("the job workspace session path cannot be resolved")
	}
	dir, ok := c.open(true, "session.conf")
	if !ok {
		return fail("the job workspace session path holds a link or another kind of file")
	}
	defer dir.Close()
	tmp := fmt.Sprintf(".session.conf-%d-%d.tmp", os.Getpid(), time.Now().UnixNano())
	f, err := dir.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fail("the job workspace session cannot be written")
	}
	_, werr := f.WriteString(body)
	serr := f.Sync()
	if cerr := f.Close(); werr != nil || serr != nil || cerr != nil {
		_ = dir.Remove(tmp)
		return fail("the job workspace session cannot be written")
	}
	if err := dir.Rename(tmp, "session.conf"); err != nil {
		_ = dir.Remove(tmp)
		return fail("the job workspace session cannot be written")
	}
	back, ok := readConfined(dir, "session.conf", maxSessionBytes)
	if !ok || string(back) != body {
		return fail("the job workspace session does not read back")
	}
	if value, set := sessionStateDir(back); pinsRoot && (!set || value != s.Store.Root) {
		return fail("the job workspace session does not read back")
	}
	return true
}

// readConfined reads name in dir through one handle opened without
// blocking: only a regular file that is the entry name held before the
// open, of at most max bytes.
func readConfined(dir *os.Root, name string, max int64) ([]byte, bool) {
	pre, err := dir.Lstat(name)
	if err != nil || !pre.Mode().IsRegular() || pre.Size() > max {
		return nil, false
	}
	f, err := dir.OpenFile(name, platform.ReadOnlyNonBlocking, 0)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(pre, opened) {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(data)) > max {
		return nil, false
	}
	return data, true
}

// sessionStateDir is the state_dir the config parser reads from session
// bytes (core.ConfigTextPairs: the parser every layer is read with, over
// the bytes already read; no second open).
func sessionStateDir(data []byte) (string, bool) {
	value, found := "", false
	for _, pair := range core.ConfigTextPairs(data) {
		if pair.Key == "state_dir" {
			value, found = pair.Value, true
		}
	}
	return value, found
}
