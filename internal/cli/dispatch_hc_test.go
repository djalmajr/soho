package cli

// DJA-604 2b-3a: dispatch through hc. The fake-hc technique is the one the
// spawn hc tests use (fakecli.Install on PATH plus SOHO_FAKECLI_CONFIG): the
// fake records every call in a JSONL log and answers with scripted JSON. A
// fake herdr is installed too and fails the test if it is ever called.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/kinds"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const (
	hcDispatchCaller    = `{"address":"t1","state":"live","labels":{"soho.session":"sX"}}`
	hcDispatchSessionLS = `{"terminals":[{"address":"t7","name":"worker","state":"live","turn":5,"reported":true,"status":{"state":"idle"}}]}`
)

// The guard probe fixtures are the read-only capability probes of one
// ordinary (non-queue) guarded send — `hc send --help` (the local parser) and
// `hc schema --json` (the serving core's OpenRPC document) — answered with
// the retained primary's (de97ea5, v0.1.20) OLD idle/working capability and
// the planned ready capability: the help's --if possible-values section and
// the terminal.send if param's IdleOrWorking enum, both regenerated from the
// primary's generated docs/commands.md and schema/hc.openrpc.json.
const (
	guardHelpOld         = "Paste text into a terminal and press Enter (waits for a person's unfinished draft)\n\nUsage: hc send [OPTIONS] <TERMINAL> <TEXT>\n\nArguments:\n  <TERMINAL>  The terminal: `t7`, a name, or `machine/t7`\n  <TEXT>      Pasted, then submitted with Enter\n\nOptions:\n      --force              Type even if the program is blocked (it may answer its dialog)\n      --json               JSON output even in a terminal\n      --if <STATUS>        Only type if the status is exactly this [possible values: idle, working]\n      --wait               Then wait for the turn this send started\n      --timeout <TIMEOUT>  With wait: seconds before it checks in (default: config, else 180)\n  -h, --help               Print help\n"
	guardHelpReady       = "Paste text into a terminal and press Enter (waits for a person's unfinished draft)\n\nUsage: hc send [OPTIONS] <TERMINAL> <TEXT>\n\nArguments:\n  <TERMINAL>  The terminal: `t7`, a name, or `machine/t7`\n  <TEXT>      Pasted, then submitted with Enter\n\nOptions:\n      --force              Type even if the program is blocked (it may answer its dialog)\n      --json               JSON output even in a terminal\n      --if <STATUS>        Only type if the status is exactly this [possible values: idle, working, ready]\n      --wait               Then wait for the turn this send started\n      --timeout <TIMEOUT>  With wait: seconds before it checks in (default: config, else 180)\n  -h, --help               Print help\n"
	guardHelpWorkingOnly = "Paste text into a terminal and press Enter (waits for a person's unfinished draft)\n\nUsage: hc send [OPTIONS] <TERMINAL> <TEXT>\n\nArguments:\n  <TERMINAL>  The terminal: `t7`, a name, or `machine/t7`\n  <TEXT>      Pasted, then submitted with Enter\n\nOptions:\n      --force              Type even if the program is blocked (it may answer its dialog)\n      --json               JSON output even in a terminal\n      --if <STATUS>        Only type if the status is exactly this [possible values: working]\n      --wait               Then wait for the turn this send started\n      --timeout <TIMEOUT>  With wait: seconds before it checks in (default: config, else 180)\n  -h, --help               Print help\n"
	guardSchemaOld       = "{\"openrpc\":\"1.3.2\",\"info\":{\"title\":\"herdr-core\",\"version\":\"0.1.20\"},\"components\":{\"schemas\":{\"IdleOrWorking\":{\"enum\":[\"idle\",\"working\"],\"type\":\"string\"}}},\"methods\":[{\"description\":\"Paste text and press Enter after a person sends or clears their draft (at most 60 s since their last key), otherwise after ~3 s of quiet. Keys pressed during delivery are held, then delivered. Refuses a blocked program unless `force`.\",\"errors\":[{\"code\":-32000,\"data\":{\"kind\":\"not_found\"},\"message\":\"not_found\"},{\"code\":-32000,\"data\":{\"kind\":\"machine_unknown\"},\"message\":\"machine_unknown\"},{\"code\":-32000,\"data\":{\"kind\":\"finished\"},\"message\":\"finished\"},{\"code\":-32000,\"data\":{\"kind\":\"unavailable\"},\"message\":\"unavailable\"},{\"code\":-32000,\"data\":{\"kind\":\"blocked\"},\"message\":\"blocked\"},{\"code\":-32000,\"data\":{\"kind\":\"status_mismatch\"},\"message\":\"status_mismatch\"},{\"code\":-32602,\"data\":{\"kind\":\"invalid_params\"},\"message\":\"invalid_params\"},{\"code\":-32000,\"data\":{\"kind\":\"config\"},\"message\":\"config\"},{\"code\":-32000,\"data\":{\"kind\":\"cancelled\"},\"message\":\"cancelled\"}],\"name\":\"terminal.send\",\"paramStructure\":\"by-name\",\"params\":[{\"description\":\"Who is sending (`HC_TERMINAL_ID`, else the calling process's parent), so a\\nbare wait from the same caller waits for this send's turn.\",\"name\":\"caller\",\"required\":false,\"schema\":{\"description\":\"Who is sending (`HC_TERMINAL_ID`, else the calling process's parent), so a\\nbare wait from the same caller waits for this send's turn.\",\"type\":[\"string\",\"null\"]}},{\"description\":\"Type even if the program is blocked (it may answer its dialog).\",\"name\":\"force\",\"required\":false,\"schema\":{\"default\":false,\"description\":\"Type even if the program is blocked (it may answer its dialog).\",\"type\":\"boolean\"}},{\"description\":\"Only type if the status is exactly this.\",\"name\":\"if\",\"required\":false,\"schema\":{\"anyOf\":[{\"$ref\":\"#/components/schemas/IdleOrWorking\"},{\"type\":\"null\"}],\"description\":\"Only type if the status is exactly this.\"}},{\"description\":\"The terminal: `t7`, a name, or `machine/t7`.\",\"name\":\"terminal\",\"required\":true,\"schema\":{\"description\":\"The terminal: `t7`, a name, or `machine/t7`.\",\"type\":\"string\"}},{\"description\":\"Pasted, then submitted with Enter.\",\"name\":\"text\",\"required\":true,\"schema\":{\"description\":\"Pasted, then submitted with Enter.\",\"type\":\"string\"}},{\"description\":\"With wait: seconds before it checks in (default: config, else 180).\",\"name\":\"timeout\",\"required\":false,\"schema\":{\"description\":\"With wait: seconds before it checks in (default: config, else 180).\",\"format\":\"double\",\"type\":[\"number\",\"null\"]}},{\"description\":\"Then wait for the turn this send started.\",\"name\":\"wait\",\"required\":false,\"schema\":{\"default\":false,\"description\":\"Then wait for the turn this send started.\",\"type\":\"boolean\"}}],\"result\":{\"name\":\"result\",\"schema\":{\"$ref\":\"#/components/schemas/SendResult\"}}}]}"
	guardSchemaReady     = "{\"openrpc\":\"1.3.2\",\"info\":{\"title\":\"herdr-core\",\"version\":\"0.1.20\"},\"components\":{\"schemas\":{\"IdleOrWorking\":{\"enum\":[\"idle\",\"working\",\"ready\"],\"type\":\"string\"}}},\"methods\":[{\"description\":\"Paste text and press Enter after a person sends or clears their draft (at most 60 s since their last key), otherwise after ~3 s of quiet. Keys pressed during delivery are held, then delivered. Refuses a blocked program unless `force`.\",\"errors\":[{\"code\":-32000,\"data\":{\"kind\":\"not_found\"},\"message\":\"not_found\"},{\"code\":-32000,\"data\":{\"kind\":\"machine_unknown\"},\"message\":\"machine_unknown\"},{\"code\":-32000,\"data\":{\"kind\":\"finished\"},\"message\":\"finished\"},{\"code\":-32000,\"data\":{\"kind\":\"unavailable\"},\"message\":\"unavailable\"},{\"code\":-32000,\"data\":{\"kind\":\"blocked\"},\"message\":\"blocked\"},{\"code\":-32000,\"data\":{\"kind\":\"status_mismatch\"},\"message\":\"status_mismatch\"},{\"code\":-32602,\"data\":{\"kind\":\"invalid_params\"},\"message\":\"invalid_params\"},{\"code\":-32000,\"data\":{\"kind\":\"config\"},\"message\":\"config\"},{\"code\":-32000,\"data\":{\"kind\":\"cancelled\"},\"message\":\"cancelled\"}],\"name\":\"terminal.send\",\"paramStructure\":\"by-name\",\"params\":[{\"description\":\"Who is sending (`HC_TERMINAL_ID`, else the calling process's parent), so a\\nbare wait from the same caller waits for this send's turn.\",\"name\":\"caller\",\"required\":false,\"schema\":{\"description\":\"Who is sending (`HC_TERMINAL_ID`, else the calling process's parent), so a\\nbare wait from the same caller waits for this send's turn.\",\"type\":[\"string\",\"null\"]}},{\"description\":\"Type even if the program is blocked (it may answer its dialog).\",\"name\":\"force\",\"required\":false,\"schema\":{\"default\":false,\"description\":\"Type even if the program is blocked (it may answer its dialog).\",\"type\":\"boolean\"}},{\"description\":\"Only type if the status is exactly this.\",\"name\":\"if\",\"required\":false,\"schema\":{\"anyOf\":[{\"$ref\":\"#/components/schemas/IdleOrWorking\"},{\"type\":\"null\"}],\"description\":\"Only type if the status is exactly this.\"}},{\"description\":\"The terminal: `t7`, a name, or `machine/t7`.\",\"name\":\"terminal\",\"required\":true,\"schema\":{\"description\":\"The terminal: `t7`, a name, or `machine/t7`.\",\"type\":\"string\"}},{\"description\":\"Pasted, then submitted with Enter.\",\"name\":\"text\",\"required\":true,\"schema\":{\"description\":\"Pasted, then submitted with Enter.\",\"type\":\"string\"}},{\"description\":\"With wait: seconds before it checks in (default: config, else 180).\",\"name\":\"timeout\",\"required\":false,\"schema\":{\"description\":\"With wait: seconds before it checks in (default: config, else 180).\",\"format\":\"double\",\"type\":[\"number\",\"null\"]}},{\"description\":\"Then wait for the turn this send started.\",\"name\":\"wait\",\"required\":false,\"schema\":{\"default\":false,\"description\":\"Then wait for the turn this send started.\",\"type\":\"boolean\"}}],\"result\":{\"name\":\"result\",\"schema\":{\"$ref\":\"#/components/schemas/SendResult\"}}}]}"
)

