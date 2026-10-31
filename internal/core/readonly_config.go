package core

// The explicit read-only configuration seam for the inventory producer
// (DJA-611): a silent, bounded load of a known canonical project root and
// an optional known session, with no terminal lookup, no Git call and no
// state/cache write. The normal loader (LoadConfigFor) keeps its own
// behavior; this API reuses its parser and user selection and shares its
// state-root arithmetic, and it is the only configuration entry point that
// accepts foreign project/session inputs.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/djalmajr/soho/internal/platform"
	skill "github.com/djalmajr/soho/skills/soho"
)

// The read bounds of the read-only seam. Every layer read is confined to
// its bound: an oversized layer is an explicit ReadError, never a silent
// truncation.
const (
	readonlyLayerMax   = 1 << 20  // defaults, user and project layers: 1 MiB
	readonlySessionMax = 64 << 10 // session.conf: 64 KiB
)

// ReadOnlyConfig is the result of LoadReadOnlyConfig. Config carries the
// layers, merged entries and user selection with the normal parser
// semantics. ProjectRoot is the canonical root the config was loaded for
// ("" in bootstrap). BaseStateRoot is the state root resolved from the
// defaults, user and project layers alone, before the session layer: the
// location under which the session file was read. StateRoot is the
// effective state root after the session layer's optional state_dir
// redirect. StateDir is StateRoot/<session> for a supplied session; all
// three state fields are path calculations only, empty in bootstrap.
type ReadOnlyConfig struct {
	Config Config
	// ProjectRoot is the canonical project root ("" in bootstrap).
	ProjectRoot string
	// BaseStateRoot is the pre-session state root ("" in bootstrap).
	BaseStateRoot string
	// StateRoot is the effective post-session state root ("" in bootstrap).
	StateRoot string
	// StateDir is StateRoot/<session> for a supplied session, "" otherwise.
	StateDir string
}

// LoadReadOnlyConfig loads a configuration for an explicitly supplied
// project root and optional session id, for callers that observe a foreign
// project and must not discover one:
//
//   - Empty projectRoot and sessionID is the bootstrap: the defaults layer
//     and the selected user layer only, no project, session or state path
//     and no caller discovery; the state fields of the result are empty.
//   - A supplied project root must be an absolute existing directory; it
//     is canonicalized by resolving filesystem aliases only (no Git) and
//     only THAT root's .agents/soho.conf is the project layer, with no
//     linked-worktree or caller-root fallback.
//   - A nonempty session requires the project root and a
//     platform.ValidStateComponent id, both checked before any I/O; the
//     session file is base-state-root/<id>/session.conf, read at most once.
//     No pane id is parsed and no HERDR/HC marker is manufactured or
//     honored.
//
// Every file read is bounded and handle-confined (see readConfinedFile):
// defaults/user/project at most 1 MiB, session.conf at most 64 KiB.
// Absence of a base or file is an ordinary empty layer; an unsafe,
// unreadable or oversized layer is recorded as that layer's ReadError and
// fails closed, so the pure resolvers (for example the privacy ResolveMode)
// close on it. The defaults layer uses an already existing skill source
// (platform.SkillDirSourceOrEmpty) and, when no source exists, the
// embedded config.defaults through skill.ReadConfigDefaults; it never
// materializes or repairs a cache.
//
// The load mutates nothing: no process environment entry, no file,
// directory, lock, .gitignore or cache publication, and no terminal,
// backend, Git or provider call. It prints nothing; argument and root
// errors are returned, per-layer input failures are recorded as layer
// ReadErrors.
func LoadReadOnlyConfig(env platform.Env, projectRoot, sessionID string, sel UserSelection) (ReadOnlyConfig, error) {
	return loadReadOnlyConfig(env, projectRoot, sessionID, sel, nil)
}

