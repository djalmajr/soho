package codexenv

import "testing"

// TOML cannot switch back to root after a header. Exercise all six state
// transitions directly as well as the reachable document orders above.
func TestPolicyTableIntroductionForms(t *testing.T) {
	forms := []struct {
		name string
		form policyTableForm
	}{{"header", policyHeader}, {"dotted", policyDotted}, {"inline", policyInline}}
	for _, first := range forms {
		for _, second := range forms {
			t.Run(first.name+"-then-"+second.name, func(t *testing.T) {
				var state policyTableForm
				if !state.introduce(first.form) {
					t.Fatal("first introduction refused")
				}
				want := first.form == policyDotted && second.form == policyDotted
				if got := state.introduce(second.form); got != want {
					t.Fatalf("first=%s second=%s allowed=%v want=%v", first.name, second.name, got, want)
				}
				if state != first.form {
					t.Fatal("refused introduction changed the table origin")
				}
			})
		}
	}
}