type dispatchHcFixture struct {
	root  string
	state string
	sd    string
	bin   string
	brief string
	env   platform.Env
	ctx   *core.Config
}

// hcWorkerGet is one state read of the fixture's worker: live, the given
// turn, and the given 7501 state word.
func hcWorkerGet(turn int, state string) string {
	return `{"address":"t7","name":"worker","state":"live","turn":` + strconv.Itoa(turn) + `,"reported":true,"status":{"state":"` + state + `"}}`
}

// newDispatchHcFixture sets up a dispatch for transport=hc in root: the
// caller terminal t1 of session sX, the fixture state dir (sd is
// <state>/sX, the session standing in for the workspace under hc), the
// worker roster row (its pane field is the hc address t7), the brief, the
// fake herdr, and the fake hc rules the test names. The composed brief and
// its report take the pinned stamp's bare names (nothing exists under them
// before the run).
func newDispatchHcFixture(t *testing.T, rules []fakecli.Rule) *dispatchHcFixture {
	t.Helper()
	root := t.TempDir()
	state, bin := filepath.Join(root, "state"), filepath.Join(root, "bin")
	roles, home, conf := filepath.Join(root, "roles"), filepath.Join(root, "home"), filepath.Join(root, "conf")
	sd := filepath.Join(state, "sX")
	for _, dir := range []string{state, filepath.Join(sd, "wait"), filepath.Join(sd, "briefs"), filepath.Join(sd, "reports"), roles, bin, home, conf} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	if err := os.WriteFile(filepath.Join(roles, "implementer.md"), []byte("---\nname: Implementer\nmode: edit\n---\nRole body.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	brief := filepath.Join(root, "brief.md")
	if err := os.WriteFile(brief, []byte("# Goal\n\nDispatch a prompt.\n# Expected result\n\nObserved.\n# Owned files\n\nnone\n# Forbidden\n\nNo commit or push.\n# Report\n\ndone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\nworker\tt7\tcodex\timplementer\topenai\t0\t\tnow\tgpt-5\ttask\timplementer\t\t\t--no-daemon\thigh\n"
	if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	pinComposedStamp(t)
	all := append([]fakecli.Rule{
		{Argv: []string{"get", "t1", "--json"}, ArgvPrefix: true, Stdout: hcDispatchCaller},
		{Argv: []string{"ls", "--label", "soho.session=sX", "--json"}, ArgvPrefix: true, Stdout: hcDispatchSessionLS},
	}, rules...)
	if _, err := fakecli.Install(t, bin, "hc", all); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, bin, "herdr", nil); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{
		"PATH": bin, "HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": conf,
		"TMPDIR": root, "SOHO_FAKECLI_CONFIG": bin,
		"SOHO_SKILL_DIR": testSkillDir(t), "SOHO_ROLES": roles,
		"SOHO_DIR": state, "SOHO_TRANSPORT": "hc", "HC_TERMINAL_ID": "t1",
		"SOHO_LANES": "off", "SOHO_BRIEF_LINT": "off",
		"SOHO_PROMPT_CHECK_SECONDS": "1", "SOHO_WAIT_POLL_MS": "50",
		"GORACE": fakecli.RaceOptions(testutil.CleanEnv(t)),
	}
	return &dispatchHcFixture{root: root, state: state, sd: sd, bin: bin, brief: brief, env: env, ctx: &core.Config{Entries: map[string]core.ConfigEntry{}}}
}

// newDispatchHcOpencodeFixture is the hc dispatch fixture with the roster
// row's kind opencode (the SOH-201 native primary): the caller t1 of
// session sX, the sd, the roles, the brief, the fakes and the env are the
// codex fixture's; only the roster row differs.
func newDispatchHcOpencodeFixture(t *testing.T, rules []fakecli.Rule) *dispatchHcFixture {
	t.Helper()
	root := t.TempDir()
	state, bin := filepath.Join(root, "state"), filepath.Join(root, "bin")
	roles, home, conf := filepath.Join(root, "roles"), filepath.Join(root, "home"), filepath.Join(root, "conf")
	sd := filepath.Join(state, "sX")
	for _, dir := range []string{state, filepath.Join(sd, "wait"), filepath.Join(sd, "briefs"), filepath.Join(sd, "reports"), roles, bin, home, conf} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	if err := os.WriteFile(filepath.Join(roles, "implementer.md"), []byte("---\nname: Implementer\nmode: edit\n---\nRole body.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	brief := filepath.Join(root, "brief.md")
	if err := os.WriteFile(brief, []byte("# Goal\n\nDispatch a prompt.\n# Expected result\n\nObserved.\n# Owned files\n\nnone\n# Forbidden\n\nNo commit or push.\n# Report\n\ndone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\nworker\tt7\topencode\timplementer\t\t0\t\tnow\tanthropic/claude-sonnet-4\ttask\timplementer\t\t\t\t\n"
	if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	pinComposedStamp(t)
	all := append([]fakecli.Rule{
		{Argv: []string{"get", "t1", "--json"}, ArgvPrefix: true, Stdout: hcDispatchCaller},
		{Argv: []string{"ls", "--label", "soho.session=sX", "--json"}, ArgvPrefix: true, Stdout: hcDispatchSessionLS},
	}, rules...)
	if _, err := fakecli.Install(t, bin, "hc", all); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, bin, "herdr", nil); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{
		"PATH": bin, "HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": conf,
		"TMPDIR": root, "SOHO_FAKECLI_CONFIG": bin,
		"SOHO_SKILL_DIR": testSkillDir(t), "SOHO_ROLES": roles,
		"SOHO_DIR": state, "SOHO_TRANSPORT": "hc", "HC_TERMINAL_ID": "t1",
		"SOHO_LANES": "off", "SOHO_BRIEF_LINT": "off",
		"SOHO_PROMPT_CHECK_SECONDS": "1", "SOHO_WAIT_POLL_MS": "50",
		"GORACE": fakecli.RaceOptions(testutil.CleanEnv(t)),
	}
	return &dispatchHcFixture{root: root, state: state, sd: sd, bin: bin, brief: brief, env: env, ctx: &core.Config{Entries: map[string]core.ConfigEntry{}}}
}

// hcOpencodeRecord is one native opencode terminal record the fixture
// scripts for the Get on the roster's own terminal id.
type hcOpencodeRecord struct {
	address     string
	nativeState string
	state       string
	reported    bool
	app         string
	profile     string
}

// hcOpencodeObserved is the exact observation the native primary terminal
// reports: live, the idle 7501 word, reported, app opencode, the primary
// profile under the label.
var hcOpencodeObserved = hcOpencodeRecord{
	address: "t7", nativeState: "live", state: "idle", reported: true,
	app: "opencode", profile: kinds.OpenCodePrimaryProfile,
}

// hcOpencodeGet renders the record as the hc core answers `get t7 --json`:
// the address, the native lifecycle state, the turn, the reported flag,
// the 7501 state word with the status app, and the launch profile label
// (an empty profile omits the label entirely).
func hcOpencodeGet(rec hcOpencodeRecord) string {
	out := `{"address":"` + rec.address + `","name":"worker","state":"` + rec.nativeState + `","turn":7,"reported":` + strconv.FormatBool(rec.reported) + `,"status":{"state":"` + rec.state + `","app":"` + rec.app + `"}`
	if rec.profile != "" {
		out += `,"labels":{"soho.launch_profile":"` + rec.profile + `"}`
	}
	return out + `}`
}

func (f *dispatchHcFixture) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	code := func() int {
		defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
		return Run(append([]string{"dispatch"}, args...), f.env)
	}()
	return code, out.String(), stderr.String()
}

func (f *dispatchHcFixture) hcCalls(t *testing.T) []fakecli.Call {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "hc.calls.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	return calls
}

// assertHcNeverTouchedHerdr fails the test when the fake herdr recorded any
// call: under transport=hc the dispatch must reach the worker only through
// hc.
func (f *dispatchHcFixture) assertHcNeverTouchedHerdr(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(f.bin, "herdr.calls.jsonl")); err == nil {
		calls, _ := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		t.Fatalf("herdr was called: %#v", calls)
	}
}

// guardProbeRules are the read-only capability probes of one ordinary
// (non-queue) guarded send, answered with the primary's OLD idle/working
// capability: both sides prove idle, so the guard selects it.
func guardProbeRules() []fakecli.Rule {
	return []fakecli.Rule{
		{Argv: []string{"send", "--help"}, Stdout: guardHelpOld},
		{Argv: []string{"schema", "--json"}, Stdout: guardSchemaOld},
	}
}

// guardProbeRulesReady answer the same probes with the ready capability:
// both sides prove ready, so the guard selects it.
func guardProbeRulesReady() []fakecli.Rule {
	return []fakecli.Rule{
		{Argv: []string{"send", "--help"}, Stdout: guardHelpReady},
		{Argv: []string{"schema", "--json"}, Stdout: guardSchemaReady},
	}
}

// isRealSend is one terminal input send: a send that is not the read-only
// send --help capability probe.
func isRealSend(argv []string) bool {
	return len(argv) > 0 && argv[0] == "send" && !(len(argv) == 2 && argv[1] == "--help")
}

// hcSends returns the recorded real send calls (the read-only send --help
// capability probe is not terminal input and is excluded), in order.
func hcSends(calls []fakecli.Call) []fakecli.Call {
	var out []fakecli.Call
	for _, c := range calls {
		if isRealSend(c.Argv) {
			out = append(out, c)
		}
	}
	return out
}

// guardProbeCalls counts the read-only capability probes the guarded send
// issued: the send --help parse probe and the schema --json serving probe.
func guardProbeCalls(calls []fakecli.Call) (help, schema int) {
	for _, c := range calls {
		switch {
		case len(c.Argv) == 2 && c.Argv[0] == "send" && c.Argv[1] == "--help":
			help++
		case len(c.Argv) == 2 && c.Argv[0] == "schema" && c.Argv[1] == "--json":
			schema++
		}
	}
	return help, schema
}

func dispatchHcOutputStatus(t *testing.T, out string) string {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &value); err != nil {
		t.Fatalf("dispatch JSON %q: %v", out, err)
	}
	status, _ := value["wait_status"].(string)
	return status
}

// TestDispatchHcIdleWorkerAccepted is acceptance (a): an idle worker, the
// exact send argv, started true, the accepted sidecar, no screen read and no
// key around the send, and no herdr call.
func TestDispatchHcIdleWorkerAccepted(t *testing.T) {
	f := newDispatchHcFixture(t, append(guardProbeRules(), []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, Call: 1, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Stdout: `{"turn":7,"started":true}`},
		{Argv: []string{"read", "worker"}, ArgvPrefix: true},
	}...))
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if status := dispatchHcOutputStatus(t, out); status != "submitted" {
		t.Fatalf("status=%q, want submitted (out=%s)", status, out)
	}
	sends := hcSends(f.hcCalls(t))
	if len(sends) != 1 {
		t.Fatalf("sends=%d, want exactly one: %#v", len(sends), sends)
	}
	if help, schema := guardProbeCalls(f.hcCalls(t)); help != 1 || schema != 1 {
		t.Fatalf("capability probes help=%d schema=%d, want the one read-only pair", help, schema)
	}
	argv := sends[0].Argv
	want := []string{"send", "--if", "idle", "--json", "--", "worker"}
	if len(argv) != len(want)+1 {
		t.Fatalf("argv=%#v, want %v plus the text", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("argv[%d]=%q, want %q (argv=%#v)", i, argv[i], want[i], argv)
		}
	}
	// The send text is the pointer prompt: the composed brief's path and the
	// report's path, the same text the herdr path types into the terminal.
	stamp := composedStampOf(f, t)
	composedPath := filepath.Join(f.sd, "briefs", "worker-"+stamp+".md")
	reportPath := filepath.Join(f.sd, "reports", "worker-"+stamp+".md")
	wantText := fmt.Sprintf("Read the file %s in full and execute it. It contains your role, your brief, and your report contract. When finished, write your report to %s and reply with exactly that path and nothing else.", composedPath, reportPath)
	if argv[6] != wantText {
		t.Fatalf("send text = %q, want %q", argv[6], wantText)
	}
	assertNoReadsOrKeysAroundTheSend(t, f.hcCalls(t))
	f.assertHcNeverTouchedHerdr(t)
	sidecar, err := os.ReadFile(filepath.Join(f.sd, "briefs", "worker-"+composedStampOf(f, t)+".dispatch.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sidecar), `"submission":"accepted"`) {
		t.Fatalf("sidecar = %s, want submission accepted", sidecar)
	}
}

// assertNoReadsOrKeysAroundTheSend pins the skipped step set: under hc the
// dispatch sends no key at all and reads no screen before the send (the
// settle, H0, prior-auth and composer reads the herdr path sends); the
// --no-wait observation phase may read the screen after the send, as the
// herdr path's no-wait phase does.
func assertNoReadsOrKeysAroundTheSend(t *testing.T, calls []fakecli.Call) {
	t.Helper()
	sendIndex := -1
	for i, c := range calls {
		if isRealSend(c.Argv) {
			sendIndex = i
			break
		}
	}
	if sendIndex < 0 {
		t.Fatal("no send recorded, want the send under test")
	}
	for i, c := range calls {
		if len(c.Argv) == 0 {
			continue
		}
		if c.Argv[0] == "keys" {
			t.Fatalf("a key was sent under hc: %#v", c)
		}
		if c.Argv[0] == "read" && i < sendIndex {
			t.Fatalf("a screen read before the send under hc: %#v", c)
		}
	}
}

// TestDispatchHcTurnRiseArrives is acceptance (b): started false, then a get
// with a greater turn is arrival proof inside the check window.
func TestDispatchHcTurnRiseArrives(t *testing.T) {
	f := newDispatchHcFixture(t, append(guardProbeRules(), []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, Call: 1, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(8, "idle")},
		{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Stdout: `{"turn":7,"started":false}`},
		{Argv: []string{"read", "worker"}, ArgvPrefix: true},
	}...))
	// The window is generous so the acceptance does not depend on the fake
	// process's start time under host load; the test ends as soon as the
	// turn rise is observed.
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "30"
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if status := dispatchHcOutputStatus(t, out); status != "submitted" {
		t.Fatalf("status=%q, want submitted (out=%s)", status, out)
	}
	if len(hcSends(f.hcCalls(t))) != 1 {
		t.Fatalf("sends: %#v, want exactly one", hcSends(f.hcCalls(t)))
	}
	if help, schema := guardProbeCalls(f.hcCalls(t)); help != 1 || schema != 1 {
		t.Fatalf("capability probes help=%d schema=%d, want the one read-only pair", help, schema)
	}
	// The arrival probe is the second get (the first is the pre-send idle
	// check); it saw turn 8 > the send's turn 7.
	gets := 0
	for _, c := range f.hcCalls(t) {
		if len(c.Argv) == 3 && c.Argv[0] == "get" && c.Argv[1] == "worker" {
			gets++
		}
	}
	if gets < 2 {
		t.Fatalf("worker gets=%d, want the pre-send check and the arrival probe", gets)
	}
	f.assertHcNeverTouchedHerdr(t)
}