// loadReadOnlyConfig is the per-call load implementation behind
// LoadReadOnlyConfig. barrier is the per-call directory test-coordination
// dependency of readConfinedFile: nil in every production call, where it
// changes nothing. An in-package consumer test passes a bounded pause that
// runs exactly once per existing appended directory, after that directory's
// complete check (pre-entry check, retained open, handle identity,
// post-entry check) and before the next component's check or the final
// file's check, so a replacement made in that window is observed
// deterministically (no timing-only race). It is a per-call argument,
// never a package-global hook, and is never invoked on the final file's
// path.
func loadReadOnlyConfig(env platform.Env, projectRoot, sessionID string, sel UserSelection, barrier func()) (ReadOnlyConfig, error) {
	if sessionID != "" {
		if projectRoot == "" {
			return ReadOnlyConfig{}, errors.New("readonly config: a session id requires a project root")
		}
		if !platform.ValidStateComponent(sessionID) {
			return ReadOnlyConfig{}, errors.New("readonly config: invalid session id")
		}
	}
	var canonical string
	if projectRoot != "" {
		resolved, err := readonlyCanonicalRoot(projectRoot)
		if err != nil {
			return ReadOnlyConfig{}, err
		}
		canonical = resolved
	}

	ctx := Config{Entries: make(map[string]ConfigEntry), Order: make([]string, 0), Sources: make([]string, 0, 4), User: sel}
	loadReadOnlyDefaults(&ctx, env, barrier)
	loadReadOnlyUser(&ctx, env, sel, barrier)

	var baseStateRoot string
	if canonical != "" {
		loadReadOnlyProject(&ctx, canonical, barrier)
		baseStateRoot = StateRootPathForProject(&ctx, env, canonical)
		if sessionID != "" {
			loadReadOnlySession(&ctx, baseStateRoot, sessionID, barrier)
		}
	}
	stateRoot := baseStateRoot
	stateDir := ""
	if canonical != "" {
		// After the session layer its state_dir (or the SOHO_ state
		// environment) may redirect the effective root; the arithmetic is
		// pure and re-reads no file.
		stateRoot = StateRootPathForProject(&ctx, env, canonical)
		if sessionID != "" {
			stateDir = filepath.Join(stateRoot, sessionID)
		}
	}
	return ReadOnlyConfig{Config: ctx, ProjectRoot: canonical, BaseStateRoot: baseStateRoot, StateRoot: stateRoot, StateDir: stateDir}, nil
}

// readonlyCanonicalRoot validates and canonicalizes the explicit project
// root: absolute, existing, a directory after alias resolution, resolved
// without any Git call.
func readonlyCanonicalRoot(projectRoot string) (string, error) {
	if !filepath.IsAbs(projectRoot) {
		return "", errors.New("readonly config: the project root must be an absolute path")
	}
	if _, err := os.Lstat(projectRoot); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("readonly config: the project root does not exist: %s", projectRoot)
		}
		return "", fmt.Errorf("readonly config: the project root cannot be inspected: %s: %v", projectRoot, err)
	}
	resolved, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		return "", fmt.Errorf("readonly config: the project root cannot be canonicalized: %s: %v", projectRoot, err)
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("readonly config: the project root is not a directory: %s", projectRoot)
	}
	return resolved, nil
}

// loadReadOnlyDefaults records the defaults layer: the config.defaults of
// an already existing skill source when SkillDirSourceOrEmpty names one
// (never materializing or repairing a cache), otherwise the embedded
// config.defaults with no filesystem path. A source that cannot be safely
// read is recorded with its ReadError rather than replaced by another
// source.
func loadReadOnlyDefaults(ctx *Config, env platform.Env, barrier func()) {
	if dir, _ := platform.SkillDirSourceOrEmpty(env); dir != "" {
		data, absent, err := readConfinedFile(dir, "config.defaults", readonlyLayerMax, barrier)
		recordReadOnlyLayer(ctx, "defaults", filepath.Join(dir, "config.defaults"), data, absent, err)
		return
	}
	data, err := skill.ReadConfigDefaults()
	if err != nil {
		ctx.Layers = append(ctx.Layers, ConfigLayer{Source: "defaults", File: "", ReadError: err, Entries: map[string]ConfigEntry{}})
		return
	}
	ctx.Sources = append(ctx.Sources, "defaults")
	record := ConfigLayer{Source: "defaults", Entries: make(map[string]ConfigEntry)}
	applyConfigText(platform.DecodeText(data), &record, ctx)
	ctx.Layers = append(ctx.Layers, record)
}

