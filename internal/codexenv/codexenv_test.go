package codexenv

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// Mutation captured: accepting duplicate policy keys changes the parsed result.
func TestDifferentialCorpus(t *testing.T) {
	b, err := os.ReadFile("testdata/codexenv.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		ManualCount    int `json:"manual_count"`
		GeneratedCount int `json:"generated_count"`
		Rows           []struct {
			Name       string     `json:"name"`
			Input      string     `json:"input"`
			Policy     *Policy    `json:"policy"`
			Evaluation Evaluation `json:"evaluation"`
		} `json:"rows"`
		Strips []struct {
			Input  string `json:"input"`
			Output string `json:"output"`
		} `json:"strips"`
		Globs []struct {
			Pattern string `json:"pattern"`
			Value   string `json:"value"`
			Output  bool   `json:"output"`
		} `json:"globs"`
	}
	if err = json.Unmarshal(b, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.ManualCount < 97 || corpus.GeneratedCount < 3097 {
		t.Fatalf("corpus too small: manual=%d generated=%d", corpus.ManualCount, corpus.GeneratedCount)
	}
	structuralDivergences, evaluationDivergences := 0, 0
	var structuralExamples, evaluationExamples []string
	for _, row := range corpus.Rows {
		got := ParseCodexPolicy(row.Input)
		if !reflect.DeepEqual(got, row.Policy) {
			structuralDivergences++
			if len(structuralExamples) < 10 {
				structuralExamples = append(structuralExamples, row.Name)
			}
		}
		if e := EvaluateCodexPolicy(got); e != row.Evaluation {
			evaluationDivergences++
			if len(evaluationExamples) < 10 {
				evaluationExamples = append(evaluationExamples, row.Name)
			}
		}
	}
	t.Logf("documents=%d structural-divergences=%d evaluation-divergences=%d", len(corpus.Rows), structuralDivergences, evaluationDivergences)
	if structuralDivergences > 0 || evaluationDivergences > 0 {
		t.Errorf("structural examples %v; evaluation examples %v", structuralExamples, evaluationExamples)
	}
	for i, row := range corpus.Strips {
		t.Run("JS: strip TOML comment "+string(rune('A'+i)), func(t *testing.T) {
			if got := StripTomlComment(row.Input); got != row.Output {
				t.Fatalf("got %q want %q", got, row.Output)
			}
		})
	}
	for i, row := range corpus.Globs {
		t.Run("JS: glob "+string(rune('A'+i)), func(t *testing.T) {
			if got := GlobMatch(row.Pattern, row.Value); got != row.Output {
				t.Fatalf("%q ~ %q = %v want %v", row.Pattern, row.Value, got, row.Output)
			}
		})
	}
}

// Mutation captured: bypassing include_only incorrectly reports all HERDR_* as present.
func TestCodexPolicyDecisions(t *testing.T) {
	cases := []struct {
		name, input string
		drops       bool
		reason      string
	}{
		{`JS: Decision 1 case: core (inherit="core" drops HERDR_*)`, "[shell_environment_policy]\ninherit = \"core\"\n", true, `inherit="core"`},
		{`JS: Decision 1 case: none (inherit="none" drops HERDR_*)`, "[shell_environment_policy]\ninherit = \"none\"\n", true, `inherit="none"`},
		{"JS: Decision 1 case: include_only without HERDR_*", "[shell_environment_policy]\ninherit = \"all\"\ninclude_only = [\"PATH\"]\n", true, "include_only does not match HERDR_ENV"},
		{"JS: Decision 1 case: include_only with HERDR_* (no warning)", "[shell_environment_policy]\ninclude_only = [\"HERDR_*\"]\n", false, ""},
		{"JS: Decision 1 case: exclude (exclude matches HERDR_ENV)", "[shell_environment_policy]\nexclude = [\"HERDR_ENV\"]\n", true, "exclude matches HERDR_ENV"},
		{"JS: Decision 1 case: multi-line array parsing", "[shell_environment_policy]\ninherit=\"all\"\ninclude_only = [\n\"HOME\",\n\"HERDR_*\"\n]\n", false, ""},
		{`JS: Decision 1 case: core + include_only with HERDR_* warns inherit="core"`, "[shell_environment_policy]\ninherit=\"core\"\ninclude_only=[\"HERDR_*\"]\n", true, `inherit="core"`},
		{"JS: Decision 1 case: none + set with all three HERDR_* does not warn", "[shell_environment_policy]\ninherit=\"none\"\n[shell_environment_policy.set]\nHERDR_ENV=\"1\"\nHERDR_PANE_ID=\"2\"\nHERDR_WORKSPACE_ID=\"3\"\n", false, ""},
		{`JS: Decision 1 case: all + exclude=["HERDR_*"] + set with all three does not warn`, "[shell_environment_policy]\ninherit=\"all\"\nexclude=[\"HERDR_*\"]\nset={HERDR_ENV=\"1\", HERDR_PANE_ID=\"2\", HERDR_WORKSPACE_ID=\"3\"}\n", false, ""},
		{"JS: Decision 2 case: a multi-line inline set table with } inside a string is read whole", "[shell_environment_policy]\nset = {\n HERDR_ENV = \"a } # b\",\n HERDR_PANE_ID = \"x\",\n HERDR_WORKSPACE_ID = \"y\"\n}\n", false, ""},
		{`JS: Decision 1 case: all + include_only=["PATH"] + set HERDR_ENV warns include_only does not match HERDR_ENV`, "[shell_environment_policy]\ninherit=\"all\"\ninclude_only=[\"PATH\"]\nset={HERDR_ENV=\"1\"}\n", true, "include_only does not match HERDR_ENV"},
		{`JS: Decision 1 case: exclude=["HERDR_EN?"] warns exclude matches HERDR_ENV`, "[shell_environment_policy]\nexclude=[\"HERDR_EN?\"]\n", true, "exclude matches HERDR_ENV"},
		{"JS: Decision 1 case: duplicate key in section or set table makes parse null (no warning)", "[shell_environment_policy]\ninherit=\"all\"\ninherit=\"core\"\n", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := ParseCodexPolicy(tc.input)
			if tc.name == "JS: Decision 1 case: duplicate key in section or set table makes parse null" {
				if p != nil {
					t.Fatal("expected invalid duplicate")
				}
				return
			}
			got := EvaluateCodexPolicy(p)
			if got.Drops != tc.drops || got.Reason != tc.reason {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestTM11WindowsCodexAncestorShortCircuit(t *testing.T) {
	t.Run("JS: Decision 2: Windows returns the base message even with a Codex ancestor", func(t *testing.T) {
		base := "not running inside Herdr (HERDR_ENV != 1); refusing to control a session from outside"
		env := fakeEnvironment(t, "herdr", []fakeRule{{[]string{"api", "snapshot"}, `{"result":{"snapshot":{"panes":[]}}}`}})
		got := DiagnoseOutsideHerdr(base, env, "win32", 5000, []Process{{PID: 3000, Name: "codex"}})
		if got != base {
			t.Fatalf("message=%q want base %q", got, base)
		}
		if _, err := os.Stat(filepath.Join(env["SOHO_FAKECLI_CONFIG"], "herdr.calls.jsonl")); !os.IsNotExist(err) {
			t.Fatalf("Windows short-circuit recorded a Herdr invocation: %v", err)
		}
	})
}

// Mutation captured: Go whitespace trimming misses the BOM and multiline arrays consume the next policy table.
func TestCodexPolicyBOMAndArrayHeaderBoundary(t *testing.T) {
	t.Run("BOM is trimmed as JavaScript trim does", func(t *testing.T) {
		p := ParseCodexPolicy("\ufeff[shell_environment_policy]\r\ninherit = 'Core'\r\n")
		got := EvaluateCodexPolicy(p)
		if !got.Drops || got.Reason != `inherit="core"` {
			t.Fatalf("got policy=%#v evaluation=%+v", p, got)
		}
	})
	t.Run("multiline array does not consume following table", func(t *testing.T) {
		input := "[shell_environment_policy]\nexclude = [\n \"PATH\"\n[shell_environment_policy.set]\nHERDR_ENV = \"1\"\n"
		if p := ParseCodexPolicy(input); p == nil || !reflect.DeepEqual(p.Set, []string{"HERDR_ENV"}) {
			t.Fatalf("policy: %#v", p)
		}
	})
}

// Mutation captured: dropping required variables, key normalization, duplicate checks, or policy precedence changes these warnings.
func TestCodexPolicyEvaluationAndDuplicateContracts(t *testing.T) {
	tests := []struct {
		name, input, reason string
	}{
		{"uppercase policy key is case insensitive", "[shell_environment_policy]\nINHERIT = 'core'\n", `inherit="core"`},
		{"include only preserves earlier denial reason", "[shell_environment_policy]\ninherit='core'\ninclude_only=['PATH']\n", `inherit="core"`},
		{"all three required variables are checked", "[shell_environment_policy]\ninclude_only=['HERDR_ENV','HERDR_PANE_ID']\n", "include_only does not match HERDR_WORKSPACE_ID"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateCodexPolicy(ParseCodexPolicy(tc.input))
			if !got.Drops || got.Reason != tc.reason {
				t.Fatalf("evaluation %+v, want reason %q", got, tc.reason)
			}
		})
	}
	t.Run("policy set key and set table duplicate definition is invalid", func(t *testing.T) {
		input := "[shell_environment_policy]\nset = {HERDR_ENV='1'}\n[shell_environment_policy.set]\nHERDR_PANE_ID='2'\n"
		if got := ParseCodexPolicy(input); got != nil {
			t.Fatalf("policy %#v", got)
		}
	})
	t.Run("duplicate policy table is invalid", func(t *testing.T) {
		input := "[shell_environment_policy]\ninherit='all'\n[shell_environment_policy]\nexclude=['HERDR_*']\n"
		if got := ParseCodexPolicy(input); got != nil {
			t.Fatalf("policy %#v", got)
		}
	})
	t.Run("home defaults to .codex", func(t *testing.T) {
		home := t.TempDir()
		path := filepath.Join(home, ".codex")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "config.toml"), []byte("[shell_environment_policy]\ninherit='core'\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := EvaluateCodexPolicy(ReadCodexPolicy(platform.Env{"HOME": home}, "darwin")); !got.Drops {
			t.Fatalf("evaluation %+v", got)
		}
	})
}

func TestDuplicateSetKeysAreRejected(t *testing.T) {
	t.Run("JS: duplicate key in set table makes parse null", func(t *testing.T) {
		if p := ParseCodexPolicy("[shell_environment_policy.set]\nHERDR_ENV=\"1\"\nHERDR_ENV=\"2\"\n"); p != nil {
			t.Fatalf("policy: %#v", p)
		}
	})
	t.Run("JS: duplicate key in inline set table makes parse null", func(t *testing.T) {
		if p := ParseCodexPolicy("[shell_environment_policy]\nset={HERDR_ENV=\"1\", HERDR_ENV=\"2\"}\n"); p != nil {
			t.Fatalf("policy: %#v", p)
		}
	})
}

// Mutation captured: replacing the ASCII-only matcher with Unicode case folding changes these matches.
func TestGlobMatch(t *testing.T) {
	cases := []struct {
		name, pattern, value string
		want                 bool
	}{
		{"JS: globMatch: case-insensitive with * and ?", "HERDR_*", "herdr_ENV", true},
		{"JS: globMatch: special regexp characters stay literal", "a+b", "A+B", true},
		{"JS: globMatch: non-ASCII long s does not fold to ASCII", "s", "ſ", false},
		{"JS: globMatch: Kelvin sign does not fold to ASCII", "k", "K", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := GlobMatch(tc.pattern, tc.value); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestDoctorWarnings(t *testing.T) {
	t.Run("JS: Decision 1 case: missing file (produces no warning)", func(t *testing.T) {
		root := t.TempDir()
		var got []string
		DoctorCodexPolicyWarnings(platform.Env{"CODEX_HOME": root}, "darwin", func(s string) { got = append(got, s) })
		if len(got) != 0 {
			t.Fatalf("warnings: %v", got)
		}
	})
	t.Run("JS: Decision 1 case: other content not printed (tokens preserved privately)", func(t *testing.T) {
		root := t.TempDir()
		content := "[api_tokens]\nopenai=\"private token\"\n[shell_environment_policy]\ninherit=\"core\"\n"
		if e := os.WriteFile(filepath.Join(root, "config.toml"), []byte(content), 0600); e != nil {
			t.Fatal(e)
		}
		var got []string
		DoctorCodexPolicyWarnings(platform.Env{"CODEX_HOME": root}, "darwin", func(s string) { got = append(got, s) })
		if len(got) != 1 || got[0] != `codex: shell_environment_policy drops HERDR_* (inherit="core"): commands Codex runs cannot see Herdr; see the Codex section of docs/guide.md` {
			t.Fatalf("warnings: %q", got)
		}
		if reflect.DeepEqual(got, []string{"private token"}) {
			t.Fatal("secret leaked")
		}
	})
}

// Mutation captured: retaining the value assigned to set exposes environment data.
func TestSetValuesAreNotRetained(t *testing.T) {
	t.Run("JS: Decision 1 case: set values are never stored or printed", func(t *testing.T) {
		secret := "SUPER_SECRET_TOKEN_VALUE_XYZ_987"
		p := ParseCodexPolicy("[shell_environment_policy]\ninherit=\"core\"\nset={HERDR_ENV=\"" + secret + "\"}\n")
		if p == nil || !reflect.DeepEqual(p.Set, []string{"HERDR_ENV"}) {
			t.Fatalf("policy %#v", p)
		}
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), secret) {
			t.Fatal("set secret retained")
		}
	})
}

// Mutation captured: adding the starting PID changes the ancestor list and pane match.
func TestGetProcessAncestorsWithFakePS(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX ps ancestry fixtures cannot run as native Windows processes")
	}
	testCases := []struct {
		name  string
		rules []fakeRule
		want  []Process
	}{
		{"JS: ppid zero still includes the process just read", []fakeRule{{[]string{"-o", "ppid=,comm=", "-p", "5000"}, "4000 bash\n"}, {[]string{"-o", "ppid=,comm=", "-p", "4000"}, "0 codex\n"}}, []Process{{4000, "codex"}}},
		{"JS: Decision 2 case: sem ancestral codex", []fakeRule{{[]string{"-o", "ppid=,comm=", "-p", "5000"}, "4000 node\n"}, {[]string{"-o", "ppid=,comm=", "-p", "4000"}, "1 bash\n"}}, []Process{{4000, "bash"}, {1, "init"}}},
		{"JS: Decision 2 case: own pid alone in a pane does not name the pane", []fakeRule{{[]string{"-o", "ppid=,comm=", "-p", "5000"}, "4000 node\n"}, {[]string{"-o", "ppid=,comm=", "-p", "4000"}, "3000 bash\n"}, {[]string{"-o", "ppid=,comm=", "-p", "3000"}, "1 codex\n"}}, []Process{{4000, "bash"}, {3000, "codex"}, {1, "init"}}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			env := fakeEnvironment(t, "ps", tc.rules)
			got := GetProcessAncestors(5000, env, "linux")
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v want %#v", got, tc.want)
			}
		})
	}
}

// Mutation captured: treating an overflowing parent PID as the largest int adds a process outside the supported PID domain.
func TestGetProcessAncestorsIgnoresUnrepresentableParentPID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX ps ancestry fixture cannot run as a native Windows process")
	}
	env := fakeEnvironment(t, "ps", []fakeRule{{
		[]string{"-o", "ppid=,comm=", "-p", "5000"}, "99999999999999999999 codex\n",
	}})
	got := GetProcessAncestors(5000, env, "darwin")
	t.Logf("unrepresentable parent PID ancestors=%#v", got)
	if len(got) != 0 {
		t.Fatalf("got %#v; overflowing parent PID must be ignored", got)
	}
}

