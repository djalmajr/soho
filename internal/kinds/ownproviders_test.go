package kinds

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

func TestPiOwnModelsExposeIdsAndEffortOnly(t *testing.T) {
	home := t.TempDir()
	file := filepath.Join(home, ".pi", "agent", "models.json")
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	content := `{"providers":{"vendor":{"apiKey":"sk-live-DO_NOT_COPY","models":[{"id":"m","thinkingLevelMap":{"low":"low","max":"max"}}]}}}`
	if err := os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	got := PiOwnModels(platform.Env{"HOME": home, "USERPROFILE": home})
	if len(got) != 1 || got[0].ID != "vendor/m" || got[0].MaxEffort != "max" {
		t.Fatalf("got %#v", got)
	}
	if strings.Contains(strings.Join(got[0].Warnings, " "), "DO_NOT_COPY") {
		t.Fatal("credential leaked")
	}
}

// TestOwnProviderKeyReferences: pi "$NAME" and "!command", opencode "{env:NAME}"
// and "{file:path}" are references; anything else present is a literal key.
func TestOwnProviderKeyReferences(t *testing.T) {
	literal := func(ws []string) bool { return strings.Contains(strings.Join(ws, " "), "literal apiKey") }
	for _, tc := range []struct {
		key  string
		want bool
	}{{"$MY_API_KEY", false}, {"!my-key-helper", false}, {"!", true}, {"! ", true}, {"sk-live-DO_NOT_COPY", true}} {
		home := t.TempDir()
		file := filepath.Join(home, ".pi", "agent", "models.json")
		_ = os.MkdirAll(filepath.Dir(file), 0o700)
		_ = os.WriteFile(file, []byte(`{"providers":{"vendor":{"apiKey":"`+tc.key+`","models":[{"id":"m"}]}}}`), 0o600)
		got := PiOwnModels(platform.Env{"HOME": home, "USERPROFILE": home})
		if len(got) != 1 || literal(got[0].Warnings) != tc.want {
			t.Fatalf("pi apiKey %q: literal=%v want %v (%#v)", tc.key, len(got) == 1 && literal(got[0].Warnings), tc.want, got)
		}
	}
	for _, tc := range []struct {
		key  string
		want bool
	}{{"{env:MY_API_KEY}", false}, {"{file:~/.config/keys/vendor}", false}, {"{file:}", true}, {"{file: x}", true}, {"{env:1BAD}", true}, {"sk-live-DO_NOT_COPY", true}} {
		root := t.TempDir()
		_ = os.WriteFile(filepath.Join(root, "opencode.json"), []byte(`{"provider":{"vendor":{"options":{"apiKey":"`+tc.key+`"},"models":{"m":{}}}}}`), 0o600)
		_ = os.Mkdir(filepath.Join(root, ".git"), 0o700)
		home := t.TempDir()
		got := OpencodeOwnModels(platform.Env{"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": filepath.Join(home, "xdg")}, root)
		if len(got) != 1 || literal(got[0].Warnings) != tc.want {
			t.Fatalf("opencode apiKey %q: literal=%v want %v (%#v)", tc.key, len(got) == 1 && literal(got[0].Warnings), tc.want, got)
		}
	}
}