// loadReadOnlyUser records the user layer for the explicit selection, the
// same file the normal loader selects (EffectiveUserConfigFile), read
// confined and bounded.
func loadReadOnlyUser(ctx *Config, env platform.Env, sel UserSelection, barrier func()) {
	file := EffectiveUserConfigFile(platform.Current(), env, sel)
	if absolute, err := filepath.Abs(file); err == nil {
		file = absolute
	}
	data, absent, err := readConfinedFile(filepath.Dir(file), filepath.Base(file), readonlyLayerMax, barrier)
	recordReadOnlyLayer(ctx, "user", file, data, absent, err)
}

// loadReadOnlyProject records the project layer: that root's own
// .agents/soho.conf, confined below the canonical root (no escape, no
// fallback to another checkout).
func loadReadOnlyProject(ctx *Config, canonical string, barrier func()) {
	file := filepath.Join(canonical, ".agents", "soho.conf")
	data, absent, err := readConfinedFile(canonical, ".agents/soho.conf", readonlyLayerMax, barrier)
	recordReadOnlyLayer(ctx, "project", file, data, absent, err)
}

// loadReadOnlySession records the session layer: at most one
// session.conf under the base state root for the explicit id, confined
// below that base. The effective state root it may redirect is computed
// from the recorded layers, never by chasing a second session file.
func loadReadOnlySession(ctx *Config, baseStateRoot, sessionID string, barrier func()) {
	file := filepath.Join(baseStateRoot, sessionID, "session.conf")
	data, absent, err := readConfinedFile(baseStateRoot, filepath.Join(sessionID, "session.conf"), readonlySessionMax, barrier)
	recordReadOnlyLayer(ctx, "session", file, data, absent, err)
}

// recordReadOnlyLayer records one bounded layer read with the normal
// layer semantics: absence is an ordinary empty layer, a failed read is
// the layer's ReadError, a successful read contributes its entries.
func recordReadOnlyLayer(ctx *Config, source, file string, data []byte, absent bool, err error) {
	if absent {
		ctx.Layers = append(ctx.Layers, ConfigLayer{Source: source, File: file, Entries: map[string]ConfigEntry{}})
		return
	}
	if err != nil {
		ctx.Layers = append(ctx.Layers, ConfigLayer{Source: source, File: file, ReadError: err, Entries: map[string]ConfigEntry{}})
		return
	}
	ctx.Sources = append(ctx.Sources, source)
	record := ConfigLayer{Source: source, File: file, Entries: make(map[string]ConfigEntry)}
	applyConfigText(platform.DecodeText(data), &record, ctx)
	ctx.Layers = append(ctx.Layers, record)
}

