package setup

// Probe response fields and refusal messages are API contracts; formatting is deliberately ignored.
var probeContractCases = map[string]probeScenario{
	"probe-aggregate": {Steps: []probeStep{
		{Args: []string{"setup", "--probe"}, RC: 0, Err: "", Probes: []map[string]interface{}{{"cause": "", "kind": "claude", "model": "opus", "source": "configured", "status": "ready"}, {"cause": "", "kind": "codex", "model": "sol|gpt-5", "source": "configured", "status": "ready"}, {"cause": "", "kind": "grok", "model": "grok", "source": "configured", "status": "ready"}, {"cause": "not installed", "kind": "agy", "model": "gemini|opus", "source": "configured", "status": "error"}, {"cause": "not installed", "kind": "gemini", "model": "", "source": "configured", "status": "error"}, {"cause": "not installed", "kind": "cursor", "model": "grok|muse", "source": "configured", "status": "error"}, {"cause": "", "kind": "pi", "model": "", "source": "configured", "status": "ready"}, {"cause": "not installed", "kind": "opencode", "model": "", "source": "configured", "status": "error"}}, Reviewer: map[string]interface{}{"family": "openai", "kind": "codex", "model": "sol|gpt-5"}},
	}},
	"probe-custom": {Steps: []probeStep{
		{Args: []string{"setup", "--probe"}, RC: 0, Err: "", Probes: []map[string]interface{}{{"cause": "", "kind": "claude", "model": "opus", "source": "configured", "status": "ready"}, {"cause": "", "kind": "codex", "model": "sol|gpt-5", "source": "configured", "status": "ready"}, {"cause": "", "kind": "grok", "model": "grok", "source": "configured", "status": "ready"}, {"cause": "not installed", "kind": "agy", "model": "gemini|opus", "source": "configured", "status": "error"}, {"cause": "not installed", "kind": "gemini", "model": "", "source": "configured", "status": "error"}, {"cause": "not installed", "kind": "cursor", "model": "grok|muse", "source": "configured", "status": "error"}, {"cause": "", "kind": "pi", "model": "", "source": "configured", "status": "ready"}, {"cause": "not installed", "kind": "opencode", "model": "", "source": "configured", "status": "error"}, {"cause": "", "kind": "pi", "model": "own/m1", "source": "custom", "status": "ready"}, {"cause": "", "kind": "pi", "model": "own/m2", "source": "custom", "status": "ready"}, {"cause": "", "kind": "pi", "model": "own/m3", "source": "custom", "status": "ready"}, {"cause": "", "kind": "pi", "model": "own/m4", "source": "custom", "status": "ready"}, {"cause": "", "kind": "pi", "model": "own/m5", "source": "custom", "status": "ready"}}, Reviewer: map[string]interface{}{"family": "openai", "kind": "codex", "model": "sol|gpt-5"}},
	}},
	"probe-error": {Steps: []probeStep{
		{Args: []string{"setup", "--probe", "--kind", "codex"}, RC: 0, Err: "", Probes: []map[string]interface{}{{"cause": "exit 3", "kind": "codex", "model": "sol|gpt-5", "source": "configured", "status": "error"}}, Reviewer: nil},
	}},
	"probe-noauth": {Steps: []probeStep{
		{Args: []string{"setup", "--probe", "--kind", "claude"}, RC: 0, Err: "", Probes: []map[string]interface{}{{"cause": "not authenticated", "kind": "claude", "model": "opus", "source": "configured", "status": "no-auth"}}, Reviewer: nil},
	}},
	"probe-one": {Steps: []probeStep{
		{Args: []string{"setup", "--probe", "--kind", "pi", "--model", "my-provider/my-model"}, RC: 0, Err: "", Probes: []map[string]interface{}{{"cause": "", "kind": "pi", "model": "my-provider/my-model", "source": "configured", "status": "ready"}}, Reviewer: nil},
	}},
	"probe-quota": {Steps: []probeStep{
		{Args: []string{"setup", "--probe", "--kind", "grok"}, RC: 0, Err: "", Probes: []map[string]interface{}{{"cause": "quota exhausted; renews 5 minutes", "kind": "grok", "model": "grok", "source": "configured", "status": "quota"}}, Reviewer: nil},
	}},
	"probe-quotatime": {Steps: []probeStep{
		{Args: []string{"setup", "--probe", "--kind", "grok"}, RC: 0, Err: "", Probes: []map[string]interface{}{{"cause": "quota exhausted; renews 14:30", "kind": "grok", "model": "grok", "source": "configured", "status": "quota"}}, Reviewer: nil},
	}},
	"probe-timeout": {Steps: []probeStep{
		{Args: []string{"setup", "--probe", "--kind", "pi", "--timeout", "1"}, RC: 0, Err: "", Probes: []map[string]interface{}{{"cause": "timeout after 1s; check connectivity, proxy and the provider status before retrying", "kind": "pi", "model": "", "source": "configured", "status": "error"}}, Reviewer: nil},
	}},
	"probe-usage": {Steps: []probeStep{
		{Args: []string{"setup", "--probe", "--kind", "pi", "--timeout", "0"}, RC: 2, Err: "PROG: setup --probe: timeout must be a whole number of seconds ≥ 1\n"},
		{Args: []string{"setup", "--probe", "--kind", "pi", "--timeout", "abc"}, RC: 2, Err: "PROG: setup --probe: timeout must be a whole number of seconds ≥ 1\n"},
		{Args: []string{"setup", "--probe", "--kind", "pi"}, RC: 2, Err: "PROG: setup --probe: timeout must be a whole number of seconds ≥ 1\n"},
		{Args: []string{"setup", "--probe", "--kind", "pi"}, RC: 0, Err: "", Probes: []map[string]interface{}{{"cause": "", "kind": "pi", "model": "", "source": "configured", "status": "ready"}}, Reviewer: nil},
		{Args: []string{"setup", "--probe", "--kind", "notepad"}, RC: 2, Err: "PROG: setup --probe: unknown kind 'notepad' (see: kinds)\n"},
		{Args: []string{"setup", "--probe", "--kind", "--model", "pi"}, RC: 2, Err: "PROG: setup --probe: --kind expects a value\n"},
		{Args: []string{"setup", "--probe", "--model", "requested/only"}, RC: 2, Err: "PROG: setup --probe: --model needs --kind (probe one kind/model: --kind K --model M)\n"},
		{Args: []string{"setup", "--probe", "--kind"}, RC: 2, Err: "PROG: setup --probe: --kind expects a value\n"},
	}},
}