// TestDispatchHcNoTurnRiseIsNotReceived is acceptance (c): started false and
// the turn never moves, so the not-received path with the reason, and exactly
// one send (no resend under hc).
func TestDispatchHcNoTurnRiseIsNotReceived(t *testing.T) {
	f := newDispatchHcFixture(t, append(guardProbeRules(), []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Stdout: `{"turn":7,"started":false}`},
	}...))
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 15 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if status := dispatchHcOutputStatus(t, out); status != "not-received" {
		t.Fatalf("status=%q, want not-received (out=%s)", status, out)
	}
	// The hc not-received line is pinned exactly: under hc it names `hc
	// read`, never the herdr pane read (the herdr wording stays as the
	// herdr tests pin it).
	wantLine := "soho: warning: prompt to 'worker' was not received after no turn started under hc; read the terminal (hc read worker) before sending anything else"
	if !strings.Contains(errText, wantLine) {
		t.Fatalf("stderr=%q, want the exact hc not-received line %q", errText, wantLine)
	}
	if len(hcSends(f.hcCalls(t))) != 1 {
		t.Fatalf("sends: %#v, want exactly one (no resend under hc)", hcSends(f.hcCalls(t)))
	}
	if help, schema := guardProbeCalls(f.hcCalls(t)); help != 1 || schema != 1 {
		t.Fatalf("capability probes help=%d schema=%d, want the one read-only pair", help, schema)
	}
	f.assertHcNeverTouchedHerdr(t)
}