// TestPiOwnModelsSkipProviderWithoutModels: a provider that only holds
// modelOverrides declares no model of its own, and must not hide the models of
// the others, wherever it sits in the file (DJA-677).
func TestPiOwnModelsSkipProviderWithoutModels(t *testing.T) {
	const (
		vendor    = `"vendor":{"models":[{"id":"m"}]}`
		local     = `"local":{"models":[{"id":"n"}]}`
		overrides = `"opencode":{"modelOverrides":{"x":{"reasoning":true}}}`
	)
	for _, tc := range []struct {
		name, providers string
		want            []string
	}{
		{"first", overrides + "," + vendor + "," + local, []string{"vendor/m", "local/n"}},
		{"middle", vendor + "," + overrides + "," + local, []string{"vendor/m", "local/n"}},
		{"last", vendor + "," + local + "," + overrides, []string{"vendor/m", "local/n"}},
		{"alone", overrides, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			file := filepath.Join(home, ".pi", "agent", "models.json")
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(`{"providers":{`+tc.providers+`}}`), 0600); err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, model := range PiOwnModels(platform.Env{"HOME": home, "USERPROFILE": home}) {
				got = append(got, model.ID)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestOwnProviderDoctorLinesSurviveProviderWithoutModels: the literal key
// warning of one provider still comes out next to a provider without models,
// and never carries the key (DJA-677).
func TestOwnProviderDoctorLinesSurviveProviderWithoutModels(t *testing.T) {
	home := t.TempDir()
	file := filepath.Join(home, ".pi", "agent", "models.json")
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	content := `{"providers":{"opencode":{"modelOverrides":{"x":{"reasoning":true}}},"vendor":{"apiKey":"sk-live-DO_NOT_COPY","models":[{"id":"m"}]}}}`
	if err := os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	found, warnings := OwnProviderDoctorLines(nil, platform.Env{"HOME": home, "USERPROFILE": home}, t.TempDir())
	joined := strings.Join(warnings, "\n")
	if !found || !strings.Contains(joined, "own provider 'vendor' (pi) has a literal apiKey") {
		t.Fatalf("found=%t warnings=%q", found, joined)
	}
	if strings.Contains(joined, "DO_NOT_COPY") {
		t.Fatal("credential leaked")
	}
}

// TestPiOwnModelsSkipMalformed: a malformed provider or item is skipped and
// the others stay listed, wherever it sits in the file; the literal-key
// warning of another provider keeps coming out without the key value
// (DJA-655 decision 5, scenario 6; supersedes the reject-the-whole-list
// contract of the former DJA-677 null-provider case).
func TestPiOwnModelsSkipMalformed(t *testing.T) {
	const (
		good  = `"good":{"models":[{"id":"m"}]}`
		goodW = `"warn":{"apiKey":"sk-live-DO_NOT_COPY","models":[{"id":"w"}]}`
	)
	cases := []struct {
		name    string
		bad     string
		keepBad []string // models of the malformed provider that must survive
	}{
		{"null provider", `"bad":null`, nil},
		{"non-object provider", `"bad":"nope"`, nil},
		{"models not a list", `"bad":{"models":"nope"}`, nil},
		{"models entry not an object", `"bad":{"models":[42]}`, nil},
		{"thinkingLevelMap not an object", `"bad":{"models":[{"id":"b","thinkingLevelMap":"high"},{"id":"ok"}]}`, []string{"bad/ok"}},
	}
	for _, c := range cases {
		for _, tc := range []struct {
			name string
			body string
			want []string
		}{
			{"first", c.bad + "," + good, append(append([]string{}, c.keepBad...), "good/m")},
			{"middle", good + "," + c.bad + "," + goodW, append(append([]string{"good/m"}, c.keepBad...), "warn/w")},
			{"last", goodW + "," + c.bad, append([]string{"warn/w"}, c.keepBad...)},
		} {
			t.Run(c.name+" "+tc.name, func(t *testing.T) {
				home := t.TempDir()
				file := filepath.Join(home, ".pi", "agent", "models.json")
				if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte(`{"providers":{`+tc.body+`}}`), 0o600); err != nil {
					t.Fatal(err)
				}
				got := PiOwnModels(platform.Env{"HOME": home, "USERPROFILE": home})
				var ids []string
				var out strings.Builder
				for _, m := range got {
					ids = append(ids, m.ID)
					for _, w := range m.Warnings {
						out.WriteString(w + " ")
					}
				}
				if strings.Join(ids, ",") != strings.Join(tc.want, ",") {
					t.Fatalf("got %v, want %v", ids, tc.want)
				}
				if strings.Contains(out.String(), "DO_NOT_COPY") {
					t.Fatal("credential leaked")
				}
				wantWarn := strings.Contains(tc.body, goodW)
				gotWarn := strings.Contains(out.String(), "own provider 'warn' (pi) has a literal apiKey")
				if wantWarn != gotWarn {
					t.Fatalf("literal warning presence: want %v, out=%q", wantWarn, out.String())
				}
			})
		}
	}

	// A non-object entry inside one provider's models list, before, middle
	// and after valid models of the same provider: the valid models
	// survive, each with the provider's literal-key warning, and the key
	// value never comes out (DJA-655 amendment P2).
	for _, tc := range []struct {
		name, list string
	}{
		{"entry before valid models", `[42, {"id": "a"}, {"id": "b"}]`},
		{"entry between valid models", `[{"id": "a"}, null, {"id": "b"}]`},
		{"entry after valid models", `[{"id": "a"}, {"id": "b"}, "oops"]`},
	} {
		t.Run("models entry not an object "+tc.name, func(t *testing.T) {
			home := t.TempDir()
			file := filepath.Join(home, ".pi", "agent", "models.json")
			if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(`{"providers":{"p":{"apiKey":"sk-live-DO_NOT_COPY","models":`+tc.list+`}}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			got := PiOwnModels(platform.Env{"HOME": home, "USERPROFILE": home})
			var ids []string
			var out strings.Builder
			for _, m := range got {
				ids = append(ids, m.ID)
				for _, w := range m.Warnings {
					out.WriteString(w + " ")
				}
			}
			if strings.Join(ids, ",") != "p/a,p/b" {
				t.Fatalf("got %v, want [p/a p/b]", ids)
			}
			if n := strings.Count(out.String(), "own provider 'p' (pi) has a literal apiKey"); n != 2 {
				t.Fatalf("literal-key warning count=%d, want 2 (one per surviving model), out=%q", n, out.String())
			}
			if strings.Contains(out.String(), "DO_NOT_COPY") {
				t.Fatal("credential leaked")
			}
		})
	}
}

func ownOpenFixture(t *testing.T) (root, home string, env platform.Env) {
	t.Helper()
	root = t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	home = t.TempDir()
	env = platform.Env{"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": filepath.Join(home, "xdg")}
	return root, home, env
}

// TestOpencodeOwnModelsV2Format: the current schema format (providers,
// settings.apiKey) is read from each config root (DJA-655, scenarios 1 and 4
// plus the edge cases of the decision table).
func TestOpencodeOwnModelsV2Format(t *testing.T) {
	write := func(t *testing.T, root, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "opencode.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("scenario 1: a V2-only file lists its models", func(t *testing.T) {
		root, _, env := ownOpenFixture(t)
		write(t, root, `{"providers":{"p":{"settings":{"apiKey":"{env:KEY}"},"models":{"m":{}}}}}`)
		got := OpencodeOwnModels(env, root)
		if len(got) != 1 || got[0].ID != "p/m" || len(got[0].Warnings) != 0 {
			t.Fatalf("got %#v", got)
		}
	})
	t.Run("scenario 4: a literal settings.apiKey warns without the value", func(t *testing.T) {
		root, _, env := ownOpenFixture(t)
		write(t, root, `{"providers":{"p":{"settings":{"apiKey":"sk-v2-DO_NOT_COPY"},"models":{"m":{}}}}}`)
		got := OpencodeOwnModels(env, root)
		out := ownModelOut(got)
		if len(got) != 1 || !strings.Contains(out, "own provider 'p' (opencode) has a literal apiKey") {
			t.Fatalf("got %#v", got)
		}
		if strings.Contains(out, "sk-v2-DO_NOT_COPY") {
			t.Fatal("credential leaked")
		}
	})
	t.Run("scenario 4: an {env:KEY} settings.apiKey does not warn", func(t *testing.T) {
		root, _, env := ownOpenFixture(t)
		write(t, root, `{"providers":{"p":{"settings":{"apiKey":"{env:KEY}"},"models":{"m":{}}}}}`)
		got := OpencodeOwnModels(env, root)
		if len(got) != 1 || len(got[0].Warnings) != 0 {
			t.Fatalf("got %#v", got)
		}
	})
	t.Run("edge: settings absent: no key warning", func(t *testing.T) {
		root, _, env := ownOpenFixture(t)
		write(t, root, `{"providers":{"p":{"models":{"m":{}}}}}`)
		got := OpencodeOwnModels(env, root)
		if len(got) != 1 || got[0].ID != "p/m" || len(got[0].Warnings) != 0 {
			t.Fatalf("got %#v", got)
		}
	})
	t.Run("edge: models absent in a V2 provider: it lists nothing", func(t *testing.T) {
		root, _, env := ownOpenFixture(t)
		write(t, root, `{"providers":{"p":{"settings":{"apiKey":"sk-v2-DO_NOT_COPY"}}}}`)
		if got := OpencodeOwnModels(env, root); len(got) != 0 {
			t.Fatalf("got %#v", got)
		}
	})
	t.Run("edge: providers not an object and a valid provider: the provider is read", func(t *testing.T) {
		root, _, env := ownOpenFixture(t)
		write(t, root, `{"providers":[],"provider":{"p":{"options":{"apiKey":"{env:KEY}"},"models":{"m":{"options":{"thinking_token_budget":16000}}}}}}`)
		got := OpencodeOwnModels(env, root)
		if len(got) != 1 || got[0].ID != "p/m" || len(got[0].Warnings) != 0 {
			t.Fatalf("got %#v", got)
		}
	})
}

// TestOpencodeOwnModelsBothFormats: a model present in both formats counts
// once, and the current format wins because the deprecated one (provider)
// loses (DJA-655, scenario 3); the key check of each format keeps applying
// even when its model was counted by the other format (scenario 5).
func TestOpencodeOwnModelsBothFormats(t *testing.T) {
	t.Run("scenario 3: the same model in both formats counts once and the V2 wins", func(t *testing.T) {
		root, _, env := ownOpenFixture(t)
		// The V1 model carries no thinking_token_budget: if the V1 entry won
		// it would come out with the missing-budget warning.
		body := `{"provider":{"p":{"options":{"apiKey":"{env:KEY}"},"models":{"m":{}}}},"providers":{"p":{"settings":{"apiKey":"{env:KEY}"},"models":{"m":{}}}}}`
		if err := os.WriteFile(filepath.Join(root, "opencode.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		got := OpencodeOwnModels(env, root)
		if len(got) != 1 || got[0].ID != "p/m" || len(got[0].Warnings) != 0 {
			t.Fatalf("got %#v", got)
		}
	})
	t.Run("scenario 5: a V1 literal key still warns when its only model is also in the V2", func(t *testing.T) {
		root, _, env := ownOpenFixture(t)
		body := `{"provider":{"p":{"options":{"apiKey":"sk-v1-DO_NOT_COPY"},"models":{"m":{"options":{"thinking_token_budget":12000}}}}},"providers":{"p":{"settings":{"apiKey":"{env:KEY}"},"models":{"m":{}}}}}`
		if err := os.WriteFile(filepath.Join(root, "opencode.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		got := OpencodeOwnModels(env, root)
		out := ownModelOut(got)
		if len(got) != 1 || got[0].ID != "p/m" || !strings.Contains(out, "own provider 'p' (opencode) has a literal apiKey") {
			t.Fatalf("got %#v", got)
		}
		if strings.Contains(out, "sk-v1-DO_NOT_COPY") {
			t.Fatal("credential leaked")
		}
	})
	t.Run("amendment P3: both formats literal in the same file: the warning is attached once", func(t *testing.T) {
		root, _, env := ownOpenFixture(t)
		// The same provider is literal in both formats of the same file: the
		// texts are identical, so the model must carry the warning once.
		body := `{"provider":{"p":{"options":{"apiKey":"sk-v1-DO_NOT_COPY"},"models":{"m":{}}}},"providers":{"p":{"settings":{"apiKey":"sk-v2-DO_NOT_COPY"},"models":{"m":{}}}}}`
		if err := os.WriteFile(filepath.Join(root, "opencode.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		got := OpencodeOwnModels(env, root)
		if len(got) != 1 || got[0].ID != "p/m" {
			t.Fatalf("got %#v", got)
		}
		if n := len(got[0].Warnings); n != 1 {
			t.Fatalf("warnings=%d, want 1 (the identical text attached once), got %#v", n, got[0].Warnings)
		}
		if !strings.Contains(got[0].Warnings[0], "own provider 'p' (opencode) has a literal apiKey") {
			t.Fatalf("warning text: %#v", got[0].Warnings)
		}
		if out := ownModelOut(got); strings.Contains(out, "DO_NOT_COPY") {
			t.Fatal("credential leaked")
		}
	})
	t.Run("amendment edge: literal keys in two different files: both warnings stay", func(t *testing.T) {
		root, home, env := ownOpenFixture(t)
		if err := os.WriteFile(filepath.Join(root, "opencode.json"), []byte(`{"providers":{"p":{"settings":{"apiKey":"sk-a-DO_NOT_COPY"},"models":{"m":{}}}}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		xdgDir := filepath.Join(home, "xdg", "opencode")
		if err := os.MkdirAll(xdgDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(xdgDir, "opencode.json"), []byte(`{"provider":{"p":{"options":{"apiKey":"sk-b-DO_NOT_COPY"},"models":{"m":{}}}}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		got := OpencodeOwnModels(env, root)
		if len(got) != 1 || got[0].ID != "p/m" {
			t.Fatalf("got %#v", got)
		}
		// The texts differ by file, so both stay on the model.
		if n := len(got[0].Warnings); n != 2 {
			t.Fatalf("warnings=%d, want 2 (one per file), got %#v", n, got[0].Warnings)
		}
		for _, w := range got[0].Warnings {
			if !strings.Contains(w, "own provider 'p' (opencode) has a literal apiKey") {
				t.Fatalf("warning text: %#v", w)
			}
		}
		if got[0].Warnings[0] == got[0].Warnings[1] {
			t.Fatalf("both warnings identical: %#v", got[0].Warnings)
		}
		if out := ownModelOut(got); strings.Contains(out, "DO_NOT_COPY") {
			t.Fatal("credential leaked")
		}
	})
}

func ownModelOut(m []OwnModel) string {
	var b strings.Builder
	for _, x := range m {
		b.WriteString(x.ID)
		for _, w := range x.Warnings {
			b.WriteString(" ")
			b.WriteString(w)
		}
	}
	return b.String()
}
