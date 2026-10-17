package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/job"
	"github.com/djalmajr/soho/internal/platform"
)

func writeConfigFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// configListingValue extracts the VALUE column of one `soho config` row:
// the key field is padded to 18, the separator is one space, and the value
// field to 30 (the test values stay shorter than 30).
func configListingValue(t *testing.T, out, key string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 19 || line[18] != ' ' {
			continue
		}
		if strings.TrimRight(line[:18], " ") != key {
			continue
		}
		field := line[19:]
		if len(field) > 30 {
			field = field[:30]
		}
		return strings.TrimRight(field, " ")
	}
	t.Fatalf("config listing has no row for %q", key)
	return ""
}

func TestConfigGetMachineKeyFromUserFileIgnoresProject(t *testing.T) {
	// The job reads the machine keys only from the user file: a project
	// .agents/soho.conf job_wake_cmd is ignored, and config get must print
	// the user file's value, exactly the one the job would use.
	env, cwd := commandFixture(t)
	writeConfigFile(t, platform.UserConfigPath(platform.Current(), env), "job_wake_cmd=wake-from-user\n")
	writeConfigFile(t, filepath.Join(cwd, ".agents", "soho.conf"), "job_wake_cmd=wake-from-project\n")
	code, out, errOut := runIn(t, []string{"config", "get", "job_wake_cmd"}, env, cwd)
	if code != 0 || out != "wake-from-user\n" || errOut != "" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	machine, err := job.LoadMachine(platform.Current(), env)
	if err != nil || machine.WakeCmd != "wake-from-user" {
		t.Fatalf("job machine = %+v, %v", machine, err)
	}
}