// TestDispatchHcBlockedRestoresThePointer is acceptance (d): a send that
// exits 9 takes the blocked result with cause hc-blocked, and the task
// pointer is restored like the codex-hooks-review block.
func TestDispatchHcBlockedRestoresThePointer(t *testing.T) {
	f := newDispatchHcFixture(t, append(guardProbeRules(), []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Code: 9, Stderr: `{"error":{"kind":"blocked","message":"worker is blocked"}}`},
	}...))
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 7 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &value); err != nil {
		t.Fatalf("dispatch JSON %q: %v", out, err)
	}
	if value["wait_status"] != "blocked" {
		t.Fatalf("wait_status=%v, want blocked (out=%s)", value["wait_status"], out)
	}
	cause, _ := value["cause"].(string)
	if cause != "hc-blocked" {
		t.Fatalf("cause=%q, want hc-blocked (out=%s)", cause, out)
	}
	// The task pointer and the last-report pointer are restored: the
	// fixture had no prior dispatch, so both are gone.
	if _, err := os.Stat(taskreport.TaskReportPointerPath(f.sd, "worker")); err == nil {
		t.Fatal("task pointer was not restored (still present)")
	}
	if _, err := os.ReadFile(filepath.Join(f.sd, "last-report-worker")); err == nil {
		t.Fatal("last-report pointer was not restored (still present)")
	}
	if help, schema := guardProbeCalls(f.hcCalls(t)); help != 1 || schema != 1 {
		t.Fatalf("capability probes help=%d schema=%d, want the one read-only pair", help, schema)
	}
	f.assertHcNeverTouchedHerdr(t)
}

// TestDispatchHcBusyRetriesOnceWhenIdle is acceptance (e): a send that exits
// 10 (the worker went working), then a get that settles to idle, then a
// second send that succeeds: two sends, accepted.
func TestDispatchHcBusyRetriesOnceWhenIdle(t *testing.T) {
	f := newDispatchHcFixture(t, append(guardProbeRules(), []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, Call: 1, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"get", "worker", "--json"}, Call: 2, Stdout: hcWorkerGet(7, "working")},
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(8, "idle")},
		{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Call: 1, Code: 10, Stderr: `{"error":{"kind":"status_mismatch","message":"the terminal is working"}}`},
		{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Call: 2, Stdout: `{"turn":8,"started":true}`},
		{Argv: []string{"read", "worker"}, ArgvPrefix: true},
	}...))
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if status := dispatchHcOutputStatus(t, out); status != "submitted" {
		t.Fatalf("status=%q, want submitted (out=%s)", status, out)
	}
	sends := hcSends(f.hcCalls(t))
	if len(sends) != 2 {
		t.Fatalf("sends=%d, want exactly two (the busy retry): %#v", len(sends), sends)
	}
	// The retry re-runs the guarded send, so the read-only capability probes
	// run twice: once per guarded send attempt.
	if help, schema := guardProbeCalls(f.hcCalls(t)); help != 2 || schema != 2 {
		t.Fatalf("capability probes help=%d schema=%d, want the two read-only pairs", help, schema)
	}
	sidecar, err := os.ReadFile(filepath.Join(f.sd, "briefs", "worker-"+composedStampOf(f, t)+".dispatch.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sidecar), `"submission":"accepted"`) {
		t.Fatalf("sidecar = %s, want submission accepted", sidecar)
	}
	f.assertHcNeverTouchedHerdr(t)
}

