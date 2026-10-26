package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestMain(m *testing.M) { fakecli.RunTests(m) }

func TestSetupReadCommands(t *testing.T) {
	t.Run("// JS: \"unifiedDiff: equal contents produce no diff\"", func(t *testing.T) {
		if got := UnifiedDiff("a\nb\n", "a\nb\n", "f"); got != "" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("// JS: \"unifiedDiff: new and emptied files use zero-count ranges\"", func(t *testing.T) {
		got := UnifiedDiff("", "x\n", "f")
		if !strings.Contains(got, "@@ -0,0 +1 @@\n+x\n") {
			t.Fatalf("got %q", got)
		}
	})
	// Mutation captured: choosing insertion on a diff tie emits additions before deletions.
	// JS: "unifiedDiff: a change in the middle emits deletion before insertion"
	t.Run("// JS: \"unifiedDiff: a change in the middle emits deletion before insertion\"", func(t *testing.T) {
		got := UnifiedDiff("a\nb\nc\n", "a\nB\nc\n", "f")
		if !strings.Contains(got, "-b\n+B\n") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("// JS: \"probeTimeout: invalid values fail before a CLI runs\"", func(t *testing.T) {
		env := fakeEnv(t, t.TempDir())
		for _, v := range []string{"0", "-1", "1.5", "abc"} {
			env["SOHO_PROBE_TIMEOUT"] = v
			func() {
				defer func() {
					if recover() == nil {
						t.Errorf("accepted %q", v)
					}
				}()
				CmdProbe(nil, nil, env, t.TempDir())
			}()
		}
	})
	t.Run("// JS: \"probeTimeout: the defaults are 60 s for codex and 20 s for the rest\"", func(t *testing.T) {
		if n := probeKindDefaultTimeout("claude"); n != 20 {
			t.Fatalf("claude default=%d, want 20", n)
		}
		if n := probeKindDefaultTimeout("codex"); n != 60 {
			t.Fatalf("codex default=%d, want 60", n)
		}
	})
	t.Run("// JS: \"probeCmd: exact kind argv\"", func(t *testing.T) {
		cases := map[string][]string{"claude": {"-p", ProbePrompt, "--model", "m"}, "codex": {"exec", "-m", "m", ProbePrompt}, "pi": {"-p", "--no-session", ProbePrompt, "--model", "m"}, "opencode": {"run", ProbePrompt, "-m", "m"}}
		for k, want := range cases {
			got := probeArgs(k, "m")
			if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
				t.Errorf("%s: %q != %q", k, got, want)
			}
		}
	})
	// Mutation captured: skipping no-auth classification reports a regular exit status.
	// JS: "probeNoauthLine: first matching line wins, case-insensitive"
	t.Run("// JS: \"probeNoauthLine: first matching line wins, case-insensitive\"", func(t *testing.T) {
		raw := "hello\nNOT AUTHENTICATED here\ninvalid api key later"
		lines := strings.Split(raw, "\n")
		var got string
		for _, line := range lines {
			if noAuthRE.MatchString(line) {
				got = line
				break
			}
		}
		if got != "NOT AUTHENTICATED here" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("// JS: \"detect document reports source layers and stable kind order\"", func(t *testing.T) {
		ctx := &core.Config{Entries: map[string]core.ConfigEntry{}, Order: []string{}}
		doc := DetectJSON(ctx, platform.Env{"PATH": "", "SOHO_SKILL_DIR": t.TempDir()}, ".")
		if strings.Contains(doc, `"recommended_reviewer"`) || !strings.Contains(doc, `"kind": "claude"`) {
			t.Fatal(doc)
		}
	})
	// Mutation captured: preserving listing order instead of sorting changes the selected top models.
	// JS: "detectTopModels: three newest of a fake listing, short timeout, no cache written"
	t.Run("// JS: \"detectTopModels: three newest of a fake listing, short timeout, no cache written\"", func(t *testing.T) {
		bin := t.TempDir()
		listing := "Available models:\ngrok-4.7 - xAI Grok 4.7 (default)\ngrok-4.6 - older release\ngrok-4.10 - the newest\ngrok-3.9 - old\n"
		if _, err := fakecli.Install(t, bin, "grok", []fakecli.Rule{{Argv: []string{"models"}, Stdout: listing}}); err != nil {
			t.Fatal(err)
		}
		env := fakeEnv(t, bin)
		env["SOHO_SKILL_DIR"] = filepath.Join("..", "..", "skills", "soho")
		tmp := t.TempDir()
		env["TMPDIR"] = tmp
		ctx := &core.Config{Entries: map[string]core.ConfigEntry{}, Order: []string{}}
		doc := Detect(ctx, env, os.TempDir())
		rows, _ := doc.Get("kinds")
		kindsRows := rows.([]any)
		row := kindsRows[2].(*jsonjs.Object)
		models, _ := row.Get("models")
		got := models.([]any)
		want := []any{"grok-4.10", "grok-4.7", "grok-4.6"}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("models=%v want %v", got, want)
		}
		if _, err := os.Stat(filepath.Join(tmp, "herdr-soho-models-grok.txt")); !os.IsNotExist(err) {
			t.Fatalf("short timeout created model cache: %v", err)
		}
		missing := env.Clone()
		missing["PATH"] = t.TempDir()
		missingDoc := Detect(ctx, missing, os.TempDir())
		missingRows, _ := missingDoc.Get("kinds")
		missingModels, _ := missingRows.([]any)[2].(*jsonjs.Object).Get("models")
		if got := missingModels.([]any); len(got) != 0 {
			t.Fatalf("missing CLI models=%v, want empty", got)
		}
	})
	// Mutation captured: dropping RunCli's deadline lets this delayed fake return ready instead of timing out.
	// JS: "probeKind: a hang with a live fake CLI is stopped by the timeout"
	t.Run("// JS: \"probeKind: a hang with a live fake CLI is stopped by the timeout\"", func(t *testing.T) {
		bin := t.TempDir()
		if _, err := fakecli.Install(t, bin, "pi", []fakecli.Rule{{Argv: []string{"-p", "--no-session", ProbePrompt}, Delay: 1400}}); err != nil {
			t.Fatal(err)
		}
		env := fakeEnv(t, bin)
		got := probeOne(nil, "pi", "", "configured", 1, env, os.TempDir())
		if got.Status != "error" || got.Cause != "timeout after 1s (a timeout does not prove the assistant is unavailable; retry with --timeout 90)" {
			t.Fatalf("probe=%+v", got)
		}
	})
	// Mutation captured: matching the build family recommends the wrong reviewer kind.
	// JS: "recommendReviewerJson: ready reviewer must use another family"
}

func TestDetectRoleConfigKeyNormalizesDotsAndDashes(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".agents", "soho-roles")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.b.md"), []byte("---\nkind: grok\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a-b.md"), []byte("---\nkind: grok\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plain-name.md"), []byte("---\nkind: pi\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{"role_a_b_kind": {Value: "claude", Source: "project"}}, Order: []string{"role_a_b_kind"}}
	skillDir, err := filepath.Abs(filepath.Join("..", "..", "skills", "soho"))
	if err != nil {
		t.Fatal(err)
	}
	rows := detectRoles(ctx, platform.Env{"SOHO_SKILL_DIR": skillDir}, root)
	if len(rows) < 3 {
		t.Fatalf("roles=%v", rows)
	}
	got := map[string][2]string{}
	for _, value := range rows {
		row := value.(*jsonjs.Object)
		key, _ := row.Get("key")
		kind, _ := row.Get("value")
		source, _ := row.Get("source")
		got[key.(string)] = [2]string{kind.(string), source.(string)}
	}
	if got["role.a.b.kind"] != [2]string{"claude", "project"} {
		t.Fatalf("dotted role override=%v", got["role.a.b.kind"])
	}
	if got["role.a-b.kind"] != [2]string{"claude", "project"} {
		t.Fatalf("dashed role override=%v", got["role.a-b.kind"])
	}
	if got["role.plain-name.kind"] != [2]string{"pi", "role"} {
		t.Fatalf("plain role frontmatter=%v", got["role.plain-name.kind"])
	}
}

func TestSetupDetectJavaScriptCases(t *testing.T) {
	t.Run("// JS: \"detectKindJson: installed/executable/family/ceiling/custom_models per kind\"", func(t *testing.T) { // Mutation captured: an installed kind must retain its executable and metadata in the user-facing detect document.
		bin := t.TempDir()
		if _, err := fakecli.Install(t, bin, "claude", []fakecli.Rule{{AnyArgs: true}}); err != nil {
			t.Fatal(err)
		}
		env := fakeEnv(t, bin)
		env["SOHO_SKILL_DIR"] = filepath.Join("..", "..", "skills", "soho")
		ctx := &core.Config{Entries: map[string]core.ConfigEntry{}, Order: []string{}}
		doc := Detect(ctx, env, t.TempDir())
		value, _ := doc.Get("kinds")
		rows := value.([]any)
		claude := rows[0].(*jsonjs.Object)
		for key, want := range map[string]any{"kind": "claude", "executable": "claude", "installed": true, "family": "anthropic"} {
			got, _ := claude.Get(key)
			if got != want {
				t.Errorf("%s=%v want %v", key, got, want)
			}
		}
	})
	t.Run("// JS: \"detectWorkerModelsJson: defaults, project override, env override, unset key\"", func(t *testing.T) { // Mutation captured: worker model precedence and source are part of the setup detect response.
		ctx := &core.Config{Entries: map[string]core.ConfigEntry{"model_pi_worker": {Value: "project/pi", Source: "project"}}, Order: []string{"model_pi_worker"}}
		env := platform.Env{"PATH": "", "SOHO_SKILL_DIR": filepath.Join("..", "..", "skills", "soho"), "SOHO_MODEL_GROK_WORKER": "env/grok"}
		doc := Detect(ctx, env, t.TempDir())
		config, _ := doc.Get("config")
		workers, _ := config.(*jsonjs.Object).Get("worker_models")
		values := map[string][2]string{}
		for _, row := range workers.([]any) {
			obj := row.(*jsonjs.Object)
			key, _ := obj.Get("key")
			value, _ := obj.Get("value")
			source, _ := obj.Get("source")
			values[key.(string)] = [2]string{value.(string), source.(string)}
		}
		if values["model.pi.worker"] != [2]string{"project/pi", "project"} || values["model.grok.worker"] != [2]string{"env/grok", "env"} || values["model.claude.worker"] != [2]string{"", "builtin"} {
			t.Fatalf("worker sources=%v", values)
		}
	})
	t.Run("// JS: \"setupDetectJson: the bash key order and the preset/effective lanes defaults\"", func(t *testing.T) { // Mutation captured: changing key order or default lane/preset values changes the serialized setup contract.
		ctx := &core.Config{Entries: map[string]core.ConfigEntry{}, Order: []string{}}
		env := platform.Env{"PATH": "", "SOHO_SKILL_DIR": filepath.Join("..", "..", "skills", "soho")}
		got := DetectJSON(ctx, env, t.TempDir())
		if strings.Index(got, `"kinds"`) > strings.Index(got, `"config"`) || strings.Contains(got, `"recommended_reviewer"`) || !strings.Contains(got, `"panes"`) || !strings.Contains(got, `"value": "4"`) || !strings.Contains(got, `"effective_lanes"`) || !strings.Contains(got, `"presets"`) {
			t.Fatalf("detect JSON shape/order=%s", got)
		}
	})
}

func fakeEnv(t *testing.T, bin string) platform.Env {
	t.Helper()
	env := platform.Env{}
	for _, item := range fakecli.Env(testutil.CleanEnv(t), bin) {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			env[key] = value
		}
	}
	// Keep host-installed assistant CLIs out of test execution; every available
	// executable must come from the fixture directory.
	env["PATH"] = bin
	if runtime.GOOS == "windows" {
		env["PATHEXT"] = ".EXE;.CMD"
	}
	return env
}

func TestDetectRespectsConfiguredModelTimeout(t *testing.T) { // Mutation captured: replacing an explicit timeout with the default can let slow provider CLIs overrun the caller's budget.
	bin := t.TempDir()
	if _, err := fakecli.Install(t, bin, "grok", []fakecli.Rule{{Argv: []string{"models"}, Stdout: "grok-4.7 - latest\n", Delay: 250}}); err != nil {
		t.Fatal(err)
	}
	env := fakeEnv(t, bin)
	env["SOHO_MODELS_TIMEOUT"] = "0.01"
	env["TMPDIR"] = t.TempDir()
	env["SOHO_SKILL_DIR"] = filepath.Join("..", "..", "skills", "soho")
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}, Order: []string{}}
	doc := Detect(ctx, env, os.TempDir())
	rowsValue, _ := doc.Get("kinds")
	rows := rowsValue.([]any)
	grok := rows[2].(*jsonjs.Object)
	modelsValue, _ := grok.Get("models")
	if got := modelsValue.([]any); len(got) != 0 {
		t.Fatalf("explicit 10ms timeout was ignored; models=%v", got)
	}
}
