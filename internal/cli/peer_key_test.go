package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/peerauth"
	"github.com/djalmajr/soho/internal/platform"
)

var hex64RE = regexp.MustCompile(`[0-9a-fA-F]{64}`)

func TestPeerKeyNewCreatesTheFileAndPrintsOnlyThePath(t *testing.T) {
	env, cwd := commandFixture(t)
	want := filepath.Join(env["XDG_CONFIG_HOME"], "soho", "peer-keys", "mac.key")
	code, out, stderr := runIn(t, []string{"peer-key", "new", "mac"}, env, cwd)
	if code != 0 || out != want+"\n" || stderr != "" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if hex64RE.MatchString(out) || !filepath.IsAbs(strings.TrimSpace(out)) {
		t.Fatalf("stdout must be only an absolute path: %q", out)
	}
	raw, err := os.ReadFile(want)
	if err != nil || len(raw) != 65 || raw[64] != '\n' || !hex64RE.Match(raw[:64]) || strings.ToLower(string(raw)) != string(raw) {
		t.Fatalf("key file = %d bytes, err=%v", len(raw), err)
	}
	if _, status := peerauth.ReadKey(filepath.Join(env["XDG_CONFIG_HOME"], "soho"), "mac"); status != peerauth.KeyOK {
		t.Fatalf("the created key does not read back: %v", status)
	}
	if runtime.GOOS != "windows" {
		file, _ := os.Stat(want)
		dir, _ := os.Stat(filepath.Dir(want))
		if file.Mode().Perm() != 0o600 || dir.Mode().Perm() != 0o700 {
			t.Fatalf("modes: file %v dir %v", file.Mode().Perm(), dir.Mode().Perm())
		}
	}
	// The key value is in no output of the command.
	if strings.Contains(out+stderr, strings.TrimSpace(string(raw))) {
		t.Fatal("the key value was printed")
	}
}

func TestPeerKeyNewRefusesAnExistingFile(t *testing.T) {
	env, cwd := commandFixture(t)
	if code, _, _ := runIn(t, []string{"peer-key", "new", "mac"}, env, cwd); code != 0 {
		t.Fatal("first new failed")
	}
	path := filepath.Join(env["XDG_CONFIG_HOME"], "soho", "peer-keys", "mac.key")
	before, _ := os.ReadFile(path)
	code, out, stderr := runIn(t, []string{"peer-key", "new", "mac"}, env, cwd)
	if code != 1 || out != "" || stderr != "soho: peer-key: "+path+" already exists; delete it to rotate the key\n" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("an existing key was overwritten")
	}
	if strings.Contains(stderr, strings.TrimSpace(string(before))) {
		t.Fatal("the existing key value was printed")
	}
}