// TestDispatchHcQueueDropsTheIdleGuard is acceptance (f): --queue sends
// without --if, so the text goes in even while the worker works.
func TestDispatchHcQueueDropsTheIdleGuard(t *testing.T) {
	f := newDispatchHcFixture(t, []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "working")},
		{Argv: []string{"send", "--json", "--", "worker"}, ArgvPrefix: true, Stdout: `{"turn":8,"started":true}`},
	})
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait", "--queue")
	if code != 0 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if status := dispatchHcOutputStatus(t, out); status != "submitted" {
		t.Fatalf("status=%q, want submitted (out=%s)", status, out)
	}
	sends := hcSends(f.hcCalls(t))
	if len(sends) != 1 {
		t.Fatalf("sends=%d, want exactly one: %#v", len(sends), sends)
	}
	argv := sends[0].Argv
	if len(argv) < 2 || argv[1] != "--json" {
		t.Fatalf("argv=%#v, want the --queue send to carry no --if guard", argv)
	}
	for _, a := range argv {
		if a == "--if" || a == "idle" {
			t.Fatalf("argv=%#v, --queue must drop the --if idle guard", argv)
		}
	}
	f.assertHcNeverTouchedHerdr(t)
}

// TestDispatchHcCompactRefusedBeforeSend is acceptance (g): --compact is
// refused with exit 2 and the exact line, before anything is sent.
func TestDispatchHcCompactRefusedBeforeSend(t *testing.T) {
	f := newDispatchHcFixture(t, nil)
	code, out, errText := f.run(t, "worker", f.brief, "--compact", "--no-wait")
	if code != 2 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if want := "soho: dispatch: --compact is not supported under transport=hc yet\n"; errText != want {
		t.Fatalf("stderr=%q, want exactly %q", errText, want)
	}
	if out != "" {
		t.Fatalf("stdout=%q, want nothing", out)
	}
	if sends := hcSends(f.hcCalls(t)); len(sends) != 0 {
		t.Fatalf("sends before the refusal: %#v", sends)
	}
	f.assertHcNeverTouchedHerdr(t)
}

// TestDispatchHcReadyWorkerAccepted is acceptance (h): the local parser and
// the serving enum both prove ready, so the guarded send goes out as
// --if ready — the ready word is proven per call, never assumed.
func TestDispatchHcReadyWorkerAccepted(t *testing.T) {
	f := newDispatchHcFixture(t, append(guardProbeRulesReady(), []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, Call: 1, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"send", "--if", "ready", "--json", "--", "worker"}, ArgvPrefix: true, Stdout: `{"turn":7,"started":true}`},
		{Argv: []string{"read", "worker"}, ArgvPrefix: true},
	}...))
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if status := dispatchHcOutputStatus(t, out); status != "submitted" {
		t.Fatalf("status=%q, want submitted (out=%s)", status, out)
	}
	sends := hcSends(f.hcCalls(t))
	if len(sends) != 1 {
		t.Fatalf("sends=%d, want exactly one: %#v", len(sends), sends)
	}
	argv := sends[0].Argv
	if len(argv) < 3 || argv[1] != "--if" || argv[2] != "ready" {
		t.Fatalf("argv=%#v, want the guarded send to carry --if ready", argv)
	}
	if help, schema := guardProbeCalls(f.hcCalls(t)); help != 1 || schema != 1 {
		t.Fatalf("capability probes help=%d schema=%d, want the one read-only pair", help, schema)
	}
	assertNoReadsOrKeysAroundTheSend(t, f.hcCalls(t))
	f.assertHcNeverTouchedHerdr(t)
	sidecar, err := os.ReadFile(filepath.Join(f.sd, "briefs", "worker-"+composedStampOf(f, t)+".dispatch.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sidecar), `"submission":"accepted"`) {
		t.Fatalf("sidecar = %s, want submission accepted", sidecar)
	}
}

// TestDispatchHcUnknownCapabilityZeroSend is acceptance (i): the parser
// proves only working and the serving enum proves idle and working — no
// guard is proven on both sides — so the dispatch refuses with the
// submission failure, the capability refusal in the raw result, and exactly
// the two read-only probes: no send at all, never an idle fallback.
func TestDispatchHcUnknownCapabilityZeroSend(t *testing.T) {
	f := newDispatchHcFixture(t, []fakecli.Rule{
		{Argv: []string{"send", "--help"}, Stdout: guardHelpWorkingOnly},
		{Argv: []string{"schema", "--json"}, Stdout: guardSchemaOld},
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "idle")},
	})
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 4 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if status := dispatchHcOutputStatus(t, out); status != "error" {
		t.Fatalf("status=%q, want error (out=%s)", status, out)
	}
	wantRaw := "cannot prove the --if guard for worker worker: no --if guard is proven on both sides"
	if !strings.Contains(out, wantRaw) {
		t.Fatalf("out=%s, want the capability refusal %q", out, wantRaw)
	}
	wantWarn := "soho: warning: prompt submission failed; inspect with: hc get worker && hc read worker. Do not resend blindly."
	if !strings.Contains(errText, wantWarn) {
		t.Fatalf("stderr=%q, want the inspection warning %q", errText, wantWarn)
	}
	if sends := hcSends(f.hcCalls(t)); len(sends) != 0 {
		t.Fatalf("sends=%d, want exactly zero (no idle fallback): %#v", len(sends), sends)
	}
	if help, schema := guardProbeCalls(f.hcCalls(t)); help != 1 || schema != 1 {
		t.Fatalf("capability probes help=%d schema=%d, want the one read-only pair", help, schema)
	}
	f.assertHcNeverTouchedHerdr(t)
}

// composedStampOf recovers the composed brief's stamp from the fixture's
// briefs dir (one dispatch, one composed brief).
func composedStampOf(f *dispatchHcFixture, t *testing.T) string {
	t.Helper()
	glob := filepath.Join(f.sd, "briefs", "worker-*.md")
	files, err := filepath.Glob(glob)
	if err != nil || len(files) != 1 {
		t.Fatalf("composed briefs = %#v (err %v), want exactly one", files, err)
	}
	name := filepath.Base(files[0])
	if !strings.HasPrefix(name, "worker-") || !strings.HasSuffix(name, ".md") {
		t.Fatalf("composed brief %q, want worker-<stamp>.md", name)
	}
	return strings.TrimSuffix(strings.TrimPrefix(name, "worker-"), ".md")
}