func TestConfigGetMachineDefaultsAndRawValues(t *testing.T) {
	env, cwd := commandFixture(t)
	// Absent keys print the job's own defaults.
	code, out, errOut := runIn(t, []string{"config", "get", "job_timeout"}, env, cwd)
	if code != 0 || out != "120\n" || errOut != "" {
		t.Fatalf("job_timeout code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut = runIn(t, []string{"config", "get", "job_blocked_timeout"}, env, cwd)
	if code != 0 || out != "1440\n" || errOut != "" {
		t.Fatalf("job_blocked_timeout code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut = runIn(t, []string{"config", "get", "job_finishing_timeout"}, env, cwd)
	if code != 0 || out != "10\n" || errOut != "" {
		t.Fatalf("job_finishing_timeout code=%d out=%q err=%q", code, out, errOut)
	}
	// The empty defaults print only the newline.
	for _, key := range []string{"machine_label", "job_orgs", "job_wake_cmd"} {
		code, out, errOut = runIn(t, []string{"config", "get", key}, env, cwd)
		if code != 0 || out != "\n" || errOut != "" {
			t.Fatalf("%s code=%d out=%q err=%q", key, code, out, errOut)
		}
	}
	// User values stay raw: the ~ of job_repos_root is not expanded.
	writeConfigFile(t, platform.UserConfigPath(platform.Current(), env), "job_repos_root=~/repos\njob_orgs=example-org, other-org\njob_timeout=90\n")
	code, out, errOut = runIn(t, []string{"config", "get", "job_repos_root"}, env, cwd)
	if code != 0 || out != "~/repos\n" || errOut != "" {
		t.Fatalf("job_repos_root code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut = runIn(t, []string{"config", "get", "job_orgs"}, env, cwd)
	if code != 0 || out != "example-org, other-org\n" || errOut != "" {
		t.Fatalf("job_orgs code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut = runIn(t, []string{"config", "get", "job_timeout"}, env, cwd)
	if code != 0 || out != "90\n" || errOut != "" {
		t.Fatalf("job_timeout user code=%d out=%q err=%q", code, out, errOut)
	}
}

func TestConfigGetAutoCloseKeysFollowTheJobResolution(t *testing.T) {
	env, cwd := commandFixture(t)
	// Absent in user and project: the job's defaults.
	code, out, errOut := runIn(t, []string{"config", "get", "job_auto_cleanup"}, env, cwd)
	if code != 0 || out != "on\n" || errOut != "" {
		t.Fatalf("job_auto_cleanup code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut = runIn(t, []string{"config", "get", "job_human_input_minutes"}, env, cwd)
	if code != 0 || out != "5\n" || errOut != "" {
		t.Fatalf("job_human_input_minutes code=%d out=%q err=%q", code, out, errOut)
	}
	// The project file wins over the user file, as in the job.
	writeConfigFile(t, platform.UserConfigPath(platform.Current(), env), "job_auto_cleanup=off\njob_human_input_minutes=9\n")
	writeConfigFile(t, filepath.Join(cwd, ".agents", "soho.conf"), "job_auto_cleanup=on\n")
	code, out, errOut = runIn(t, []string{"config", "get", "job_auto_cleanup"}, env, cwd)
	if code != 0 || out != "on\n" || errOut != "" {
		t.Fatalf("job_auto_cleanup project code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut = runIn(t, []string{"config", "get", "job_human_input_minutes"}, env, cwd)
	if code != 0 || out != "9\n" || errOut != "" {
		t.Fatalf("job_human_input_minutes user code=%d out=%q err=%q", code, out, errOut)
	}
}

func TestConfigGetMatchesTheListingValueColumn(t *testing.T) {
	env, cwd := commandFixture(t)
	var out string
	writeConfigFile(t, filepath.Join(cwd, ".agents", "soho.conf"), "lane.build.model=gpt-6-luna\n")
	code, listing, errOut := runIn(t, []string{"config"}, env, cwd)
	if code != 0 || errOut != "" {
		t.Fatalf("config code=%d err=%q", code, errOut)
	}
	listModel := configListingValue(t, listing, "lane.build.model")
	code, out, errOut = runIn(t, []string{"config", "get", "lane.build.model"}, env, cwd)
	if code != 0 || out != listModel+"\n" || errOut != "" {
		t.Fatalf("lane.build.model code=%d out=%q listing=%q err=%q", code, out, listModel, errOut)
	}
	if listModel != "gpt-6-luna" {
		t.Fatalf("lane.build.model listing value = %q, want gpt-6-luna", listModel)
	}
	// config get takes exactly the keys config set takes: the underscore
	// spelling of a dotted key is unknown to both.
	code, out, errOut = runIn(t, []string{"config", "get", "lane_build_model"}, env, cwd)
	if code != 2 || out != "" || errOut != "soho: config get: unknown key 'lane_build_model'\n" {
		t.Fatalf("lane_build_model code=%d out=%q err=%q", code, out, errOut)
	}
	// state_dir unset: the listing's special default, not an empty value.
	listState := configListingValue(t, listing, "state_dir")
	if listState != ".soho" {
		t.Fatalf("state_dir listing value = %q, want .soho", listState)
	}
	code, out, errOut = runIn(t, []string{"config", "get", "state_dir"}, env, cwd)
	if code != 0 || out != listState+"\n" || errOut != "" {
		t.Fatalf("state_dir code=%d out=%q listing=%q err=%q", code, out, listState, errOut)
	}
	// state_dir set by the user file.
	writeConfigFile(t, platform.UserConfigPath(platform.Current(), env), "state_dir=.soho-user\n")
	code, listing, errOut = runIn(t, []string{"config"}, env, cwd)
	if code != 0 || errOut != "" {
		t.Fatalf("config user code=%d err=%q", code, errOut)
	}
	listState = configListingValue(t, listing, "state_dir")
	code, out, errOut = runIn(t, []string{"config", "get", "state_dir"}, env, cwd)
	if code != 0 || out != listState+"\n" || errOut != "" {
		t.Fatalf("state_dir user code=%d out=%q listing=%q err=%q", code, out, listState, errOut)
	}
	if listState != ".soho-user" {
		t.Fatalf("state_dir listing value = %q, want .soho-user", listState)
	}
}

func TestConfigGetUnknownKeyAndUsage(t *testing.T) {
	env, cwd := commandFixture(t)
	code, out, errOut := runIn(t, []string{"config", "get", "nope"}, env, cwd)
	if code != 2 || out != "" || errOut != "soho: config get: unknown key 'nope'\n" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut = runIn(t, []string{"config", "get", "lane.nope"}, env, cwd)
	if code != 2 || out != "" || errOut != "soho: config get: unknown key 'lane.nope'\n" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut = runIn(t, []string{"config", "get", "lane_build_nope"}, env, cwd)
	if code != 2 || out != "" || errOut != "soho: config get: unknown key 'lane_build_nope'\n" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut = runIn(t, []string{"config", "get"}, env, cwd)
	if code != 2 || out != "" || errOut != "soho: usage: config get <key>\n" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut = runIn(t, []string{"config", "get", "a", "b"}, env, cwd)
	if code != 2 || out != "" || errOut != "soho: usage: config get <key>\n" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
}

func TestConfigGetUnreadableUserFileDiesLikeTheJob(t *testing.T) {
	// A directory where the user file belongs cannot be read as a file on
	// any platform, root included (a chmod 0 file stays readable on
	// Windows and for root).
	env, cwd := commandFixture(t)
	userFile := platform.UserConfigPath(platform.Current(), env)
	if err := os.MkdirAll(userFile, 0o700); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runIn(t, []string{"config", "get", "job_wake_cmd"}, env, cwd)
	if code != 2 || out != "" || errOut != "soho: job: cannot read user configuration\n" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
}

func TestNowriteConfigGet(t *testing.T) {
	// The exact config get <key> runs read-only (the whole fixture tree,
	// project and state included, keeps the same paths, sizes and mtimes);
	// config get without a key is refused with the message that lists the
	// accepted reads as 'config [get <key>]'.
	nr := newNowriteReads(t, false)
	code, out, errOut := nr.runNowrite(t, "config", "get", "job_wake_cmd")
	if code != 0 || out != "\n" || errOut != "" {
		t.Fatalf("config get job_wake_cmd code=%d out=%q err=%q", code, out, errOut)
	}
	const messagePrefix = "soho: soho: SOHO_NOWRITE=1 is read-only: only these invocations run (the plugin's reads): the exact 'doctor', 'roster [--scope workspace|server] [--json]', 'explain', 'config [get <key>]', 'roles [--json]', 'friction' with the read options --since, --level, --command, --agent, --summary, 'collect <agent> [--lines N] [--verify]', 'copies', 'procs', 'status [agents...]', 'gc' without --yes, 'collaborate status <assignment> [--json]', 'capabilities --json'; rejected: "
	const messageTail = " — unset SOHO_NOWRITE to write\n"
	for _, c := range []struct {
		argv     []string
		rejected string
	}{
		{argv: []string{"config", "get"}, rejected: "config (1 extra argument not allowed)"},
		{argv: []string{"config", "get", "a", "b"}, rejected: "config (3 extra arguments not allowed)"},
	} {
		t.Run(strings.Join(c.argv, "_"), func(t *testing.T) {
			code, out, errOut := nr.runNowrite(t, c.argv...)
			if code != 2 || out != "" {
				t.Fatalf("%v code=%d out=%q err=%q", c.argv, code, out, errOut)
			}
			want := messagePrefix + c.rejected + messageTail
			if errOut != want {
				t.Fatalf("stderr:\n got:  %q\nwant: %q", errOut, want)
			}
		})
	}
}