func TestPeerKeyNewRefusesInvalidNames(t *testing.T) {
	for _, name := range []string{"../x", "a/b", "..", "", strings.Repeat("a", 65), "C:x", `a\b`, "-x", ".x", "a b"} {
		t.Run(name, func(t *testing.T) {
			env, cwd := commandFixture(t)
			code, out, stderr := runIn(t, []string{"peer-key", "new", name}, env, cwd)
			if code != 2 || out != "" || !strings.Contains(stderr, "peer-key: invalid name") {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
			if _, err := os.Stat(filepath.Join(env["XDG_CONFIG_HOME"], "soho")); !os.IsNotExist(err) {
				t.Fatalf("an invalid name created files: %v", err)
			}
		})
	}
}

func TestPeerKeyUsageAndReadOnly(t *testing.T) {
	env, cwd := commandFixture(t)
	for _, args := range [][]string{{"peer-key"}, {"peer-key", "list"}, {"peer-key", "new"}, {"peer-key", "new", "a", "b"}, {"peer-key", "rotate", "mac"}} {
		code, out, stderr := runIn(t, args, env, cwd)
		if code != 2 || out != "" || !strings.Contains(stderr, "usage: peer-key new <name>") {
			t.Fatalf("%v: code=%d out=%q stderr=%q", args, code, out, stderr)
		}
	}
	env["SOHO_NOWRITE"] = "1"
	code, _, stderr := runIn(t, []string{"peer-key", "new", "mac"}, env, cwd)
	if code != 2 || !strings.Contains(stderr, "SOHO_NOWRITE=1 is read-only") {
		t.Fatalf("under NOWRITE: code=%d stderr=%q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(env["XDG_CONFIG_HOME"], "soho")); !os.IsNotExist(err) {
		t.Fatal("NOWRITE created a key")
	}
}

func TestPeerKeyIsKnownAndDocumented(t *testing.T) {
	if !knownCommands["peer-key"] {
		t.Fatal("peer-key is not a known command")
	}
	if help := commandHelp("peer-key"); !strings.Contains(help, "soho peer-key new <name>") {
		t.Fatalf("help = %q", help)
	}
	if help := commandHelp("hook"); !strings.Contains(help, "reminder|doctor|inbound") {
		t.Fatalf("hook help = %q", help)
	}
}

func TestConfigGetAnswersTheInboundKeysFromTheUserFileOnly(t *testing.T) {
	env, cwd := commandFixture(t)
	user := platform.UserConfigPath(platform.Current(), env)
	get := func(key string) string {
		t.Helper()
		code, out, errOut := runIn(t, []string{"config", "get", key}, env, cwd)
		if code != 0 || errOut != "" {
			t.Fatalf("config get %s: code=%d err=%q", key, code, errOut)
		}
		return out
	}
	if get("inbound_auth") != "off\n" || get("inbound_allow") != "\n" {
		t.Fatalf("defaults: %q %q", get("inbound_auth"), get("inbound_allow"))
	}
	writeConfigFile(t, user, "inbound_auth=warn\ninbound_allow=mac|pc\n")
	writeConfigFile(t, filepath.Join(cwd, ".agents", "soho.conf"), "inbound_auth=off\ninbound_allow=evil\n")
	env["SOHO_INBOUND_AUTH"], env["SOHO_INBOUND_ALLOW"] = "off", "evil"
	if got := get("inbound_auth"); got != "warn\n" {
		t.Fatalf("inbound_auth = %q", got)
	}
	if got := get("inbound_allow"); got != "mac|pc\n" {
		t.Fatalf("inbound_allow = %q", got)
	}
	// An invalid value prints as written; the receiver applies it as enforce.
	delete(env, "SOHO_INBOUND_AUTH")
	writeConfigFile(t, user, "inbound_auth=sure\n")
	if got := get("inbound_auth"); got != "sure\n" {
		t.Fatalf("invalid inbound_auth = %q", got)
	}
	// The listing is the one the frozen oracle pins: the new keys are not rows.
	code, out, _ := runIn(t, []string{"config"}, env, cwd)
	if code != 0 || strings.Contains(out, "inbound_auth") || strings.Contains(out, "inbound_allow") {
		t.Fatalf("config listing shows the inbound keys: code=%d", code)
	}
	// And nothing of the key files shows up in it.
	if code, _, _ := runIn(t, []string{"peer-key", "new", "mac"}, env, cwd); code != 0 {
		t.Fatal("peer-key new failed")
	}
	raw, _ := os.ReadFile(filepath.Join(filepath.Dir(user), "peer-keys", "mac.key"))
	_, out, _ = runIn(t, []string{"config"}, env, cwd)
	if strings.Contains(out, strings.TrimSpace(string(raw))) || strings.Contains(out, "peer-keys") {
		t.Fatal("config shows something of the key files")
	}
}

func TestConfigSetAcceptsTheInboundKeys(t *testing.T) {
	env, cwd := commandFixture(t)
	code, out, errOut := runIn(t, []string{"config", "set", "--user", "inbound_auth=warn"}, env, cwd)
	if code != 0 || errOut != "" || !strings.Contains(out, "set inbound_auth=warn in ") {
		t.Fatalf("config set: code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, _ = runIn(t, []string{"config", "get", "inbound_auth"}, env, cwd)
	if code != 0 || out != "warn\n" {
		t.Fatalf("get after set: %d %q", code, out)
	}
}