// Mutation captured: losing process names, dash normalization, or the 64-ancestor bound changes the returned chain.
func TestGetProcessAncestorsPreservesNamesAndDepthLimit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX ps ancestry fixtures cannot run as native Windows processes")
	}
	t.Run("dash prefix and spaces in comm", func(t *testing.T) {
		env := fakeEnvironment(t, "ps", []fakeRule{
			{[]string{"-o", "ppid=,comm=", "-p", "5000"}, "4000 -bash worker\n"},
			{[]string{"-o", "ppid=,comm=", "-p", "4000"}, "3000 -bash worker\n"},
			{[]string{"-o", "ppid=,comm=", "-p", "3000"}, "0 codex process\n"},
		})
		got := GetProcessAncestors(5000, env, "darwin")
		want := []Process{{4000, "bash worker"}, {3000, "codex process"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v want %#v", got, want)
		}
	})
	t.Run("walk stops at 64 ancestors", func(t *testing.T) {
		rules := make([]fakeRule, 0, 70)
		for pid := 5000; pid > 4930; pid-- {
			rules = append(rules, fakeRule{[]string{"-o", "ppid=,comm=", "-p", strconv.Itoa(pid)}, strconv.Itoa(pid-1) + " node\n"})
		}
		env := fakeEnvironment(t, "ps", rules)
		got := GetProcessAncestors(5000, env, "darwin")
		if len(got) != 63 {
			t.Fatalf("ancestor count %d, want 63", len(got))
		}
	})
}

