// Oracle for the DJA-445 approvals default over the frozen legacy config
// goldens (internal/testdata/legacy/parity-config.json).
//
// The frozen config listing records ask as the approvals value from
// defaults. The intentional new default is auto. Adapt only that exact
// complete row, preserving its column widths and every unrelated byte;
// explicit ask values from another source remain unchanged. The frozen
// golden files are never regenerated or edited.
package testutil

import (
	"strings"

	"github.com/djalmajr/soho/internal/jsonjs"
)

const approvalsAskDefaultRow = "approvals          ask                            defaults"
const approvalsAutoDefaultRow = "approvals          auto                           defaults"

// RewriteApprovalsAuto applies the DJA-445 intentional contract adaptation
// to complete config listing lines only. A different value, source, row
// alignment or surrounding text passes through byte-identical, so an
// unrelated contract drift still fails its parity comparison.
func RewriteApprovalsAuto(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line == approvalsAskDefaultRow {
			lines[i] = approvalsAutoDefaultRow
		}
	}
	return strings.Join(lines, "\n")
}

// RewriteApprovalsAutoDoctor adapts the exact default-config summary row
// recorded by parity-doctor.json and the init cases of parity-setup-probe.
// These frozen cases have no explicit approvals setting. This is not a
// normalizer for arbitrary doctor output or for explicit ask policies.
func RewriteApprovalsAutoDoctor(s string) string {
	const old = "ok     config: layout=split approvals=ask auto_approve=off reuse_workers=on multi_role=on worker_context=full"
	const next = "ok     config: layout=split approvals=auto auto_approve=off reuse_workers=on multi_role=on worker_context=full"
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line == old {
			lines[i] = next
		}
	}
	return strings.Join(lines, "\n")
}

func explicitApprovalRequest(args []string) bool {
	for _, arg := range args {
		if arg == "--approvals" || strings.HasPrefix(arg, "--approvals=") {
			return true
		}
	}
	return false
}

// The frozen fresh-spawn cases use these four kinds. These constants are
// the owner-approved DJA-445 auto contract, independent of kinds.go.
func frozenAutoArgs(kind string) (string, bool) {
	switch kind {
	case "grok":
		return "--permission-mode auto", true
	case "agy":
		return "--mode auto", true
	case "codex":
		return "--approve-for-me", true
	case "pi":
		return "", true
	}
	return "", false
}

// RewriteApprovalsAutoSpawnJSON adapts only a fresh, default-ask spawn's
// approvals and agent_args fields in parity-spawn/parity-dispatch. Explicit
// approval flags, reuses, other approval values and other kinds pass through.
// A following dispatch JSON document or collected report is left intact.
func RewriteApprovalsAutoSpawnJSON(out string, request []string) string {
	if explicitApprovalRequest(request) {
		return out
	}
	doc, ok := firstSpawnDoc(out)
	if !ok {
		return out
	}
	v, err := jsonjs.Parse([]byte(doc))
	if err != nil {
		return out
	}
	obj, ok := v.(*jsonjs.Object)
	if !ok {
		return out
	}
	mode, _ := obj.Get("approvals")
	reused, _ := obj.Get("reused")
	if mode != "ask" || reused == true {
		return out
	}
	k, _ := obj.Get("kind")
	kind, _ := k.(string)
	flag, ok := frozenAutoArgs(kind)
	if !ok {
		return out
	}
	a, _ := obj.Get("agent_args")
	args, ok := a.(string)
	if !ok {
		return out
	}
	obj.Set("approvals", "auto")
	if flag != "" {
		obj.Set("agent_args", flag+" "+args)
	}
	return jsonjs.StringifyIndent(obj, 2) + "\n" + out[len(doc):]
}

// RewriteApprovalsAutoStartLog inserts only the new approval prefix after
// the native-argv delimiter of frozen fresh agent-start calls. Existing
// args and every other call stay byte-identical; explicit policies stay put.
func RewriteApprovalsAutoStartLog(log string, request []string) string {
	if explicitApprovalRequest(request) {
		return log
	}
	lines := strings.Split(log, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "agent start ") {
			continue
		}
		head, args, ok := strings.Cut(line, " -- ")
		if !ok {
			continue
		}
		fields := strings.Fields(head)
		kind := ""
		for j := 0; j+1 < len(fields); j++ {
			if fields[j] == "--kind" {
				kind = fields[j+1]
				break
			}
		}
		flag, ok := frozenAutoArgs(kind)
		if !ok || flag == "" || args == flag || strings.HasPrefix(args, flag+" ") {
			continue
		}
		lines[i] = head + " -- " + flag + " " + args
	}
	return strings.Join(lines, "\n")
}

// RewriteApprovalsAutoRoster changes only column 10 of the fresh default
// workers supplied by the consumer. Seeded/legacy rows, missing columns,
// explicit policies and all unrelated columns stay unchanged.
func RewriteApprovalsAutoRoster(roster string, spawned []string) string {
	names := map[string]bool{}
	for _, name := range spawned {
		names[name] = true
	}
	lines := strings.Split(roster, "\n")
	for i, line := range lines {
		fields := strings.Split(line, "\t")
		if len(fields) >= 10 && names[fields[0]] && fields[9] == "ask" {
			fields[9] = "auto"
			lines[i] = strings.Join(fields, "\t")
		}
	}
	return strings.Join(lines, "\n")
}
