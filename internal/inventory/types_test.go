package inventory

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func strp(s string) *string    { return &s }
func int64p(v int64) *int64    { return &v }
func reasonp(r Reason) *Reason { return &r }

func marshalToMap(t *testing.T, v any) (map[string]any, string) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("Unmarshal round-trip: %v", err)
	}
	return m, string(raw)
}

func keySet(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// sampleDocument is the representative document of the contract's
// "Format" example, extended with the owner reason and a non-ASCII task.
func sampleDocument(t *testing.T) Document {
	t.Helper()
	task := "Correção: reprovado o cartão rejeitado na fatura"
	return Document{
		Schema:      Schema,
		SohoVersion: "v0.3.1",
		GeneratedMS: 1793376000000,
		Projects: []Project{{
			Name: "acme/web",
			Sessions: []Session{{
				ID: "w1",
				Agents: []Agent{
					{
						Terminal:    "w1:p1",
						Name:        "orchestrator",
						Role:        "orchestrator",
						Kind:        "claude",
						Model:       nil,
						Effort:      nil,
						State:       StateWorking,
						Reason:      nil,
						Task:        strp("Retry declined cards at checkout"),
						ReportReady: false,
						StartedMS:   1793372400000,
						SinceMS:     nil,
					},
					{
						Terminal:    "w1:p2",
						Name:        "build",
						Role:        "implementer",
						Kind:        "codex",
						Model:       strp("example/model-1"),
						Effort:      strp("high"),
						State:       StateNeedsYou,
						Reason:      reasonp(ReasonOwner),
						Task:        strp(task),
						ReportReady: false,
						StartedMS:   1793373000000,
						SinceMS:     int64p(1793375990000),
					},
					{
						Terminal:    "w1:p3",
						Name:        "review",
						Role:        "reviewer",
						Kind:        "pi",
						Model:       nil,
						Effort:      strp("xhigh"),
						State:       StateDone,
						Reason:      nil,
						Task:        nil,
						ReportReady: true,
						StartedMS:   1793373800000,
						SinceMS:     nil,
					},
				},
			}},
		}},
		Hidden: Counts{},
	}
}

// TestDocumentSerializesTheContractShape asserts the actual JSON keys,
// nulls, booleans and the five zero hidden counts against values derived
// from the contract, not from a second implementation run.
func TestDocumentSerializesTheContractShape(t *testing.T) {
	m, raw := marshalToMap(t, sampleDocument(t))

	// Top level: exactly the contract's five keys while truncated is
	// false (the only omitempty field, omitted when false).
	wantTop := []string{"generated_ms", "hidden", "projects", "schema", "soho_version"}
	if got := keySet(m); !reflect.DeepEqual(got, wantTop) {
		t.Fatalf("top-level keys = %v, want %v", got, wantTop)
	}
	if v := m["schema"]; v != float64(Schema) {
		t.Fatalf("schema = %v, want %v", v, Schema)
	}
	if v := m["soho_version"]; v != "v0.3.1" {
		t.Fatalf("soho_version = %v", v)
	}
	if v := m["generated_ms"]; v != float64(1793376000000) {
		t.Fatalf("generated_ms = %v", v)
	}

	// Hidden: all five state keys present, at zero.
	hidden, ok := m["hidden"].(map[string]any)
	if !ok {
		t.Fatalf("hidden = %T, want object", m["hidden"])
	}
	wantHidden := []string{"done", "idle", "needs_you", "unknown", "working"}
	if got := keySet(hidden); !reflect.DeepEqual(got, wantHidden) {
		t.Fatalf("hidden keys = %v, want %v", got, wantHidden)
	}
	for _, key := range wantHidden {
		if v := hidden[key]; v != float64(0) {
			t.Fatalf("hidden.%s = %v, want 0 (key present at zero)", key, v)
		}
	}

	// Project and session shape.
	projects, ok := m["projects"].([]any)
	if !ok || len(projects) != 1 {
		t.Fatalf("projects = %v, want one project", m["projects"])
	}
	project := projects[0].(map[string]any)
	if got := keySet(project); !reflect.DeepEqual(got, []string{"name", "sessions"}) {
		t.Fatalf("project keys = %v", got)
	}
	if v := project["name"]; v != "acme/web" {
		t.Fatalf("project.name = %v", v)
	}
	sessions := project["sessions"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d entries, want 1", len(sessions))
	}
	session := sessions[0].(map[string]any)
	if got := keySet(session); !reflect.DeepEqual(got, []string{"agents", "id"}) {
		t.Fatalf("session keys = %v", got)
	}
	if v := session["id"]; v != "w1" {
		t.Fatalf("session.id = %v", v)
	}

	// Agents: the contract's twelve keys, no more, no less.
	agents := session["agents"].([]any)
	if len(agents) != 3 {
		t.Fatalf("agents = %d entries, want 3", len(agents))
	}
	wantAgent := []string{"effort", "kind", "model", "name", "reason", "report_ready", "role", "since_ms", "started_ms", "state", "task", "terminal"}
	for i, entry := range agents {
		agent := entry.(map[string]any)
		if got := keySet(agent); !reflect.DeepEqual(got, wantAgent) {
			t.Fatalf("agent %d keys = %v, want %v", i, got, wantAgent)
		}
	}
	a0 := agents[0].(map[string]any)
	a1 := agents[1].(map[string]any)
	a2 := agents[2].(map[string]any)

	// The nullable keys are present as explicit nulls.
	for name, key := range map[string]string{"model a0": "model", "effort a0": "effort", "reason a0": "reason", "since_ms a0": "since_ms"} {
		v, present := a0[key]
		if !present {
			t.Fatalf("%s: key must be present as null, not omitted", name)
		}
		if v != nil {
			t.Fatalf("%s = %v, want null", name, v)
		}
	}
	if v, present := a2["task"]; !present || v != nil {
		t.Fatalf("task a2 = %v (present %v), want null", v, present)
	}

	// Fixed fields round-trip.
	if a0["state"] != "working" || a0["report_ready"] != false || a0["started_ms"] != float64(1793372400000) || a0["task"] != "Retry declined cards at checkout" {
		t.Fatalf("a0 fixed fields = %v", a0)
	}
	if a1["state"] != "needs_you" || a1["reason"] != "owner" || a1["model"] != "example/model-1" || a1["effort"] != "high" || a1["since_ms"] != float64(1793375990000) {
		t.Fatalf("a1 needs_you/owner fields = %v", a1)
	}
	if a2["state"] != "done" || a2["report_ready"] != true || a2["reason"] != nil {
		t.Fatalf("a2 done fields = %v", a2)
	}

	// The non-ASCII task survives verbatim in the raw JSON (no escaping,
	// no control data), and the value round-trips to the original string.
	task := "Correção: reprovado o cartão rejeitado na fatura"
	if a1["task"] != task {
		t.Fatalf("a1 task = %q, want %q", a1["task"], task)
	}
	if !strings.Contains(raw, `"task":"`+task+`"`) {
		t.Fatalf("raw JSON does not carry the non-ASCII task verbatim:\n%s", raw)
	}
	for _, r := range task {
		if r < 0x20 || r == 0x7f {
			t.Fatalf("task carries control data: %q", task)
		}
	}
}

// TestDocumentTruncatedOmittedAndPresent pins the single omitempty field:
// absent while false, present while true, and projects serializes as an
// empty array (never omitted) when the producer assigns the empty list.
// A nil slice is Go's null; the contract's "projects is always present"
// rule is the producer's to honor by assigning a non-nil slice.
func TestDocumentTruncatedOmittedAndPresent(t *testing.T) {
	empty := Document{Schema: Schema, SohoVersion: "v0.3.1", GeneratedMS: 1, Projects: []Project{}}
	m, raw := marshalToMap(t, empty)
	if _, present := m["truncated"]; present {
		t.Fatalf("truncated must be omitted while false: %s", raw)
	}
	if !strings.Contains(raw, `"projects":[]`) {
		t.Fatalf("projects must serialize as an empty array, not null: %s", raw)
	}
	hidden := m["hidden"].(map[string]any)
	for _, key := range []string{"working", "needs_you", "done", "idle", "unknown"} {
		if v, present := hidden[key]; !present || v != float64(0) {
			t.Fatalf("hidden.%s = %v (present %v), want 0", key, v, present)
		}
	}
	empty.Truncated = true
	m2, raw2 := marshalToMap(t, empty)
	if v, present := m2["truncated"]; !present || v != true {
		t.Fatalf("truncated = %v (present %v), want true in %s", v, present, raw2)
	}
}

// TestAgentNullsAreExplicitKeys asserts that every "or null" field of the
// contract serializes as an explicit null key and that the required
// fields are never omitted.
func TestAgentNullsAreExplicitKeys(t *testing.T) {
	m, raw := marshalToMap(t, Agent{Terminal: "w1:p1", Name: "orchestrator", Role: "orchestrator", Kind: "claude", State: StateIdle})
	for _, key := range []string{"model", "effort", "reason", "task", "since_ms"} {
		if !strings.Contains(raw, `"`+key+`":null`) {
			t.Fatalf("raw JSON lacks the explicit null for %s: %s", key, raw)
		}
		if v, present := m[key]; !present || v != nil {
			t.Fatalf("%s = %v (present %v), want null", key, v, present)
		}
	}
	for _, key := range []string{"terminal", "name", "role", "kind", "state", "report_ready", "started_ms"} {
		if _, present := m[key]; !present {
			t.Fatalf("required key %s is omitted", key)
		}
	}
	if m["state"] != "idle" || m["report_ready"] != false || m["started_ms"] != float64(0) {
		t.Fatalf("fixed fields = %v", m)
	}
}

// TestStateAndReasonConstantsPinTheContractVocabulary asserts the typed
// constants are exactly the contract's five states and eight reasons.
func TestStateAndReasonConstantsPinTheContractVocabulary(t *testing.T) {
	states := map[State]string{
		StateWorking:  "working",
		StateNeedsYou: "needs_you",
		StateDone:     "done",
		StateIdle:     "idle",
		StateUnknown:  "unknown",
	}
	if len(states) != 5 {
		t.Fatalf("states = %d values, want 5 distinct ones", len(states))
	}
	for state, want := range states {
		if string(state) != want {
			t.Fatalf("state constant = %q, want %q", state, want)
		}
	}
	reasons := map[Reason]string{
		ReasonQuestion:    "question",
		ReasonApproval:    "approval",
		ReasonQuota:       "quota",
		ReasonProvider:    "provider",
		ReasonNotReceived: "not_received",
		ReasonNoReport:    "no_report",
		ReasonCompact:     "compact",
		ReasonOwner:       "owner",
	}
	if len(reasons) != 8 {
		t.Fatalf("reasons = %d values, want 8 distinct ones", len(reasons))
	}
	for r, want := range reasons {
		if string(r) != want {
			t.Fatalf("reason constant = %q, want %q", r, want)
		}
	}
}