// Mutation captured: typed JSON decoding loses JavaScript nullish navigation, truthiness, and numeric coercion.
func TestDiagnoseDynamicSnapshotNavigation(t *testing.T) {
	base := "not running inside Herdr (HERDR_ENV != 1); refusing to control a session from outside"
	ancestor := []Process{{PID: 3000, Name: "codex"}}
	tests := []struct {
		name, snapshot, processInfo string
		wantPane                    bool
	}{
		{"missing panes returns base", `{"result":{}}`, "", false},
		{"numeric pane id uses JavaScript string conversion", `{"result":{"snapshot":{"panes":[{"pane_id":7,"agent":"codex"}]}}}`, `{"result":{"process_info":{"foreground_processes":[{"pid":3000.0}]}}}`, true},
		{"truthy string remote is filtered", `{"result":{"snapshot":{"panes":[{"pane_id":"p1","agent":"codex","remote":"yes"}]}}}`, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if runtime.GOOS == "windows" && tc.name != "missing panes returns base" {
				t.Skip("POSIX diagnostic fixture cannot run as a native Windows process")
			}
			rules := []fakecli.Rule{{Argv: []string{"api", "snapshot"}, Stdout: tc.snapshot}}
			if tc.processInfo != "" {
				rules = append(rules, fakecli.Rule{Argv: []string{"pane", "process-info", "--pane", "7"}, Stdout: tc.processInfo})
			}
			env := diagnosisEnvWithRules(t, rules)
			got := DiagnoseOutsideHerdr(base, env, "linux", 5000, ancestor)
			if strings.Contains(got, "Herdr pane local/7") != tc.wantPane {
				t.Fatalf("message: %s", got)
			}
			if tc.name == "missing panes returns base" && got != base {
				t.Fatalf("got %q want base %q", got, base)
			}
		})
	}
	t.Run("numeric agent follows JavaScript method failure", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("POSIX diagnostic fixture cannot run as a native Windows process")
		}
		env := diagnosisEnvWithRules(t, []fakecli.Rule{{Argv: []string{"api", "snapshot"}, Stdout: `{"result":{"snapshot":{"panes":[{"pane_id":"p1","agent":5}]}}}`}})
		defer func() {
			if recover() == nil {
				t.Fatal("expected non-string agent to fail like JavaScript toLowerCase")
			}
		}()
		DiagnoseOutsideHerdr(base, env, "linux", 5000, ancestor)
	})
}

