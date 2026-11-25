package spawn

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
)

func TestEnvDropRosterCodec(t *testing.T) {
	legacy := make([]string, envDropColumn)
	legacy[16], legacy[17] = "4444", "approval column"
	names, err := RosterEnvDrop(legacy)
	if names != nil || err != nil {
		t.Fatalf("legacy row acquired a policy: %v %v", names, err)
	}
	var sixteen []string
	for i := 0; i < 16; i++ {
		sixteen = append(sixteen, fmt.Sprintf("DROP_%d", i))
	}
	marker := envDropPrefix + strings.Join(sixteen, ",")
	names, err = RosterEnvDrop(append(legacy, marker))
	if !slices.Equal(names, sixteen) || err != nil {
		t.Fatalf("16 names parsed as %v %v", names, err)
	}
	for _, malformed := range []string{
		"env-drop:v2:A", envDropPrefix, envDropPrefix + "A,A", envDropPrefix + "A,a",
		marker + ",EXTRA", envDropPrefix + strings.Repeat("X", envDropMarkerSize),
	} {
		if names, err := RosterEnvDrop(append(legacy, malformed)); err == nil || names != nil {
			t.Fatal("invalid roster marker accepted")
		}
	}
}

func TestEnvDropRosterPreservedByWorkerUpdates(t *testing.T) {
	dir := t.TempDir()
	fields := []string{"worker", "t7", "pi", "implementer", "", "1", dir, "now", "", "auto", "implementer", "", "", "", "", "inherit", "4444", "", envDropPrefix + "ONE,TWO"}
	core.RosterAppend(dir, fields)
	core.RosterSetRole(dir, "worker", "reviewer")
	core.RosterSetModel(dir, "worker", "provider/model", "low")
	core.RosterReplacePane(dir, "t7", "t8")
	ok, err := core.RosterRenameIf(dir, "worker", "renamed", core.RosterLine(dir, "worker"))
	if !ok || err != nil {
		t.Fatalf("rename = %v, %v", ok, err)
	}
	row := strings.Split(core.RosterLine(dir, "renamed"), "\t")
	names, err := RosterEnvDrop(row)
	if !slices.Equal(names, []string{"ONE", "TWO"}) || err != nil || core.RosterE2EPort(row) != "4444" || row[1] != "t8" {
		t.Fatalf("rewritten row lost identity or policy: %q, %v", row, err)
	}
}