// readConfinedFile reads one plain regular file of at most max bytes below
// an existing base directory, handle-confined: the base is canonicalized
// (filesystem aliases resolved) and trusted once through os.OpenRoot,
// every appended directory must be a plain directory, acquired through its
// own retained child-root handle whose identity is compared against the
// pre and post parent-entry Lstat (a replacement between them is a
// refusal), the final file must be a plain regular file (no symlink,
// reparse, FIFO or other nonregular kind), every final-file check and the
// bounded (max+1) nonblocking read resolve from the retained parent handle
// — never from the original root plus a full relative path — so a checked
// directory swapped to a symlink after validation cannot redirect the
// read, and the handle's identity and size are compared with the Lstat
// identity before and after the read. A missing base or file reports
// absent; a refused or failed read reports an explicit error the caller
// records as the layer's ReadError.
func readConfinedFile(base, rel string, max int64, barrier func()) (data []byte, absent bool, err error) {
	if !filepath.IsAbs(base) {
		absolute, absErr := filepath.Abs(base)
		if absErr != nil {
			return nil, false, fmt.Errorf("readonly config: %v", absErr)
		}
		base = absolute
	}
	canonical, err := filepath.EvalSymlinks(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("readonly config: cannot canonicalize %s: %v", base, err)
	}
	root, err := os.OpenRoot(canonical)
	if err != nil {
		return nil, false, fmt.Errorf("readonly config: cannot open %s: %v", canonical, err)
	}
	defer func() { _ = root.Close() }()

	rel = filepath.ToSlash(filepath.Clean(rel))
	components := strings.Split(rel, "/")
	opened := []*os.Root{root}
	defer func() {
		for i := len(opened) - 1; i >= 0; i-- {
			_ = opened[i].Close()
		}
	}()
	current := root
	for i := 0; i+1 < len(components); i++ {
		comp := components[i]
		pre, dirErr := current.Lstat(comp)
		if dirErr != nil {
			if os.IsNotExist(dirErr) {
				return nil, true, nil
			}
			return nil, false, fmt.Errorf("readonly config: cannot inspect %s below %s: %v", comp, canonical, dirErr)
		}
		if pre.Mode()&os.ModeSymlink != 0 || !pre.IsDir() {
			return nil, false, fmt.Errorf("readonly config: unsafe: %s below %s is not a plain directory", comp, canonical)
		}
		child, err := openReadOnlyDirectory(current, comp)
		if err != nil {
			return nil, false, fmt.Errorf("readonly config: cannot open %s below %s: %v", comp, canonical, err)
		}
		opened = append(opened, child)
		handle, err := child.Lstat(".")
		if err != nil || !handle.IsDir() || !os.SameFile(pre, handle) {
			return nil, false, fmt.Errorf("readonly config: unsafe: %s below %s changed identity", comp, canonical)
		}
		post, err := current.Lstat(comp)
		if err != nil || !post.IsDir() || !os.SameFile(handle, post) {
			return nil, false, fmt.Errorf("readonly config: unsafe: %s below %s changed identity", comp, canonical)
		}
		current = child
		if barrier != nil {
			barrier()
		}
	}

	last := components[len(components)-1]
	info, err := current.Lstat(last)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("readonly config: cannot inspect %s below %s: %v", rel, canonical, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("readonly config: unsafe: %s below %s is not a plain regular file", rel, canonical)
	}
	if info.Size() > max {
		return nil, false, fmt.Errorf("readonly config: unsafe: %s below %s exceeds the %d byte bound", rel, canonical, max)
	}
	f, err := current.OpenFile(last, platform.ReadOnlyNonBlocking, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("readonly config: cannot open %s below %s: %v", rel, canonical, err)
	}
	defer func() { _ = f.Close() }()
	hinfo, err := f.Stat()
	if err != nil || !hinfo.Mode().IsRegular() || hinfo.Size() > max || !os.SameFile(info, hinfo) {
		return nil, false, fmt.Errorf("readonly config: unsafe: %s below %s changed kind, size or identity", rel, canonical)
	}
	post, err := current.Lstat(last)
	if err != nil || !post.Mode().IsRegular() || !os.SameFile(hinfo, post) {
		return nil, false, fmt.Errorf("readonly config: unsafe: %s below %s changed identity", rel, canonical)
	}
	data, err = io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, false, fmt.Errorf("readonly config: cannot read %s below %s: %v", rel, canonical, err)
	}
	if int64(len(data)) > max {
		return nil, false, fmt.Errorf("readonly config: unsafe: %s below %s exceeds the %d byte bound", rel, canonical, max)
	}
	return data, false, nil
}
