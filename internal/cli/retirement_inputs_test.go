package cli

// Explicit invocation matrices and exit codes; output is asserted semantically by the runners.
var retirementInvocations = map[string]map[string][]retireStep{
	"entry": {
		"help-variants": {
			{Args: []string{"help"}, RC: 0, Err: ""},
			{Args: []string{"-h"}, RC: 0, Err: ""},
			{Args: []string{"--help"}, RC: 0, Err: ""},
			{Args: []string{}, RC: 0, Err: ""},
		},
		"unknown-command": {
			{Args: []string{"nope"}, RC: 2, Err: "PROG: unknown command 'nope'\n"},
		},
	},
	"status": {
		"friction-empty": {
			{Args: []string{"friction"}, RC: 0, Err: ""},
		},
		"friction-seeded": {
			{Args: []string{"friction"}, RC: 0, Err: ""},
		},
		"roster-empty": {
			{Args: []string{"roster"}, RC: 0, Err: ""},
		},
		"roster-table": {
			{Args: []string{"roster"}, RC: 0, Err: ""},
			{Args: []string{"status", "task"}, RC: 0, Err: "", Columns: [][]string{{"task", "working", "<ROOT>/state/ws/reports/task.md", "<N>", "<N>"}}},
		},
		"status-batch": {
			{Args: []string{"status", "stuck", "dead"}, RC: 4, Err: "PROG: warning: agent 'stuck': herdr agent get failed: Error: Os { code: 13, kind: PermissionDenied, message: \"Permission denied\" }\n", Columns: [][]string{{"stuck", "unavailable", "", "Error: Os { code: 13, kind: PermissionDenied, message: \"Permission denied\" }", "-", "-"}, {"dead", "gone", "", "-", "-"}}},
		},
		"status-blocked": {
			{Args: []string{"status", "worker"}, RC: 0, Err: "", Columns: [][]string{{"worker", "blocked", "", "-", "-"}}},
		},
		"status-denied": {
			{Args: []string{"status", "worker"}, RC: 4, Err: "PROG: warning: agent 'worker': herdr agent get failed: Error: Os { code: 13, kind: PermissionDenied, message: \"Permission denied\" }\n", Columns: [][]string{{"worker", "unavailable", "", "Error: Os { code: 13, kind: PermissionDenied, message: \"Permission denied\" }", "-", "-"}}},
		},
		"status-denied-multi": {
			{Args: []string{"status", "worker"}, RC: 4, Err: "PROG: warning: agent 'worker': herdr agent get failed: Error: Os { code: 13, kind: PermissionDenied, message: \"Permission denied\" } second-line [31mred\n", Columns: [][]string{{"worker", "unavailable", "", "Error: Os { code: 13, kind: PermissionDenied, message: \"Permission denied\" } second-line [31mred", "-", "-"}}},
		},
		"status-down": {
			{Args: []string{"status", "worker"}, RC: 4, Err: "PROG: warning: agent 'worker': herdr agent get failed: server_not_running: no herdr server is running\n", Columns: [][]string{{"worker", "unavailable", "", "server_not_running: no herdr server is running", "-", "-"}}},
		},
		"status-env-errors": {
			{Args: []string{"status"}, RC: 2, Err: "PROG: status: give at least one agent name (herdr-soho roster lists them all)\n"},
			{Args: []string{"status", "worker"}, RC: 2, Err: "PROG: not running inside Herdr (HERDR_ENV != 1); refusing to control a session from outside\n"},
			{Args: []string{"status", "worker"}, RC: 2, Err: "PROG: herdr CLI not found in PATH\n"},
		},
		"status-idle": {
			{Args: []string{"status", "worker"}, RC: 0, Err: "", Columns: [][]string{{"worker", "no-report-yet", "", "-", "-"}}},
		},
		"status-missing": {
			{Args: []string{"status", "worker"}, RC: 4, Err: "PROG: warning: agent 'worker': herdr agent get failed: {\"error\":{\"code\":\"agent_not_found\",\"message\":\"agent target \"$target\" not found\"},\"id\":\"cli:agent:get\"}\n", Columns: [][]string{{"worker", "unavailable", "", "{\"error\":{\"code\":\"agent_not_found\",\"message\":\"agent target \"$target\" not found\"},\"id\":\"cli:agent:get\"}", "-", "-"}}},
		},
		"status-quota": {
			{Args: []string{"status", "build"}, RC: 11, Err: "PROG: warning: quota: agent 'build' lane=build kind=grok model=grok-4.7 : Error: quota exceeded for this account\n"},
		},
		"status-report-wins": {
			{Args: []string{"status", "worker"}, RC: 0, Err: "", Columns: [][]string{{"worker", "done", "<ROOT>/state/ws/reports/worker.md", "0", "-"}}},
		},
		"status-unknown": {
			{Args: []string{"status", "nosuch"}, RC: 0, Err: "", Columns: [][]string{{"nosuch", "unknown-agent", "", "-", "-"}}},
		},
		"status-working": {
			{Args: []string{"status", "worker"}, RC: 0, Err: "", Columns: [][]string{{"worker", "working", "", "-", "-"}}},
		},
	},
	"dispatch": {
		"dispatch-col4": {
			{Args: []string{"dispatch", "res", "brief.md", "--no-wait"}, RC: 0, JSON: []map[string]interface{}{{"agent": "res", "kind": "grok", "role": "researcher", "wait_status": "submitted"}}},
		},
		"dispatch-denied": {
			{Args: []string{"dispatch", "worker", "brief.md", "--timeout", "2000"}, RC: 4, JSON: []map[string]interface{}{{"agent": "worker", "kind": "codex", "role": "implementer", "wait_status": "unavailable"}}},
		},
		"dispatch-family": {
			{Args: []string{"dispatch", "rev", "brief.md", "--resend", "--no-wait"}, RC: 0, JSON: []map[string]interface{}{{"agent": "rev", "kind": "codex", "role": "reviewer", "wait_status": "submitted"}}},
			{Args: []string{"dispatch", "rev", "brief.md", "--resend", "--no-wait"}, RC: 0, JSON: []map[string]interface{}{{"agent": "rev", "kind": "codex", "role": "reviewer", "wait_status": "submitted"}}},
			{Args: []string{"dispatch", "rev", "brief.md", "--no-wait"}, RC: 2, JSON: []map[string]interface{}{}},
			{Args: []string{"dispatch", "rev", "full.md", "--no-wait"}, RC: 0, JSON: []map[string]interface{}{{"agent": "rev", "kind": "codex", "role": "reviewer", "wait_status": "submitted"}}},
		},
		"dispatch-quota": {
			{Args: []string{"dispatch", "build", "brief.md", "--timeout", "5000"}, RC: 11, JSON: []map[string]interface{}{{"agent": "build", "kind": "grok", "lane": "build", "model": "grok-4.7", "role": "implementer", "wait_status": "quota"}}},
		},
		"dispatch-titled": {
			{Args: []string{"dispatch", "build", "tbrief.md", "--no-wait"}, RC: 0, JSON: []map[string]interface{}{{"agent": "build", "kind": "grok", "role": "implementer", "wait_status": "submitted"}}},
			{Args: []string{"dispatch", "build", "brief.md", "--no-wait"}, RC: 0, JSON: []map[string]interface{}{{"agent": "build", "kind": "grok", "role": "implementer", "wait_status": "submitted"}}},
		},
		"run-no-wait": {
			{Args: []string{"run", "implementer", "brief.md", "--no-wait"}, RC: 0, JSON: []map[string]interface{}{{"kind": "grok", "model": "grok", "name": "build", "placement": "herd", "role": "implementer"}, {"agent": "build", "kind": "grok", "role": "implementer", "wait_status": "submitted"}}},
		},
		"run-wait": {
			{Args: []string{"run", "implementer", "brief.md", "--timeout", "5000"}, RC: 0, JSON: []map[string]interface{}{{"kind": "grok", "model": "grok", "name": "build", "placement": "herd", "role": "implementer"}, {"agent": "build", "kind": "grok", "role": "implementer", "wait_status": "done"}}},
		},
	},
}