// Mutation captured: case-sensitive matching or skipping remote/machine panes produces a false pane match.
func TestDiagnosePaneFiltersAndAncestorCase(t *testing.T) {
	base := "outside"
	cases := []struct {
		name, pane string
		ancestor   Process
		wantPane   bool
	}{
		{"remote string is truthy and filtered", `{"pane_id":"p1","agent":"codex","remote":"yes"}`, Process{3000, "codex"}, false},
		{"remote numeric zero is false", `{"pane_id":"p1","agent":"codex","remote":0.0}`, Process{3000, "codex"}, true},
		{"machine string is filtered", `{"pane_id":"p1","agent":"codex","machine":"host"}`, Process{3000, "codex"}, false},
		{"machine negative zero is false", `{"pane_id":"p1","agent":"codex","machine":-0}`, Process{3000, "codex"}, true},
		{"agent comparison ignores ASCII case", `{"pane_id":"p1","agent":"Codex"}`, Process{3000, "CODEX"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("POSIX diagnostic fixture cannot run as a native Windows process")
			}
			rules := []fakecli.Rule{
				{Argv: []string{"api", "snapshot"}, Stdout: `{"result":{"snapshot":{"panes":[` + tc.pane + `]}}}`},
				{Argv: []string{"pane", "process-info", "--pane", "p1"}, Stdout: `{"result":{"process_info":{"foreground_processes":[{"pid":3000}]}}}`},
			}
			env := diagnosisEnvWithRules(t, rules)
			got := DiagnoseOutsideHerdr(base, env, "linux", 5000, []Process{tc.ancestor})
			if strings.Contains(got, "Herdr pane local/p1") != tc.wantPane {
				t.Fatalf("message %q", got)
			}
		})
	}
	t.Run("win32 returns before Herdr probing", func(t *testing.T) {
		env := diagnosisEnvWithRules(t, []fakecli.Rule{{Argv: []string{"api", "snapshot"}, Stdout: `{}`}})
		dir := env["SOHO_FAKECLI_CONFIG"]
		if runtime.GOOS != "windows" {
			if err := os.Symlink(filepath.Join(dir, "herdr"), filepath.Join(dir, "herdr.exe")); err != nil {
				t.Fatal(err)
			}
		}
		if _, ok := platform.FindExecutable("herdr", env, "win32"); !ok {
			t.Fatal("Windows fake executable was not resolvable")
		}
		if got := DiagnoseOutsideHerdr(base, env, "win32", 5000, []Process{{3000, "codex"}}); got != base {
			t.Fatalf("message %q", got)
		}
		calls, err := fakecli.ReadCallsForConfig(filepath.Join(env["SOHO_FAKECLI_CONFIG"], "herdr.json"))
		if err == nil && len(calls) != 0 {
			t.Fatalf("Herdr was probed: %#v", calls)
		}
	})
}

