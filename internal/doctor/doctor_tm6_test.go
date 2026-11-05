package doctor

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func tm6DoctorEnv(t *testing.T) (platform.Env, string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	roles := filepath.Join(root, "roles")
	state := filepath.Join(root, "state")
	bin := filepath.Join(root, "bin")
	tmp := filepath.Join(root, "tmp")
	for _, dir := range []string{home, roles, bin, tmp} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	_, file, _, _ := runtime.Caller(0)
	skill := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "skills", "soho"))
	for _, role := range []string{"implementer", "designer", "tasker", "scouter", "researcher", "reviewer", "security-reviewer", "ui-reviewer", "inspector", "documenter", "planner", "sub-orchestrator"} {
		kind := "grok"
		if role == "reviewer" || role == "documenter" {
			kind = "codex"
		} else if role == "designer" || role == "ui-reviewer" || role == "inspector" {
			kind = "agy"
		} else if role == "security-reviewer" || role == "planner" || role == "sub-orchestrator" {
			kind = "claude"
		}
		data := "---\nkind: " + kind + "\n---\n"
		if role == "ui-reviewer" {
			data = "---\nkind: agy\nmodel: gemini|sonnet\n---\n"
		}
		if err := os.WriteFile(filepath.Join(roles, role+".md"), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	clean := map[string]string{}
	for _, item := range testutil.CleanEnv(t) {
		if key, value, ok := strings.Cut(item, "="); ok {
			clean[key] = value
		}
	}
	clean["HOME"], clean["USERPROFILE"], clean["PATH"] = home, home, bin
	clean["TMPDIR"] = tmp
	clean["HERDR_ENV"] = "1"
	clean["SOHO_DIR"] = state
	clean["SOHO_ROLES"] = roles
	clean["SOHO_SKILL_DIR"] = skill
	clean["HERDR_SOCKET_PATH"] = filepath.Join(root, "missing.sock")
	return platform.Env(clean), root
}

func tm6DoctorConfig(values map[string]string, source string) *core.Config {
	ctx := &core.Config{Entries: make(map[string]core.ConfigEntry, len(values))}
	for key, value := range values {
		ctx.Entries[key] = core.ConfigEntry{Value: value, Source: source}
		ctx.Order = append(ctx.Order, key)
	}
	return ctx
}

func tm6DoctorOutput(t *testing.T, ctx *core.Config, env platform.Env, root string) string {
	t.Helper()
	var out strings.Builder
	DoctorCheck(ctx, env, root, &out)
	return out.String()
}

func TestDoctorTM6LaneWarnings(t *testing.T) {
	t.Run(`doctorLaneWarnings: an invalid lanes value and panes outside 2/3/4`, func(t *testing.T) { // JS: "doctorLaneWarnings: an invalid lanes value and panes outside 2/3/4"
		// Mutation captured: accepting invalid lane and pane values removes both diagnostics.
		env, root := tm6DoctorEnv(t)
		for _, tc := range []struct {
			values map[string]string
			want   string
		}{
			{map[string]string{"lanes": "bogus"}, "config: lanes='bogus' is not on|off"},
			{map[string]string{"panes": "5"}, "config: panes='5' is not 2, 3 or 4"},
			{map[string]string{"panes": "3"}, "config: panes=3 (project)"},
		} {
			var out strings.Builder
			doctorLaneWarnings(tm6DoctorConfig(tc.values, "project"), env, root, &Say{Out: &out})
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("missing %q in %s", tc.want, out.String())
			}
		}
	})
	t.Run(`doctorLaneWarnings: the pane_mode status line (strict ok, flex ok, invalid warn)`, func(t *testing.T) { // JS: "doctorLaneWarnings: the pane_mode status line (strict ok, flex ok, invalid warn)"
		// Mutation captured: treating invalid pane_mode as valid or losing its effective pane count changes the status line.
		env, root := tm6DoctorEnv(t)
		for _, tc := range []struct {
			values map[string]string
			want   string
		}{
			{map[string]string{"panes": "2"}, "pane_mode=strict (never more than 2 panels)"},
			{map[string]string{"panes": "3", "pane_mode": "flex", "flex_roles": "documenter"}, "pane_mode=flex (+1 temporary panel for documenter)"},
			{map[string]string{"pane_mode": "bad"}, "pane_mode='bad' is not strict|flex"},
		} {
			var out strings.Builder
			doctorLaneWarnings(tm6DoctorConfig(tc.values, "project"), env, root, &Say{Out: &out})
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("missing %q in %s", tc.want, out.String())
			}
		}
	})
	t.Run(`doctor: split_max_panes is checked against 1 + max_workers (greater and no-room warn)`, func(t *testing.T) { // A9: the reference is 1 + the effective max_workers (the frozen JS text was panes-based)
		// Mutation captured: comparing the split cap to panes alone instead of 1 + max_workers moves both warning boundaries.
		env, root := tm6DoctorEnv(t)
		for _, tc := range []struct {
			values map[string]string
			want   string
		}{
			{map[string]string{"panes": "4", "pane_mode": "flex", "split_max_panes": "4"}, "leaves no room for the whole team"},
			{map[string]string{"panes": "4", "pane_mode": "flex", "split_max_panes": "5"}, "leaves no room for the whole team"},
			{map[string]string{"panes": "4", "pane_mode": "flex", "split_max_panes": "6"}, "no split cap diagnostic"},
			{map[string]string{"panes": "4", "pane_mode": "flex", "split_max_panes": "7"}, "greater than 1 + max_workers=5"},
			{map[string]string{"panes": "4", "split_max_panes": "5"}, ""},
			{map[string]string{"panes": "4", "split_max_panes": "6"}, "greater than 1 + max_workers=4"},
		} {
			var out strings.Builder
			doctorLaneWarnings(tm6DoctorConfig(tc.values, "project"), env, root, &Say{Out: &out})
			if tc.want != "" && tc.want != "no split cap diagnostic" && !strings.Contains(out.String(), tc.want) {
				t.Fatalf("missing %q in %s", tc.want, out.String())
			}
			if tc.want == "no split cap diagnostic" && strings.Contains(out.String(), "split_max_panes=6") {
				t.Fatalf("unexpected exact-cap warning: %s", out.String())
			}
			if tc.want == "" && strings.Contains(out.String(), "leaves no room") {
				t.Fatalf("unexpected flex warning: %s", out.String())
			}
		}
	})
	t.Run(`doctorLaneWarnings: unknown role, a role in two lanes, edit+review mixed, planner in a lane`, func(t *testing.T) { // JS: "doctorLaneWarnings: unknown role, a role in two lanes, edit+review mixed, planner in a lane"
		// Mutation captured: dropping any lane validation hides its corresponding observable warning.
		env, root := tm6DoctorEnv(t)
		ctx := tm6DoctorConfig(map[string]string{"lanes": "on", "lane_build_roles": "implementer,nosuch,planner,reviewer", "lane_read_roles": "implementer,researcher"}, "project")
		var out strings.Builder
		doctorLaneWarnings(ctx, env, root, &Say{Out: &out})
		for _, want := range []string{"unknown roles: build:nosuch", "roles in more than one lane: implementer", "build mix an edit role with a review role", "'build' includes planner"} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("missing %q in %s", want, out.String())
			}
		}
	})
	t.Run(`doctorLaneWarnings: the planner in a solo lane is the only role: no used kind, no unknown`, func(t *testing.T) { // JS: "doctorLaneWarnings: the planner in a solo lane is the only role: no used kind, no unknown"
		// Mutation captured: counting planner as a pane role changes the known-role and used-kind results.
		env, root := tm6DoctorEnv(t)
		ctx := tm6DoctorConfig(map[string]string{"lane_solo_roles": "planner"}, "project")
		var out strings.Builder
		doctorLaneWarnings(ctx, env, root, &Say{Out: &out})
		if !strings.Contains(out.String(), "every role is known") || len(usedKinds(ctx, env, root)) != 0 {
			t.Fatalf("output=%s kinds=%v", out.String(), usedKinds(ctx, env, root))
		}
	})
	t.Run(`doctorLaneWarnings: a lane effort from a lower layer than the lane kind is reported as ok`, func(t *testing.T) { // JS: "doctorLaneWarnings: a lane effort from a lower layer than the lane kind is reported as ok"
		// Mutation captured: omitting layer precedence hides the ignored effort decision.
		env, root := tm6DoctorEnv(t)
		ctx := tm6DoctorConfig(map[string]string{"panes": "3", "lane_review_kind": "codex", "lane_review_effort": "low"}, "project")
		ctx.Entries["lane_review_effort"] = core.ConfigEntry{Value: "low", Source: "user"}
		var out strings.Builder
		doctorLaneWarnings(ctx, env, root, &Say{Out: &out})
		if !strings.Contains(out.String(), "ignored lane effort low from user (another kind)") {
			t.Fatalf("output=%s", out.String())
		}
	})
	t.Run(`doctorDiscardedModels: a role model from a layer below the role kind warns (exact text, both directions)`, func(t *testing.T) { // JS: "doctorDiscardedModels: a role model from a layer below the role kind warns (exact text, both directions)"
		// Mutation captured: reversing config-layer precedence reports the kept model and drops the ignored one.
		env, root := tm6DoctorEnv(t)
		ctx := tm6DoctorConfig(map[string]string{"lanes": "off", "role_reviewer_model": "opus", "role_reviewer_kind": "codex", "role_designer_kind": "claude", "role_designer_model": "sonnet"}, "user")
		ctx.Entries["role_reviewer_kind"] = core.ConfigEntry{Value: "codex", Source: "project"}
		ctx.Entries["role_designer_model"] = core.ConfigEntry{Value: "sonnet", Source: "project"}
		var out strings.Builder
		doctorDiscardedModels(ctx, env, root, &Say{Out: &out})
		if !strings.Contains(out.String(), "role.reviewer.model=opus") || strings.Contains(out.String(), "role.designer.model=sonnet") {
			t.Fatalf("output=%s", out.String())
		}
	})
	t.Run(`doctor: the session layer gets the same key checks as the user and project files`, func(t *testing.T) { // JS: "doctor: the session layer gets the same key checks as the user and project files"
		// Mutation captured: filtering session-sourced orphan and planner entries suppresses their warnings.
		env, root := tm6DoctorEnv(t)
		ctx := tm6DoctorConfig(map[string]string{"panes": "4", "role_planner_model": "fable", "lane_ops_kind": "codex", "lane_review_kind": "codex", "lane_review_model": "opus"}, "session")
		ctx.Entries["lane_review_model"] = core.ConfigEntry{Value: "opus", Source: "user"}
		var out strings.Builder
		doctorLaneWarnings(ctx, env, root, &Say{Out: &out})
		if !strings.Contains(out.String(), "lane.ops.kind=codex (session)") {
			t.Fatalf("output=%s", out.String())
		}
		if !strings.Contains(out.String(), "role_planner_model is set (session)") {
			t.Fatalf("planner warning missing: %s", out.String())
		}
		out.Reset()
		doctorDiscardedModels(ctx, env, root, &Say{Out: &out})
		if !strings.Contains(out.String(), "lane.review.model=opus (user)") {
			t.Fatalf("output=%s", out.String())
		}
	})
	t.Run(`doctorModelPairs: an unresolvable kind+model pair warns; an unavailable list skips`, func(t *testing.T) { // JS: "doctorModelPairs: an unresolvable kind+model pair warns; an unavailable list skips"
		// Mutation captured: warning on unresolved model specs must not turn into silence or warn when the kind is unavailable.
		env, root := tm6DoctorEnv(t)
		ctx := tm6DoctorConfig(map[string]string{"lanes": "off", "role_reviewer_kind": "grok", "role_reviewer_model": "not-a-model"}, "project")
		var out strings.Builder
		bin := t.TempDir()
		if _, err := fakecli.Install(t, bin, "grok", []fakecli.Rule{{AnyArgs: true, Stdout: "grok-4.7\ngrok-4\ngrok-3\n"}}); err != nil {
			t.Fatal(err)
		}
		fakeEnv := platform.Env{}
		for _, entry := range fakecli.Env(env.List(), bin) {
			if key, value, ok := strings.Cut(entry, "="); ok {
				fakeEnv[key] = value
			}
		}
		out.Reset()
		doctorModelPairs(ctx, fakeEnv, root, &Say{Out: &out})
		if !strings.Contains(out.String(), "role 'reviewer' model 'not-a-model'") {
			t.Fatalf("unresolvable model not warned: %s", out.String())
		}
		ctx = tm6DoctorConfig(map[string]string{"lanes": "off", "role_reviewer_kind": "grok", "role_reviewer_model": "grok-4"}, "project")
		out.Reset()
		doctorModelPairs(ctx, fakeEnv, root, &Say{Out: &out})
		if out.Len() != 0 {
			t.Fatalf("resolvable model warned: %s", out.String())
		}
		ctx = tm6DoctorConfig(map[string]string{"lanes": "off", "role_reviewer_kind": "grok", "role_reviewer_model": "not-a-model"}, "project")
		t.Run("an unavailable model listing skips unresolved warnings", func(t *testing.T) {
			// Mutation captured: treating an empty model list as unresolved adds a warning JS does not emit.
			unavailableEnv, unavailableRoot := tm6DoctorEnv(t)
			var unavailable strings.Builder
			doctorModelPairs(ctx, unavailableEnv, unavailableRoot, &Say{Out: &unavailable})
			if unavailable.Len() != 0 {
				t.Fatalf("unavailable model list warned: %s", unavailable.String())
			}
		})
	})
	t.Run(`doctorModelPairs: a lane model without a lane kind is judged against each role kind`, func(t *testing.T) { // JS: "doctorModelPairs: a lane model without a lane kind is judged against each role kind"
		// Mutation captured: skipping a lane model without its own kind loses the role-specific validation.
		env, root := tm6DoctorEnv(t)
		ctx := tm6DoctorConfig(map[string]string{"panes": "4", "lane_review_model": "not-a-model", "role_reviewer_kind": "grok"}, "project")
		var out strings.Builder
		bin := t.TempDir()
		if _, err := fakecli.Install(t, bin, "grok", []fakecli.Rule{{AnyArgs: true, Stdout: "grok-4.7\ngrok-4\ngrok-3\n"}}); err != nil {
			t.Fatal(err)
		}
		fakeEnv := platform.Env{}
		for _, entry := range fakecli.Env(env.List(), bin) {
			if key, value, ok := strings.Cut(entry, "="); ok {
				fakeEnv[key] = value
			}
		}
		out.Reset()
		doctorModelPairs(ctx, fakeEnv, root, &Say{Out: &out})
		if !strings.Contains(out.String(), "lane 'review' model 'not-a-model'") {
			t.Fatalf("lane model not judged against role kind: %s", out.String())
		}
		t.Run("an unavailable lane model listing skips unresolved warnings", func(t *testing.T) {
			// Mutation captured: judging a lane model against an empty list adds a warning JS skips.
			unavailableEnv, unavailableRoot := tm6DoctorEnv(t)
			var unavailable strings.Builder
			doctorModelPairs(ctx, unavailableEnv, unavailableRoot, &Say{Out: &unavailable})
			if unavailable.Len() != 0 {
				t.Fatalf("unavailable model list warned: %s", unavailable.String())
			}
		})
	})
	t.Run(`doctorDiscardedModels: the frontmatter model is dropped when the kind comes from a config layer`, func(t *testing.T) { // JS: "doctorDiscardedModels: the frontmatter model is dropped when the kind comes from a config layer"
		// Mutation captured: allowing a role-file model to survive a higher-layer kind drops the diagnostic.
		env, root := tm6DoctorEnv(t)
		ctx := tm6DoctorConfig(map[string]string{"lanes": "off", "role_ui_reviewer_kind": "grok"}, "project")
		var out strings.Builder
		doctorDiscardedModels(ctx, env, root, &Say{Out: &out})
		if !strings.Contains(out.String(), "role file model 'gemini|sonnet' of 'ui-reviewer'") {
			t.Fatalf("output=%s", out.String())
		}
	})
	t.Run(`doctorLaneWarnings: per-role kind/model under a lane kind, divergent kinds, alignment warns`, func(t *testing.T) { // JS: "doctorLaneWarnings: per-role kind/model under a lane kind, divergent kinds, alignment warns"
		// Mutation captured: suppressing lane conflicts or worker alignment removes the corresponding diagnostics.
		env, root := tm6DoctorEnv(t)
		ctx := tm6DoctorConfig(map[string]string{"panes": "4", "max_workers": "9", "lane_build_roles": "implementer,designer", "lane_build_kind": "grok", "role_implementer_kind": "codex", "role_designer_model": "opus", "lane_review_roles": "reviewer,security-reviewer", "role_reviewer_kind": "codex", "role_security_reviewer_kind": "claude"}, "project")
		var out strings.Builder
		doctorLaneWarnings(ctx, env, root, &Say{Out: &out})
		for _, want := range []string{"role.implementer.kind is set", "role.designer.model is set", "max_workers=9 but the lanes hold", "roles disagree"} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("missing %q in %s", want, out.String())
			}
		}
	})
	t.Run(`doctorLaneWarnings: the old preset lanes and the orphan lane keys`, func(t *testing.T) { // JS: "doctorLaneWarnings: the old preset lanes and the orphan lane keys"
		// Mutation captured: dropping the old-preset signature or orphan args check hides configuration warnings.
		env, root := tm6DoctorEnv(t)
		old := map[string]string{"lane_build_roles": "implementer,designer,tasker,scouter,researcher", "lane_review_roles": "reviewer,security-reviewer,ui-reviewer,inspector"}
		ctx := tm6DoctorConfig(old, "project")
		var out strings.Builder
		doctorLaneWarnings(ctx, env, root, &Say{Out: &out})
		if strings.Contains(out.String(), "old preset") {
			t.Fatalf("current lane definitions called old preset: %s", out.String())
		}
		ctx = tm6DoctorConfig(map[string]string{"lane_explore_args": "-c x"}, "project")
		out.Reset()
		doctorLaneWarnings(ctx, env, root, &Say{Out: &out})
		if !strings.Contains(out.String(), "lane.explore.args=-c x") {
			t.Fatalf("output=%s", out.String())
		}
		legacy := map[string]string{"panes": "3", "lane_build_roles": "implementer,designer,tasker", "lane_read_roles": "scouter,researcher,reviewer,security-reviewer,ui-reviewer,inspector"}
		ctx = tm6DoctorConfig(legacy, "project")
		out.Reset()
		doctorLaneWarnings(ctx, env, root, &Say{Out: &out})
		if !strings.Contains(out.String(), "lanes come from an old preset") {
			t.Fatalf("legacy signature not detected: %s", out.String())
		}
	})
	t.Run(`doctor: the native args the current lane mode ignores (lanes on: role args; lanes off: lane args)`, func(t *testing.T) { // JS: "doctor: the native args the current lane mode ignores (lanes on: role args; lanes off: lane args)"
		// Mutation captured: removing either branch leaves ignored native args unreported.
		env, _ := tm6DoctorEnv(t)
		for _, tc := range []struct {
			values map[string]string
			want   string
		}{
			{map[string]string{"role_reviewer_args": "--flag"}, "role.reviewer.args is ignored"},
			{map[string]string{"lanes": "off", "lane_review_args": "--flag"}, "lane.review.args is ignored"},
		} {
			var out strings.Builder
			doctorLaneArgs(tm6DoctorConfig(tc.values, "project"), env, &Say{Out: &out})
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("missing %q in %s", tc.want, out.String())
			}
		}
	})
	t.Run(`doctorFeedbackWarnings: feedback=local warns on an empty or missing feedback_dir; nothing otherwise`, func(t *testing.T) { // JS: "doctorFeedbackWarnings: feedback=local warns on an empty or missing feedback_dir; nothing otherwise"
		// Mutation captured: accepting an empty or relative path removes the local-feedback diagnostic.
		env, _ := tm6DoctorEnv(t)
		for _, dir := range []string{"", "relative"} {
			var out strings.Builder
			doctorFeedback(tm6DoctorConfig(map[string]string{"feedback": "local", "feedback_dir": dir}, "project"), env, &Say{Out: &out})
			if !strings.Contains(out.String(), "feedback=local but feedback_dir") {
				t.Fatalf("dir %q: %s", dir, out.String())
			}
		}
		var out strings.Builder
		doctorFeedback(tm6DoctorConfig(map[string]string{"feedback": "ask"}, "project"), env, &Say{Out: &out})
		if out.Len() != 0 {
			t.Fatalf("default feedback should be quiet: %s", out.String())
		}
		file := filepath.Join(t.TempDir(), "not-a-directory")
		if err := os.WriteFile(file, []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		out.Reset()
		doctorFeedback(tm6DoctorConfig(map[string]string{"feedback": "local", "feedback_dir": file}, "project"), env, &Say{Out: &out})
		if !strings.Contains(out.String(), "feedback=local but feedback_dir") {
			t.Fatalf("file path accepted: %s", out.String())
		}
		valid := t.TempDir()
		out.Reset()
		doctorFeedback(tm6DoctorConfig(map[string]string{"feedback": "local", "feedback_dir": valid}, "project"), env, &Say{Out: &out})
		if out.Len() != 0 {
			t.Fatalf("directory rejected: %s", out.String())
		}
	})
	t.Run(`doctorUsedKinds: lane kind wins, else the lane roles, else every role file; planner excluded`, func(t *testing.T) { // JS: "doctorUsedKinds: lane kind wins, else the lane roles, else every role file; planner excluded"
		// Mutation captured: including planner or ignoring a lane kind changes the used-kind set.
		env, root := tm6DoctorEnv(t)
		ctx := tm6DoctorConfig(map[string]string{"lane_solo_roles": "planner"}, "project")
		if got := usedKinds(ctx, env, root); len(got) != 0 {
			t.Fatalf("planner contributed kinds: %v", got)
		}
		ctx = tm6DoctorConfig(map[string]string{"panes": "2", "lane_build_kind": "grok"}, "project")
		if got := usedKinds(ctx, env, root); len(got) != 1 || got[0] != "grok" {
			t.Fatalf("lane kind wins: %v", got)
		}
		ctx = tm6DoctorConfig(map[string]string{"lanes": "off"}, "project")
		got := usedKinds(ctx, env, root)
		if strings.Join(got, " ") != "agy claude codex grok" {
			t.Fatalf("all active role files: %v", got)
		}
	})
	t.Run(`nowrite: doctor reports an absent state dir as absent, never creates it, and keeps the warning for unwritable or inaccessible state`, func(t *testing.T) { // JS: "nowrite: doctor reports an absent state dir as absent, never creates it, and keeps the warning for unwritable or inaccessible state"
		// Mutation captured: creating state under no-write mode violates the observable absent-state contract.
		env, root := tm6DoctorEnv(t)
		env["SOHO_NOWRITE"] = "1"
		state := filepath.Join(env.Get("SOHO_DIR"), "ws")
		env["HERDR_WORKSPACE_ID"] = "ws"
		out := tm6DoctorOutput(t, tm6DoctorConfig(nil, "defaults"), env, root)
		if !strings.Contains(out, "state dir absent (no-write mode, not created)") {
			t.Fatalf("output=%s", out)
		}
		if _, err := os.Stat(state); !os.IsNotExist(err) {
			t.Fatalf("state created in no-write mode: %v", err)
		}
		if err := os.MkdirAll(state, 0o700); err != nil {
			t.Fatal(err)
		}
		old := writableAccess
		writableAccess = func(string) error { return os.ErrPermission }
		t.Cleanup(func() { writableAccess = old })
		out = tm6DoctorOutput(t, tm6DoctorConfig(nil, "defaults"), env, root)
		if !strings.Contains(out, "warn   state dir not writable:") {
			t.Fatalf("unwritable state warning missing: %s", out)
		}
	})
	t.Run(`doctor: pi maxTokens without the 8192 headroom over the effort budget warns`, func(t *testing.T) { // JS: "doctor: pi maxTokens without the 8192 headroom over the effort budget warns"
		// Mutation captured: changing the 8192-token headroom comparison drops the provider model warning.
		env, root := tm6DoctorEnv(t)
		pi := filepath.Join(env.Get("HOME"), ".pi", "agent", "models.json")
		if err := os.MkdirAll(filepath.Dir(pi), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pi, []byte(`{"providers":{"local":{"apiKey":"$KEY","models":[{"id":"m","maxTokens":20000}]}}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		out := tm6DoctorOutput(t, tm6DoctorConfig(nil, "defaults"), env, root)
		if !strings.Contains(out, "pi model local/m: maxTokens 20000 leaves less than 8192") {
			t.Fatalf("output=%s", out)
		}
	})
	t.Run(`doctor: opencode model without thinking_token_budget warns; the ok line; nothing without a provider`, func(t *testing.T) { // JS: "doctor: opencode model without thinking_token_budget warns; the ok line; nothing without a provider"
		// Mutation captured: treating a missing model budget as configured hides the opencode warning.
		env, root := tm6DoctorEnv(t)
		file := filepath.Join(root, "opencode.json")
		if err := os.WriteFile(file, []byte(`{"provider":{"local":{"options":{"apiKey":"{env:KEY}"},"models":{"m":{}}}}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		out := tm6DoctorOutput(t, tm6DoctorConfig(nil, "defaults"), env, root)
		if !strings.Contains(out, "opencode model local/m has no thinking_token_budget") {
			t.Fatalf("output=%s", out)
		}
		if err := os.WriteFile(file, []byte(`{"provider":{"local":{"options":{"apiKey":"{env:KEY}"},"models":{"m":{"options":{"thinking_token_budget":16000}}}}}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		out = tm6DoctorOutput(t, tm6DoctorConfig(nil, "defaults"), env, root)
		if strings.Contains(out, "has no thinking_token_budget") || !strings.Contains(out, "own providers: no known trap") {
			t.Fatalf("valid provider output=%s", out)
		}
		if err := os.Remove(file); err != nil {
			t.Fatal(err)
		}
		out = tm6DoctorOutput(t, tm6DoctorConfig(nil, "defaults"), env, root)
		if strings.Contains(out, "own provider") {
			t.Fatalf("missing provider emitted diagnostics: %s", out)
		}
	})
	t.Run(`doctor: the literal key value never appears on stdout/stderr`, func(t *testing.T) { // JS: "doctor: the literal key value never appears on stdout/stderr"
		// Mutation captured: interpolating a provider key into diagnostics leaks the secret literal.
		env, root := tm6DoctorEnv(t)
		pi := filepath.Join(env.Get("HOME"), ".pi", "agent", "models.json")
		if err := os.MkdirAll(filepath.Dir(pi), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pi, []byte(`{"providers":{"local":{"apiKey":"literal-secret","models":[{"id":"m","maxTokens":32768}]}}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		out := tm6DoctorOutput(t, tm6DoctorConfig(nil, "defaults"), env, root)
		if strings.Contains(out, "literal-secret") || !strings.Contains(out, "literal apiKey") {
			t.Fatalf("output=%s", out)
		}
	})
	t.Run(`doctor: the literal-key warn appears once per provider, not per model`, func(t *testing.T) { // JS: "doctor: the literal-key warn appears once per provider, not per model"
		// Mutation captured: emitting a key warning once per model duplicates provider-level findings.
		env, root := tm6DoctorEnv(t)
		pi := filepath.Join(env.Get("HOME"), ".pi", "agent", "models.json")
		if err := os.MkdirAll(filepath.Dir(pi), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pi, []byte(`{"providers":{"local":{"apiKey":"literal","models":[{"id":"m1","maxTokens":32768},{"id":"m2","maxTokens":32768}]}}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		out := tm6DoctorOutput(t, tm6DoctorConfig(nil, "defaults"), env, root)
		if got := strings.Count(out, "own provider 'local'"); got != 1 {
			t.Fatalf("provider warning count=%d: %s", got, out)
		}
	})
	t.Run(`doctor: a non-numeric thinking_token_budget is no budget`, func(t *testing.T) { // JS: "doctor: a non-numeric thinking_token_budget is no budget"
		// Mutation captured: accepting string or null budget values hides invalid/missing numeric configuration.
		env, root := tm6DoctorEnv(t)
		file := filepath.Join(root, "opencode.json")
		for _, budget := range []string{`null`, `"16000"`} {
			data := `{"provider":{"local":{"options":{"apiKey":"{env:KEY}"},"models":{"m":{"options":{"thinking_token_budget":` + budget + `}}}}}}`
			if err := os.WriteFile(file, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			out := tm6DoctorOutput(t, tm6DoctorConfig(nil, "defaults"), env, root)
			if !strings.Contains(out, "has no thinking_token_budget") {
				t.Fatalf("budget %s: %s", budget, out)
			}
		}
		if err := os.WriteFile(file, []byte(`{"provider":{"local":{"options":{"apiKey":"{env:KEY}"},"models":{"m":{"options":{"thinking_token_budget":16000}}}}}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		out := tm6DoctorOutput(t, tm6DoctorConfig(nil, "defaults"), env, root)
		if strings.Contains(out, "has no thinking_token_budget") {
			t.Fatalf("numeric budget treated as invalid: %s", out)
		}
	})
}
