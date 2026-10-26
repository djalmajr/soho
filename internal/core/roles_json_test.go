package core

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/jsonjs"
)

// DJA-375 A: soho roles --json prints one JSON object per line whose roles
// carry the same values and the same order/deduplication as the text table.

// rolesTextRow parses one row of the text table. The columns are fixed-width
// (padUTF16, no truncation) as long as every value fits its width; the
// separators are asserted to be spaces so a fixture value that grows past
// its column fails loudly instead of silently misparsing.
func rolesTextRow(t *testing.T, line string) (name, kind, model, effort, mode, from string) {
	t.Helper()
	if len(line) < 73 {
		t.Fatalf("roles row too short: %q", line)
	}
	for _, sep := range []int{18, 27, 52, 61, 72} {
		if line[sep] != ' ' {
			t.Fatalf("roles row columns shifted (fixture value too long): %q", line)
		}
	}
	name = strings.TrimRight(line[0:18], " ")
	kind = strings.TrimRight(line[19:27], " ")
	model = strings.TrimRight(line[28:52], " ")
	effort = strings.TrimRight(line[53:61], " ")
	mode = strings.TrimRight(line[62:72], " ")
	from = line[73:]
	return
}

var rolesFromRE = regexp.MustCompile(`^source: (skill(?:\+append)?|project(?:\+append)?); kind: (.+); model: (.+); effort: (.+); file: (.+)$`)

func TestCmdRolesJSONMatchesTheTextTable(t *testing.T) {
	// A lane config with roles and a project role that no lane claims: the
	// JSON must agree with the text table field by field, in the same order,
	// with empty values as "" (the text shows "-").
	env, repo := fixture(t)
	conf := filepath.Join(repo, ".agents", "soho.conf")
	write(t, conf, "lane.build.roles=designer,tasker\nlane.build.kind=pi\nlane.build.model=lane-model\nlane.build.effort=xhigh\n")
	customFile := filepath.Join(repo, ".agents", "soho-roles", "custom.md")
	write(t, customFile, "---\nmode: edit\n---\n")

	textOut, textErr := capture(t, func() { CmdRoles(env, repo) })
	if textErr != "" {
		t.Fatalf("roles text stderr = %q", textErr)
	}
	jsonOut, jsonErr := capture(t, func() { CmdRolesJSON(env, repo) })
	if jsonErr != "" {
		t.Fatalf("roles JSON stderr = %q", jsonErr)
	}

	lines := strings.Split(strings.TrimSuffix(textOut, "\n"), "\n")
	if len(lines) < 2 || lines[0] == "" || !strings.Contains(lines[0], "ROLE") {
		t.Fatalf("roles text table incomplete: %q", textOut)
	}
	textRows := lines[1:]

	parsed, err := jsonjs.Parse([]byte(jsonOut))
	if err != nil {
		t.Fatalf("roles JSON does not parse: %v\n%s", err, jsonOut)
	}
	root, ok := parsed.(*jsonjs.Object)
	if !ok {
		t.Fatalf("roles JSON root is %T, want object", parsed)
	}
	if got := root.Keys(); len(got) != 1 || got[0] != "roles" {
		t.Fatalf("roles JSON top-level keys = %v, want [roles]", got)
	}
	roles, ok := root.Get("roles")
	if !ok {
		t.Fatalf("roles JSON has no roles key: %s", jsonOut)
	}
	arr, ok := roles.([]any)
	if !ok {
		t.Fatalf("roles JSON roles is %T, want array", roles)
	}
	if len(arr) != len(textRows) {
		t.Fatalf("roles JSON has %d roles, text table has %d rows:\n%s", len(arr), len(textRows), jsonOut)
	}

	wantFirst := `{"role":"custom","kind":"","model":"","effort":"","mode":"edit","lane":"","kind_from":"default","model_from":"default","effort_from":"default","file":` + jsonjs.Stringify(customFile) + `,"source":"project"}`
	if !strings.Contains(jsonOut, wantFirst) {
		t.Fatalf("first JSON role does not pin the key order and the empty values as \"\":\ngot  %s\nwant it to contain %s", jsonOut, wantFirst)
	}
	if !strings.HasPrefix(jsonOut, `{"roles":[`) || !strings.HasSuffix(jsonOut, "}\n") {
		t.Fatalf("roles JSON is not one object on one line: %q", jsonOut)
	}

	for i, entry := range arr {
		role, ok := entry.(*jsonjs.Object)
		if !ok {
			t.Fatalf("roles[%d] is %T, want object", i, entry)
		}
		if keys := role.Keys(); strings.Join(keys, ",") != "role,kind,model,effort,mode,lane,kind_from,model_from,effort_from,file,source" {
			t.Fatalf("roles[%d] key order = %v", i, keys)
		}
		name, kind, model, effort, mode, from := rolesTextRow(t, textRows[i])
		fromFields := rolesFromRE.FindStringSubmatch(from)
		if fromFields == nil {
			t.Fatalf("roles row %d does not carry the from column: %q", i, textRows[i])
		}
		// uncell inverts the text table's "-" cell: the JSON keeps the empty
		// value as "".
		uncell := func(v string) string {
			if v == "-" {
				return ""
			}
			return v
		}
		got := func(key string) string {
			v, _ := role.Get(key)
			s, _ := v.(string)
			return s
		}
		if got("role") != name || got("kind") != uncell(kind) || got("model") != uncell(model) || got("effort") != uncell(effort) || got("mode") != uncell(mode) {
			t.Fatalf("roles[%d] diverges from the text row:\ngot   role=%q kind=%q model=%q effort=%q mode=%q\nwant role=%q kind=%q model=%q effort=%q mode=%q",
				i, got("role"), got("kind"), got("model"), got("effort"), got("mode"), name, uncell(kind), uncell(model), uncell(effort), uncell(mode))
		}
		if got("source") != fromFields[1] || got("kind_from") != fromFields[2] || got("model_from") != fromFields[3] || got("effort_from") != fromFields[4] || got("file") != fromFields[5] {
			t.Fatalf("roles[%d] from fields diverge:\ngot   source=%q kind_from=%q model_from=%q effort_from=%q file=%q\nwant source=%q kind_from=%q model_from=%q effort_from=%q file=%q",
				i, got("source"), got("kind_from"), got("model_from"), got("effort_from"), got("file"), fromFields[1], fromFields[2], fromFields[3], fromFields[4], fromFields[5])
		}
		wantLane := ""
		if name == "designer" || name == "tasker" {
			wantLane = "build"
		}
		if got("lane") != wantLane {
			t.Fatalf("roles[%d] lane = %q, want %q (the lane shows only for the lane's roles)", i, got("lane"), wantLane)
		}
	}

	// The text table itself must stay untouched by the JSON mode: run it
	// again and compare byte for byte with the first capture.
	again, againErr := capture(t, func() { CmdRoles(env, repo) })
	if againErr != "" || again != textOut {
		t.Fatalf("roles text changed after the JSON run: %q vs %q", again, textOut)
	}
}