func diagnosisEnvWithRules(t *testing.T, rules []fakecli.Rule) platform.Env {
	t.Helper()
	dir := t.TempDir()
	if _, err := fakecli.Install(t, dir, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{}
	for _, item := range fakecli.Env(testutil.CleanEnv(t), dir) {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			env[key] = value
		}
	}
	return env
}

func TestDiagnoseOutsideHerdr(t *testing.T) {
	base := "not running inside Herdr (HERDR_ENV != 1); refusing to control a session from outside"
	if runtime.GOOS == "windows" {
		// Mutation captured: removing the win32 shortcut launches Herdr probes.
		t.Run("JS: win32 returns the base message without process or Herdr probes", func(t *testing.T) {
			env := diagnosisEnvironment(t, []string{"w14:p1"}, map[string][]int{"w14:p1": {3000}})
			if got := DiagnoseOutsideHerdr(base, env, "win32", 5000, []Process{{4000, "codex"}}); got != base {
				t.Fatal(got)
			}
			calls, err := fakecli.ReadCallsForConfig(filepath.Join(env["SOHO_FAKECLI_CONFIG"], "herdr.json"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if len(calls) != 0 {
				t.Fatalf("Win32 diagnosis ran Herdr probes: %#v", calls)
			}
		})
		return
	}
	t.Run("JS: Decision 2 case: sem ancestral codex (nenhuma chamada ao Herdr)", func(t *testing.T) {
		env := diagnosisEnvironment(t, nil, nil)
		if got := DiagnoseOutsideHerdr(base, env, "darwin", 5000, []Process{{4000, "bash"}}); got != base {
			t.Fatal(got)
		}
		calls, err := fakecli.ReadCallsForConfig(filepath.Join(env["SOHO_FAKECLI_CONFIG"], "herdr.json"))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if len(calls) != 0 {
			t.Fatalf("unexpected Herdr calls: %#v", calls)
		}
	})
	t.Run("JS: Decision 2 case: um painel casa (local/<pane>)", func(t *testing.T) {
		panes := []string{"w14:p1"}
		infos := map[string][]int{"w14:p1": {3000}}
		env := diagnosisEnvironment(t, panes, infos)
		got := DiagnoseOutsideHerdr(base, env, "darwin", 5000, []Process{{3000, "codex"}})
		if !strings.Contains(got, "Herdr pane local/w14:p1") {
			t.Fatal(got)
		}
	})
	t.Run("JS: Decision 2 case: dois paineis casam", func(t *testing.T) {
		panes := []string{"w14:p1", "w14:p2"}
		infos := map[string][]int{"w14:p1": {3000}, "w14:p2": {3000}}
		env := diagnosisEnvironment(t, panes, infos)
		got := DiagnoseOutsideHerdr(base, env, "darwin", 5000, []Process{{3000, "codex"}})
		if got != base+" (a Codex ancestor was found, but no single Herdr pane matched it)" {
			t.Fatal(got)
		}
	})
	t.Run("JS: Decision 2 case: nenhum painel casa", func(t *testing.T) {
		panes := []string{"w14:p1"}
		infos := map[string][]int{"w14:p1": {9999}}
		env := diagnosisEnvironment(t, panes, infos)
		got := DiagnoseOutsideHerdr(base, env, "darwin", 5000, []Process{{3000, "codex"}})
		if got != base+" (a Codex ancestor was found, but no single Herdr pane matched it)" {
			t.Fatal(got)
		}
	})
	t.Run("JS: Decision 2 case: own pid alone in a pane does not name the pane", func(t *testing.T) {
		panes := []string{"w14:pWRONG"}
		infos := map[string][]int{"w14:pWRONG": {5000}}
		env := diagnosisEnvironment(t, panes, infos)
		got := DiagnoseOutsideHerdr(base, env, "darwin", 5000, []Process{{3000, "codex"}})
		if strings.Contains(got, "pWRONG") {
			t.Fatal(got)
		}
	})
}

func diagnosisEnvironment(t *testing.T, panes []string, infos map[string][]int) platform.Env {
	t.Helper()
	dir := t.TempDir()
	var p []string
	for _, id := range panes {
		p = append(p, `{"pane_id":"`+id+`","agent":"codex"}`)
	}
	rules := []fakecli.Rule{{Argv: []string{"api", "snapshot"}, Stdout: `{"result":{"snapshot":{"panes":[` + strings.Join(p, ",") + `]}}}`}}
	for id, pids := range infos {
		var entries []string
		for _, pid := range pids {
			entries = append(entries, `{"pid":`+strconv.Itoa(pid)+`}`)
		}
		rules = append(rules, fakecli.Rule{Argv: []string{"pane", "process-info", "--pane", id}, Stdout: `{"result":{"process_info":{"foreground_processes":[` + strings.Join(entries, ",") + `]}}}`})
	}
	if _, err := fakecli.Install(t, dir, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	list := fakecli.Env(testutil.CleanEnv(t), dir)
	env := platform.Env{}
	for _, v := range list {
		k, value, ok := strings.Cut(v, "=")
		if ok {
			env[k] = value
		}
	}
	return env
}

type fakeRule struct {
	argv   []string
	stdout string
}

func TestMain(m *testing.M) { fakecli.RunTests(m) }
func fakeEnvironment(t *testing.T, name string, rules []fakeRule) platform.Env {
	t.Helper()
	dir := t.TempDir()
	converted := make([]fakecli.Rule, 0, len(rules))
	for _, r := range rules {
		converted = append(converted, fakecli.Rule{Argv: r.argv, Stdout: r.stdout})
	}
	if _, err := fakecli.Install(t, dir, name, converted); err != nil {
		t.Fatal(err)
	}
	list := fakecli.Env(testutil.CleanEnv(t), dir)
	env := platform.Env{}
	for _, entry := range list {
		k, v, ok := strings.Cut(entry, "=")
		if ok {
			env[k] = v
		}
	}
	return env
}
