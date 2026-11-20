package wait

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// SOH-442 review F1: a real dispatch keeps the composed brief only in the
// state dir's briefs/, beside its sidecar. The declared list must be read
// through that sidecar's stem, ahead of any brief beside the report.
func TestDeclaredTestLayersReadThroughStateBriefsSidecar(t *testing.T) {
	want := DeclaredTestLayers("Test layers: unit, mutation\n")
	if len(want) != 2 {
		t.Fatalf("fixture line parsed as %v", want)
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, ext := range []string{".brief.md", ".md"} {
		t.Run(ext, func(t *testing.T) {
			sd := t.TempDir()
			reports := t.TempDir()
			const base = "worker-20261120T000000"
			report := filepath.Join(reports, base+".md")
			stem := filepath.Join(sd, "briefs", base)
			write(stem+".dispatch.json", `{"version":1}`)
			write(stem+ext, "# Brief\n\nType: backend\nTest layers: unit, mutation\n")
			// A brief beside the report without the line must not win.
			write(filepath.Join(reports, base+".brief.md"), "# Brief\n\nType: backend\n")
			if got := declaredTestLayersForReport(sd, report); !reflect.DeepEqual(got, want) {
				t.Fatalf("declared layers = %v, want %v", got, want)
			}
		})
	}
}