// TestDispatchHcOpenCodePrimary pins the SOH-201 hc+opencode gate: the
// dispatch reads the roster's own terminal id through Get and admits the
// send only while it reports the exact live primary profile; every other
// observation refuses with the fixed line and zero input, report-pointer,
// sidecar, screen or provider side effect. The admitted worker keeps the
// existing queue, when-idle, working and blocked semantics: Observed is
// not Ready, so no readiness check or guard downgrade is added.
func TestDispatchHcOpenCodePrimary(t *testing.T) {
	const refuseLine = "soho: dispatch: opencode requires a live reported primary launch profile under transport=hc\n"
	observations := []struct {
		name string
		rec  hcOpencodeRecord
	}{
		{"missing-profile", hcOpencodeRecord{address: "t7", nativeState: "live", state: "idle", reported: true, app: "opencode"}},
		{"wrong-profile", hcOpencodeRecord{address: "t7", nativeState: "live", state: "idle", reported: true, app: "opencode", profile: "some-other-profile"}},
		{"not-reported", hcOpencodeRecord{address: "t7", nativeState: "live", state: "idle", reported: false, app: "opencode", profile: kinds.OpenCodePrimaryProfile}},
		{"not-live", hcOpencodeRecord{address: "t7", nativeState: "finished", state: "idle", reported: true, app: "opencode", profile: kinds.OpenCodePrimaryProfile}},
		{"wrong-app", hcOpencodeRecord{address: "t7", nativeState: "live", state: "idle", reported: true, app: "codex", profile: kinds.OpenCodePrimaryProfile}},
		{"wrong-target", hcOpencodeRecord{address: "t9", nativeState: "live", state: "idle", reported: true, app: "opencode", profile: kinds.OpenCodePrimaryProfile}},
	}
	for _, tc := range observations {
		t.Run(tc.name, func(t *testing.T) {
			f := newDispatchHcOpencodeFixture(t, []fakecli.Rule{
				{Argv: []string{"get", "t7", "--json"}, ArgvPrefix: true, Stdout: hcOpencodeGet(tc.rec)},
			})
			code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
			if code != 2 {
				t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
			}
			if errText != refuseLine {
				t.Fatalf("stderr=%q, want exactly %q", errText, refuseLine)
			}
			if out != "" {
				t.Fatalf("stdout=%q, want nothing", out)
			}
			calls := f.hcCalls(t)
			if len(hcSends(calls)) != 0 {
				t.Fatalf("terminal input before the refusal: %#v", hcSends(calls))
			}
			gateGets := 0
			for _, c := range calls {
				switch {
				case len(c.Argv) > 0 && c.Argv[0] == "get" && c.Argv[1] == "t7":
					gateGets++
				case len(c.Argv) > 0 && c.Argv[0] == "get" && c.Argv[1] == "worker":
					t.Fatalf("a pre-send state get before the refusal: %#v", c)
				case len(c.Argv) > 0 && (c.Argv[0] == "read" || c.Argv[0] == "keys"):
					t.Fatalf("a screen read or key before the refusal: %#v", c)
				}
			}
			if gateGets != 1 {
				t.Fatalf("gate gets=%d, want the one Get on the roster's own terminal id: %#v", gateGets, calls)
			}
			if _, err := os.Stat(taskreport.TaskReportPointerPath(f.sd, "worker")); err == nil {
				t.Fatal("the task report pointer was written before the refusal")
			}
			if _, err := os.Stat(filepath.Join(f.sd, "last-report-worker")); err == nil {
				t.Fatal("the report pointer was written before the refusal")
			}
			if files, _ := filepath.Glob(filepath.Join(f.sd, "briefs", "worker-*")); len(files) != 0 {
				t.Fatalf("a composed brief or sidecar before the refusal: %#v", files)
			}
			f.assertHcNeverTouchedHerdr(t)
		})
	}
	t.Run("idle-accepted", func(t *testing.T) {
		f := newDispatchHcOpencodeFixture(t, append(guardProbeRules(), []fakecli.Rule{
			{Argv: []string{"get", "t7", "--json"}, ArgvPrefix: true, Stdout: hcOpencodeGet(hcOpencodeObserved)},
			{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcOpencodeGet(hcOpencodeObserved)},
			{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Stdout: `{"turn":7,"started":true}`},
			{Argv: []string{"read", "worker"}, ArgvPrefix: true},
		}...))
		code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		if status := dispatchHcOutputStatus(t, out); status != "submitted" {
			t.Fatalf("status=%q, want submitted (out=%s)", status, out)
		}
		sends := hcSends(f.hcCalls(t))
		if len(sends) != 1 {
			t.Fatalf("sends=%d, want exactly one: %#v", len(sends), sends)
		}
		argv := sends[0].Argv
		want := []string{"send", "--if", "idle", "--json", "--", "worker"}
		if len(argv) != len(want)+1 {
			t.Fatalf("argv=%#v, want %v plus the text", argv, want)
		}
		for i := range want {
			if argv[i] != want[i] {
				t.Fatalf("argv[%d]=%q, want %q (argv=%#v)", i, argv[i], want[i], argv)
			}
		}
		if help, schema := guardProbeCalls(f.hcCalls(t)); help != 1 || schema != 1 {
			t.Fatalf("capability probes help=%d schema=%d, want the one read-only pair", help, schema)
		}
		gateGets := 0
		for _, c := range f.hcCalls(t) {
			if len(c.Argv) > 0 && c.Argv[0] == "get" && c.Argv[1] == "t7" {
				gateGets++
			}
		}
		if gateGets != 1 {
			t.Fatalf("gate gets=%d, want the one Get on the roster's own terminal id", gateGets)
		}
		sidecar, err := os.ReadFile(filepath.Join(f.sd, "briefs", "worker-"+composedStampOf(f, t)+".dispatch.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(sidecar), `"submission":"accepted"`) {
			t.Fatalf("sidecar = %s, want submission accepted", sidecar)
		}
		f.assertHcNeverTouchedHerdr(t)
	})
	t.Run("working-busy-retry", func(t *testing.T) {
		working := hcOpencodeRecord{address: "t7", nativeState: "live", state: "working", reported: true, app: "opencode", profile: kinds.OpenCodePrimaryProfile}
		idle := hcOpencodeRecord{address: "t7", nativeState: "live", state: "idle", reported: true, app: "opencode", profile: kinds.OpenCodePrimaryProfile}
		f := newDispatchHcOpencodeFixture(t, append(guardProbeRules(), []fakecli.Rule{
			{Argv: []string{"get", "t7", "--json"}, ArgvPrefix: true, Stdout: hcOpencodeGet(working)},
			{Argv: []string{"get", "worker", "--json"}, Call: 1, Stdout: hcOpencodeGet(working)},
			{Argv: []string{"get", "worker", "--json"}, Call: 2, Stdout: hcOpencodeGet(working)},
			{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcOpencodeGet(idle)},
			{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Call: 1, Code: 10, Stderr: `{"error":{"kind":"status_mismatch","message":"the terminal is working"}}`},
			{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Call: 2, Stdout: `{"turn":8,"started":true}`},
			{Argv: []string{"read", "worker"}, ArgvPrefix: true},
		}...))
		code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		if status := dispatchHcOutputStatus(t, out); status != "submitted" {
			t.Fatalf("status=%q, want submitted (out=%s)", status, out)
		}
		if len(hcSends(f.hcCalls(t))) != 2 {
			t.Fatalf("sends: %#v, want exactly two (the busy retry)", hcSends(f.hcCalls(t)))
		}
		if help, schema := guardProbeCalls(f.hcCalls(t)); help != 2 || schema != 2 {
			t.Fatalf("capability probes help=%d schema=%d, want the two read-only pairs", help, schema)
		}
		sidecar, err := os.ReadFile(filepath.Join(f.sd, "briefs", "worker-"+composedStampOf(f, t)+".dispatch.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(sidecar), `"submission":"accepted"`) {
			t.Fatalf("sidecar = %s, want submission accepted", sidecar)
		}
		f.assertHcNeverTouchedHerdr(t)
	})
	t.Run("queue-drops-the-idle-guard", func(t *testing.T) {
		working := hcOpencodeRecord{address: "t7", nativeState: "live", state: "working", reported: true, app: "opencode", profile: kinds.OpenCodePrimaryProfile}
		f := newDispatchHcOpencodeFixture(t, []fakecli.Rule{
			{Argv: []string{"get", "t7", "--json"}, ArgvPrefix: true, Stdout: hcOpencodeGet(working)},
			{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcOpencodeGet(working)},
			{Argv: []string{"send", "--json", "--", "worker"}, ArgvPrefix: true, Stdout: `{"turn":8,"started":true}`},
		})
		code, out, errText := f.run(t, "worker", f.brief, "--no-wait", "--queue")
		if code != 0 {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		if status := dispatchHcOutputStatus(t, out); status != "submitted" {
			t.Fatalf("status=%q, want submitted (out=%s)", status, out)
		}
		sends := hcSends(f.hcCalls(t))
		if len(sends) != 1 {
			t.Fatalf("sends=%d, want exactly one: %#v", len(sends), sends)
		}
		for _, a := range sends[0].Argv {
			if a == "--if" || a == "idle" {
				t.Fatalf("argv=%#v, --queue must drop the --if idle guard", sends[0].Argv)
			}
		}
		f.assertHcNeverTouchedHerdr(t)
	})
	t.Run("when-idle", func(t *testing.T) {
		working := hcOpencodeRecord{address: "t7", nativeState: "live", state: "working", reported: true, app: "opencode", profile: kinds.OpenCodePrimaryProfile}
		idle := hcOpencodeRecord{address: "t7", nativeState: "live", state: "idle", reported: true, app: "opencode", profile: kinds.OpenCodePrimaryProfile}
		f := newDispatchHcOpencodeFixture(t, append(guardProbeRules(), []fakecli.Rule{
			{Argv: []string{"get", "t7", "--json"}, ArgvPrefix: true, Stdout: hcOpencodeGet(working)},
			{Argv: []string{"get", "worker", "--json"}, Call: 1, Stdout: hcOpencodeGet(working)},
			{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcOpencodeGet(idle)},
			{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Stdout: `{"turn":7,"started":true}`},
			{Argv: []string{"read", "worker"}, ArgvPrefix: true},
		}...))
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "30"
		code, out, errText := f.run(t, "worker", f.brief, "--no-wait", "--when-idle")
		if code != 0 {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		if status := dispatchHcOutputStatus(t, out); status != "submitted" {
			t.Fatalf("status=%q, want submitted (out=%s)", status, out)
		}
		sends := hcSends(f.hcCalls(t))
		if len(sends) != 1 {
			t.Fatalf("sends=%d, want exactly one: %#v", len(sends), sends)
		}
		argv := sends[0].Argv
		if len(argv) < 3 || argv[1] != "--if" || argv[2] != "idle" {
			t.Fatalf("argv=%#v, want the guarded send to carry --if idle", argv)
		}
		f.assertHcNeverTouchedHerdr(t)
	})
	t.Run("blocked", func(t *testing.T) {
		f := newDispatchHcOpencodeFixture(t, append(guardProbeRules(), []fakecli.Rule{
			{Argv: []string{"get", "t7", "--json"}, ArgvPrefix: true, Stdout: hcOpencodeGet(hcOpencodeObserved)},
			{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcOpencodeGet(hcOpencodeObserved)},
			{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Code: 9, Stderr: `{"error":{"kind":"blocked","message":"worker is blocked"}}`},
		}...))
		code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
		if code != 7 {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		var value map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &value); err != nil {
			t.Fatalf("dispatch JSON %q: %v", out, err)
		}
		if value["wait_status"] != "blocked" {
			t.Fatalf("wait_status=%v, want blocked (out=%s)", value["wait_status"], out)
		}
		cause, _ := value["cause"].(string)
		if cause != "hc-blocked" {
			t.Fatalf("cause=%q, want hc-blocked (out=%s)", cause, out)
		}
		if _, err := os.Stat(taskreport.TaskReportPointerPath(f.sd, "worker")); err == nil {
			t.Fatal("task pointer was not restored (still present)")
		}
		if _, err := os.ReadFile(filepath.Join(f.sd, "last-report-worker")); err == nil {
			t.Fatal("last-report pointer was not restored (still present)")
		}
		if len(hcSends(f.hcCalls(t))) != 1 {
			t.Fatalf("sends: %#v, want exactly one", hcSends(f.hcCalls(t)))
		}
		if help, schema := guardProbeCalls(f.hcCalls(t)); help != 1 || schema != 1 {
			t.Fatalf("capability probes help=%d schema=%d, want the one read-only pair", help, schema)
		}
		f.assertHcNeverTouchedHerdr(t)
	})
}

// TestHCNativeModelProfile pins the model-only command against the shared
// launch resolver: the native hc+opencode combination reports the public
// launch_profile and the standalone arg with no model flag and no
// environment value; every other combination keeps its exact legacy JSON;
// a malformed native reference takes the fixed value-free refusal: exit 2
// with the soho prefix, no raw value echo and no panic.
func TestHCNativeModelProfile(t *testing.T) {
	const ref = "anthropic/claude-sonnet-4"
	legacyFields := []string{"agent_args", "effort", "effort_ceiling", "family", "kind", "model", "spec"}
	modelRun := func(t *testing.T, env platform.Env, args ...string) (int, string, string) {
		t.Helper()
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var out, stderr bytes.Buffer
		platform.Stdout, platform.Stderr = &out, &stderr
		code := func() int {
			defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
			return Run(append([]string{"model"}, args...), env)
		}()
		return code, out.String(), stderr.String()
	}
	parseModel := func(t *testing.T, out string) map[string]any {
		t.Helper()
		var value map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &value); err != nil {
			t.Fatalf("model JSON %q: %v", out, err)
		}
		return value
	}
	sortedKeys := func(value map[string]any) []string {
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return keys
	}
	assertFields := func(t *testing.T, value map[string]any, want []string) {
		t.Helper()
		got := sortedKeys(value)
		if len(got) != len(want) {
			t.Fatalf("fields = %v, want exactly %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("fields = %v, want exactly %v", got, want)
			}
		}
	}
	nativeFields := append([]string{"launch_profile"}, legacyFields...)
	sort.Strings(nativeFields)
	t.Run("native-standalone", func(t *testing.T) {
		f := newDispatchHcFixture(t, nil)
		code, out, errText := modelRun(t, f.env, "opencode", ref)
		if code != 0 {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		value := parseModel(t, out)
		assertFields(t, value, nativeFields)
		if value["kind"] != "opencode" || value["spec"] != ref {
			t.Fatalf("echo = %v, want kind and spec echoed", value)
		}
		if value["launch_profile"] != kinds.OpenCodePrimaryProfile {
			t.Fatalf("launch_profile=%v, want %q", value["launch_profile"], kinds.OpenCodePrimaryProfile)
		}
		agentArgs, _ := value["agent_args"].(string)
		if agentArgs != "--standalone" {
			t.Fatalf("agent_args=%q, want the standalone arg alone (no model flag, no env value)", agentArgs)
		}
		if strings.Contains(out, "OPENCODE_") {
			t.Fatalf("stdout leaks an environment override: %s", out)
		}
	})
	t.Run("legacy-opencode-herdr", func(t *testing.T) {
		f := newDispatchHcFixture(t, nil)
		f.env["SOHO_TRANSPORT"] = "herdr"
		code, out, errText := modelRun(t, f.env, "opencode", ref)
		if code != 0 {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		value := parseModel(t, out)
		assertFields(t, value, legacyFields)
		agentArgs, _ := value["agent_args"].(string)
		if strings.Contains(agentArgs, "--standalone") {
			t.Fatalf("agent_args=%q, the herdr transport keeps no standalone arg", agentArgs)
		}
	})
	t.Run("other-kind-hc", func(t *testing.T) {
		f := newDispatchHcFixture(t, nil)
		code, out, errText := modelRun(t, f.env, "codex", "gpt-5")
		if code != 0 {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		value := parseModel(t, out)
		assertFields(t, value, legacyFields)
		if value["kind"] != "codex" {
			t.Fatalf("kind=%v, want codex", value["kind"])
		}
		agentArgs, _ := value["agent_args"].(string)
		if strings.Contains(agentArgs, "--standalone") {
			t.Fatalf("agent_args=%q, another kind keeps no standalone arg", agentArgs)
		}
	})
	t.Run("malformed-native-reference", func(t *testing.T) {
		f := newDispatchHcFixture(t, nil)
		const spec = "not-a-reference"
		code, out, errText := modelRun(t, f.env, "opencode", spec)
		if code != 2 {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		if out != "" {
			t.Fatalf("stdout=%q, want nothing", out)
		}
		const want = "soho: launch profile: model reference is malformed\n"
		if errText != want {
			t.Fatalf("stderr=%q, want exactly %q", errText, want)
		}
		if strings.Contains(errText, spec) || strings.Contains(out, spec) {
			t.Fatalf("the raw reference was echoed: stderr=%q out=%q", errText, out)
		}
	})
	// The explicit session configuration reaches the same shared resolver:
	// the inline configuration and the configuration directory are consumed
	// from the command's platform.Env (never the process environment), each
	// malformed value is refused with the resolver's own fixed value-free
	// sentinel under the soho prefix (exit 2, no stdout, no raw value echo),
	// the valid pair keeps the exact public profile output, and the
	// non-native and legacy combinations ignore the inputs.
	t.Run("malformed-inline-config", func(t *testing.T) {
		// The root repro: the session's explicit inline configuration is a
		// JSON array, which the pinned shape refuses; before the fix the
		// value never reached the resolver, so the command returned 0 with
		// the primary metadata.
		f := newDispatchHcFixture(t, nil)
		f.env["OPENCODE_CONFIG_CONTENT"] = "[]"
		code, out, errText := modelRun(t, f.env, "opencode", ref)
		if code != 2 {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		if out != "" {
			t.Fatalf("stdout=%q, want nothing", out)
		}
		// The exact value-free line is a fixed public-contract literal,
		// not derived from the resolver.
		const want = "soho: launch profile: inline configuration is not a single non-null JSON object\n"
		if errText != want {
			t.Fatalf("stderr=%q, want exactly %q", errText, want)
		}
		if strings.Contains(errText, "[]") {
			t.Fatalf("the raw inline configuration was echoed: %q", errText)
		}
	})
	t.Run("malformed-agents-build", func(t *testing.T) {
		// A decodable configuration whose agents entry is not an object is
		// refused with a fixed public-contract literal, not derived from
		// the resolver.
		f := newDispatchHcFixture(t, nil)
		const inline = `{"agents":"zcanary"}`
		f.env["OPENCODE_CONFIG_CONTENT"] = inline
		code, out, errText := modelRun(t, f.env, "opencode", ref)
		if code != 2 {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		if out != "" {
			t.Fatalf("stdout=%q, want nothing", out)
		}
		const want = "soho: launch profile: agents or agents.build is not a JSON object\n"
		if errText != want {
			t.Fatalf("stderr=%q, want exactly %q", errText, want)
		}
		if strings.Contains(errText, "zcanary") {
			t.Fatalf("the raw inline configuration was echoed: %q", errText)
		}
	})
	t.Run("malformed-config-dir", func(t *testing.T) {
		// A configuration directory that holds invalid UTF-8 or a NUL is
		// refused before a profile is returned, with a fixed public-contract
		// literal and no echo of the stored value.
		for _, tc := range []struct {
			name string
			dir  string
		}{
			{"invalid-utf8", "\xff\xfe"},
			{"nul", "cfg\x00dir"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := newDispatchHcFixture(t, nil)
				f.env["OPENCODE_CONFIG_DIR"] = tc.dir
				code, out, errText := modelRun(t, f.env, "opencode", ref)
				if code != 2 {
					t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
				}
				if out != "" {
					t.Fatalf("stdout=%q, want nothing", out)
				}
				const want = "soho: launch profile: configuration directory is malformed\n"
				if errText != want {
					t.Fatalf("stderr=%q, want exactly %q", errText, want)
				}
				if strings.Contains(errText, tc.dir) {
					t.Fatalf("the raw configuration directory was echoed: %q", errText)
				}
			})
		}
	})
	t.Run("valid-explicit-config-and-dir", func(t *testing.T) {
		// A valid explicit pair changes nothing public: the known primary
		// profile, the standalone arg alone, no model flag and the public
		// fields only; the configuration bytes and the directory never join
		// the output.
		f := newDispatchHcFixture(t, nil)
		const dir = "cfg"
		f.env["OPENCODE_CONFIG_CONTENT"] = `{}`
		f.env["OPENCODE_CONFIG_DIR"] = dir
		code, out, errText := modelRun(t, f.env, "opencode", ref)
		if code != 0 {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		value := parseModel(t, out)
		assertFields(t, value, nativeFields)
		if value["kind"] != "opencode" || value["spec"] != ref {
			t.Fatalf("echo = %v, want kind and spec echoed", value)
		}
		if value["launch_profile"] != kinds.OpenCodePrimaryProfile {
			t.Fatalf("launch_profile=%v, want %q", value["launch_profile"], kinds.OpenCodePrimaryProfile)
		}
		agentArgs, _ := value["agent_args"].(string)
		if agentArgs != "--standalone" {
			t.Fatalf("agent_args=%q, want the standalone arg alone (no model flag, no env value)", agentArgs)
		}
		if strings.Contains(out, "OPENCODE_") || strings.Contains(out, dir) {
			t.Fatalf("stdout leaks the explicit configuration inputs: %s", out)
		}
	})
	for _, tc := range []struct {
		name, kind, spec, transport string
	}{
		{"other-kind-hc", "codex", "gpt-5", "hc"},
		{"legacy-opencode-herdr", "opencode", ref, "herdr"},
	} {
		t.Run("malformed-values-ignored-"+tc.name, func(t *testing.T) {
			// The non-native and legacy combinations ignore the explicit
			// configuration inputs through the existing resolver semantics:
			// the exact legacy JSON, no profile and no standalone arg, even
			// when both inputs are malformed.
			f := newDispatchHcFixture(t, nil)
			f.env["SOHO_TRANSPORT"] = tc.transport
			f.env["OPENCODE_CONFIG_CONTENT"] = "[]"
			f.env["OPENCODE_CONFIG_DIR"] = "\xff\xfe"
			code, out, errText := modelRun(t, f.env, tc.kind, tc.spec)
			if code != 0 {
				t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
			}
			value := parseModel(t, out)
			assertFields(t, value, legacyFields)
			if value["kind"] != tc.kind {
				t.Fatalf("kind=%v, want %q", value["kind"], tc.kind)
			}
			agentArgs, _ := value["agent_args"].(string)
			if strings.Contains(agentArgs, "--standalone") {
				t.Fatalf("agent_args=%q, the non-native combination keeps no standalone arg", agentArgs)
			}
		})
	}
}
