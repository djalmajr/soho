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

// TestPiOwnModelsRejectNullProvider: a null provider is malformed, not a
// provider without models, so it keeps rejecting the whole list (DJA-677).
func TestPiOwnModelsRejectNullProvider(t *testing.T) {
	home := t.TempDir()
	file := filepath.Join(home, ".pi", "agent", "models.json")
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(`{"providers":{"bad":null,"good":{"models":[{"id":"m"}]}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got := PiOwnModels(platform.Env{"HOME": home, "USERPROFILE": home}); len(got) != 0 {
		t.Fatalf("got %#v", got)
	}
}
