package cli

// Review round 2 (forms-r-20261119T180825 F3): collaborate start keeps its
// own concrete-input contract, so a plain token that names an existing file
// (build.spec-template) still starts an assignment, while the build.go
// control keeps starting through the shared grammar.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/collaboration"
)

func TestCollaborateStartConcretePlainInput(t *testing.T) {
	for _, filename := range []string{"build.spec-template", "build.go"} {
		t.Run(filename, func(t *testing.T) {
			f, brief := collaborationFixture(t, false)
			if err := os.WriteFile(filepath.Join(f.repo, filename), []byte("concrete input\n"), 0600); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(brief)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(brief, []byte(strings.Replace(string(body), "- src/app.go", "- "+filename, 1)), 0600); err != nil {
				t.Fatal(err)
			}
			code, out, stderr := f.run(t, f.env, "collaborate", "start", "author", "reviewer", "--brief", brief)
			var assignment collaboration.Assignment
			if code != 0 || json.Unmarshal([]byte(out), &assignment) != nil || !reflect.DeepEqual(assignment.Paths, []string{filename}) {
				t.Fatalf("start code=%d paths=%q stderr=%q", code, assignment.Paths, stderr)
			}
		})
	}
}
