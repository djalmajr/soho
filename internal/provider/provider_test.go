package provider

import (
	"encoding/json"
	herdrtext "github.com/djalmajr/soho/internal/text"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestProviderDetectCases(t *testing.T) {
	t.Run("providerDetect: the brief examples and counterexamples", func(t *testing.T) {
		// JS: "providerDetect: the brief examples and counterexamples"
		for _, row := range []struct{ screen, status string }{
			{`Error: Retry failed after 3 attempts: 503: {"message":"inference capacity exhausted","type":"model_capacity"}`, "capacity"},
			{`Error: 429: {"message":"too many concurrent requests for this key","type":"key_capacity"}`, "capacity"},
			{`API Error: 529 {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, "capacity"},
			{"Error: Retry failed after 3 attempts: Request timed out.", "provider-error"},
			{"Error: Connection error.", "provider-error"},
			{"■ stream disconnected before completion: error sending request", "provider-error"},
		} {
			if got := ProviderDetect("idle", row.screen); got == nil || got.Status != row.status || got.Cause == "" {
				t.Errorf("ProviderDetect(%q) = %#v", row.screen, got)
			}
		}
		for _, screen := range []string{"error: expected ';' at line 4", `  const msg = "Error: Request timed out";`, `throw new Error("Request timed out")`} {
			if got := ProviderDetect("idle", screen); got != nil {
				t.Errorf("ProviderDetect(%q) = %#v, want nil", screen, got)
			}
		}
	})
	t.Run("providerDetect: the in-flight Retrying line is ignored", func(t *testing.T) {
		// JS: "providerDetect: the in-flight Retrying line is ignored"
		for _, screen := range []string{"API Error (Request timed out) · Retrying in 5 seconds…", "Will retry in 30 seconds", "Reconnecting…"} {
			if got := ProviderDetect("idle", screen); got != nil {
				t.Errorf("retry line matched: %#v", got)
			}
		}
		if got := ProviderDetect("idle", "Error: Connection error.\nReconnecting…"); got == nil || got.Status != "provider-error" {
			t.Errorf("older connection error = %#v", got)
		}
		if got := ProviderDetect("idle", "Error: 529 at capacity\nretrying next request"); got == nil || got.Status != "capacity" {
			t.Errorf("older capacity error = %#v", got)
		}
	})
	t.Run("providerDetect: never while working, never on an empty screen", func(t *testing.T) {
		// JS: "providerDetect: never while working, never on an empty screen"
		for _, row := range []struct{ state, screen string }{{"working", "Error: Connection error."}, {"working", `API Error: 529 {"type":"overloaded_error"}`}, {"idle", ""}, {"idle", "\n\n"}} {
			if got := ProviderDetect(row.state, row.screen); got != nil {
				t.Errorf("ProviderDetect(%q, %q) = %#v", row.state, row.screen, got)
			}
		}
	})
	t.Run("providerDetect: CRLF screens are normalized before the scan", func(t *testing.T) {
		// JS: "providerDetect: CRLF screens are normalized before the scan"
		for _, screen := range []string{"Error: Connection error.\r\n", "first\r\nError: 503 Service Unavailable\r\n"} {
			if got := ProviderDetect("idle", screen); got == nil || got.Status != "provider-error" {
				t.Errorf("ProviderDetect(%q) = %#v", screen, got)
			}
		}
	})
	t.Run("providerDetect: bottom-up scan, the most recent line wins", func(t *testing.T) {
		// JS: "providerDetect: bottom-up scan, the most recent line wins"
		for _, row := range []struct{ screen, status string }{
			{"Error: Request timed out.\n■ at capacity\n", "capacity"},
			{"■ at capacity\nError: 502: Bad Gateway\n", "provider-error"},
			{"■ something else broke\nError: Connection error.\n", "provider-error"},
		} {
			if got := ProviderDetect("idle", row.screen); got == nil || got.Status != row.status {
				t.Errorf("ProviderDetect = %#v, want %s", got, row.status)
			}
		}
	})
	t.Run("providerDetect: a line matching both is capacity", func(t *testing.T) {
		// JS: "providerDetect: a line matching both is capacity"
		if got := ProviderDetect("idle", `Error: 503: {"type":"overload","message":"Request timed out"}`); got == nil || got.Status != "capacity" {
			t.Fatalf("ProviderDetect() = %#v", got)
		}
	})
	t.Run("providerDetect: the remaining capacity and provider-error patterns", func(t *testing.T) {
		// JS: "providerDetect: the remaining capacity and provider-error patterns"
		for _, row := range []struct{ screen, status string }{
			{"Error: 503 at capacity, try later", "capacity"}, {"ERROR: OVERLOADED", "capacity"}, {"Error: 529", "capacity"},
			{"Error: connect ECONNREFUSED", "provider-error"}, {"Error: ECONNRESET", "provider-error"},
			{"Error: connect connection refused", "provider-error"}, {"Error: connect connection reset by peer", "provider-error"},
			{"Error: Retry failed after 1 attempt", "provider-error"}, {"Error: 500 Internal Server Error", "provider-error"},
			{"Error: 502: Bad Gateway", "provider-error"}, {"Error: 503 (Service Unavailable)", "provider-error"},
			{"Error: 504 Gateway Timeout", "provider-error"}, {"Error: 504 gateway time-out", "provider-error"},
			{"■ socket hang up", "provider-error"}, {"■ fetch failed", "provider-error"},
			{"✗ Error: stream disconnected before completion: timeout", "provider-error"},
		} {
			if got := ProviderDetect("idle", row.screen); got == nil || got.Status != row.status {
				t.Errorf("ProviderDetect(%q) = %#v, want %s", row.screen, got, row.status)
			}
		}
	})
	t.Run("providerDetect: non-provider error lines and code lines stay null", func(t *testing.T) {
		// JS: "providerDetect: non-provider error lines and code lines stay null"
		for _, screen := range []string{"■ installing dependencies", "Error: expected a semicolon", "error: Request timed out", "ERRORS: none found", "Error handling is fine", "// Error: Connection error.", "# Error: 503 at capacity", `result = "Error: Request timed out"`, `return "Error: 502: Bad Gateway"`, "Error: econnrefused", "Error: Econnreset"} {
			if got := ProviderDetect("idle", screen); got != nil {
				t.Errorf("ProviderDetect(%q) = %#v, want nil", screen, got)
			}
		}
	})
	t.Run("providerDetect: the cause is the redacted, sanitized line", func(t *testing.T) {
		// JS: "providerDetect: the cause is the redacted, sanitized line"
		got := ProviderDetect("idle", "■ Error: Request timed out token=sk_live_abcdefghij")
		if got == nil || got.Status != "provider-error" || got.Cause != "Error: Request timed out token=[redacted]" {
			t.Fatalf("ProviderDetect() = %#v", got)
		}
		long := ProviderDetect("idle", "Error: Connection error "+strings.Repeat("x", 300))
		if long == nil || len(long.Cause) != 200 {
			t.Fatalf("long cause length = %#v", long)
		}
	})
	t.Run("providerDetect: ordinary tool output with provider words is not a stop", func(t *testing.T) {
		// JS: "providerDetect: ordinary tool output with provider words is not a stop"
		for _, screen := range []string{"• Ran the health check: Connection error on the first try, fine after", "● The docs page said: Request timed out, so I used the local copy"} {
			if got := ProviderDetect("idle", screen); got != nil {
				t.Errorf("tool output matched: %#v", got)
			}
		}
		for _, row := range []struct{ screen, status string }{{"■ unexpected status 503 Service Unavailable", "provider-error"}, {"  ⎿  API Error: 529 {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\"}}", "capacity"}} {
			if got := ProviderDetect("idle", row.screen); got == nil || got.Status != row.status {
				t.Errorf("ProviderDetect = %#v", got)
			}
		}
	})
	t.Run("providerDetect: only the bottom of the screen counts", func(t *testing.T) {
		// JS: "providerDetect: only the bottom of the screen counts"
		footer := "\n────────\n \n────────\n~/work (main)\n↑48k ↓933 8.6%/262k (auto)   (my-provider) my-model • high\n"
		if got := ProviderDetect("idle", "Error: Retry failed after 3 attempts: Request timed out."+footer); got == nil || got.Status != "provider-error" {
			t.Errorf("bottom error = %#v", got)
		}
		later := strings.Join([]string{"• step 1 done", "• step 2 done", "• step 3 done", "• step 4 done", "• step 5 done", "• step 6 done", "• step 7 done", "• step 8 done", "• step 9 done", "• step 10 done"}, "\n")
		if got := ProviderDetect("idle", "Error: Connection error.\n"+later+footer); got != nil {
			t.Errorf("recovered error matched: %#v", got)
		}
	})
	t.Run("providerDetect: agent output below an error ends the search", func(t *testing.T) {
		// JS: "providerDetect: agent output below an error ends the search"
		for _, screen := range []string{"Error: Connection error.\n• Ran the tests again\n  └ 12 passed\n›\n  my-model · 40% left", "  ⎿  API Error: Request timed out\n● Wrote the report\n>\n  ? for shortcuts"} {
			if got := ProviderDetect("idle", screen); got != nil {
				t.Errorf("recovered error matched: %#v", got)
			}
		}
		if got := ProviderDetect("idle", "• Ran the tests\nError: Connection error.\n›\n  my-model · 40% left"); got == nil || got.Status != "provider-error" {
			t.Errorf("error below output = %#v", got)
		}
	})
	t.Run("providerDetect: authentication failures are terminal provider-errors", func(t *testing.T) {
		// JS: "providerDetect: authentication failures are terminal provider-errors"
		for _, screen := range []string{"Error: 401 Unauthorized", `Error: 401: {"message":"Incorrect API key provided"}`, "API Error: Incorrect API key provided", "■ unexpected status 401: authentication failed", "■ Failed to refresh access token: the refresh token was revoked, please log in again", "Error: Failed to refresh access token"} {
			if got := ProviderDetect("idle", screen); got == nil || got.Status != "provider-error" || !got.Auth {
				t.Errorf("auth ProviderDetect(%q) = %#v", screen, got)
			}
		}
		if got := ProviderDetect("idle", "Error: Connection error."); got == nil || got.Auth {
			t.Errorf("transient = %#v", got)
		}
		if got := ProviderDetect("idle", "Error: 529 Incorrect API key"); got == nil || got.Status != "capacity" {
			t.Errorf("capacity/auth precedence = %#v", got)
		}
		for _, row := range []struct {
			screen string
			auth   bool
		}{{"Error: 401 Unauthorized\nError: Connection error.\n", false}, {"Error: Connection error.\nError: 401 Unauthorized\n", true}} {
			if got := ProviderDetect("idle", row.screen); got == nil || got.Auth != row.auth {
				t.Errorf("newest match = %#v", got)
			}
		}
	})
	t.Run("providerDetect: auth false positives stay null", func(t *testing.T) {
		// JS: "providerDetect: auth false positives stay null"
		for _, row := range []struct{ state, screen string }{{"working", "Error: 401 Unauthorized"}, {"idle", `  const msg = "Error: 401 Unauthorized";`}, {"idle", "// Error: Incorrect API key provided"}, {"idle", "• Ran the health check: 401 on the first try, fine after"}, {"idle", "● The docs said the refresh token was revoked, so I used the local copy"}, {"idle", "error: 401 unauthorized"}, {"idle", `throw new Error("Incorrect API key")`}, {"idle", "Error: expected a semicolon"}} {
			if got := ProviderDetect(row.state, row.screen); got != nil {
				t.Errorf("false-positive ProviderDetect = %#v", got)
			}
		}
	})
	t.Run("providerDetect: the auth cause is redacted and sanitized", func(t *testing.T) {
		// JS: "providerDetect: the auth cause is redacted and sanitized"
		got := ProviderDetect("idle", "Error: 401 Unauthorized token=sk_test_fakekey000")
		if got == nil || !got.Auth || got.Cause != "Error: 401 Unauthorized token=[redacted]" || strings.Contains(got.Cause, "sk_test_fakekey000") {
			t.Fatalf("auth cause = %#v", got)
		}
	})
	t.Run("providerDetect: non-401 unexpected statuses are transient, not auth", func(t *testing.T) {
		// JS: "providerDetect: non-401 unexpected statuses are transient, not auth"
		for _, screen := range []string{"Error: unexpected status 503 Service Unavailable", "Error: unexpected status 500 Internal Server Error", "■ unexpected status 503 Service Unavailable"} {
			if got := ProviderDetect("idle", screen); got == nil || got.Status != "provider-error" || got.Auth {
				t.Errorf("transient = %#v", got)
			}
		}
		if got := ProviderDetect("idle", "Error: unexpected status 401: authentication failed"); got == nil || !got.Auth {
			t.Errorf("401 = %#v", got)
		}
	})
	t.Run("providerDetect: refresh matching needs a revoked token or a failed token refresh", func(t *testing.T) {
		// JS: "providerDetect: refresh matching needs a revoked token or a failed token refresh"
		for _, screen := range []string{"■ Failed to refresh access token: the refresh token was revoked, please log in again", "Error: the refresh token was revoked by the admin", "Error: refresh token revoked, please re-login", "Error: Failed to refresh access token"} {
			if got := ProviderDetect("idle", screen); got == nil || !got.Auth {
				t.Errorf("auth = %#v", got)
			}
		}
		for _, screen := range []string{"Error: failed to refresh the dashboard", "Error: the refresh token cache is stale"} {
			if got := ProviderDetect("idle", screen); got != nil {
				t.Errorf("non-auth refresh matched: %#v", got)
			}
		}
	})
	t.Run("providerDetect: bare unexpected statuses classify inside an error line only", func(t *testing.T) {
		// JS: "providerDetect: bare unexpected statuses classify inside an error line only"
		for _, row := range []struct {
			screen string
			auth   bool
		}{{"Error: unexpected status 503", false}, {"Error: unexpected status 401", true}, {"■ unexpected status 503", false}} {
			if got := ProviderDetect("idle", row.screen); got == nil || got.Auth != row.auth {
				t.Errorf("unexpected status = %#v", got)
			}
		}
		for _, screen := range []string{"unexpected status 503", "• unexpected status 503, kept going locally", `const s = "unexpected status 503";`} {
			if got := ProviderDetect("idle", screen); got != nil {
				t.Errorf("non-error unexpected status matched: %#v", got)
			}
		}
		if got := ProviderDetect("working", "Error: unexpected status 503"); got != nil {
			t.Errorf("working unexpected status = %#v", got)
		}
	})
}

func TestProviderMutationBoundaries(t *testing.T) {
	t.Run("providerDetect: ten-line tail includes row ten and excludes row eleven", func(t *testing.T) {
		inside := "Error: Connection error.\n" + strings.Join([]string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}, "\n")
		outside := inside + "\n10"
		if got := ProviderDetect("idle", inside); got == nil {
			t.Fatal("error on tenth visible line was ignored")
		}
		if got := ProviderDetect("idle", outside); got != nil {
			t.Fatalf("error outside ten-line tail matched: %#v", got)
		}
		withinTail := "head\nError: Connection error.\n" + strings.Join([]string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}, "\n")
		if got := ProviderDetect("idle", withinTail); got == nil {
			t.Fatal("error on the tenth line from the bottom was ignored")
		}
		atFifteenthLine := "Read the file x\n" + strings.Join([]string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13", "14"}, "\n")
		if !PromptSitsInInput(atFifteenthLine) {
			t.Fatal("prompt on the fifteenth visible line was ignored")
		}
		if PromptSitsInInput(atFifteenthLine + "\n15") {
			t.Fatal("prompt on the sixteenth visible line was included")
		}
	})
	t.Run("providerDetect: status, retry, and ASCII-fold boundaries", func(t *testing.T) {
		for _, screen := range []string{"Error: 4011 Unauthorized", "API Error: unexpected ſtatus 503", "API Error: Incorrect API Key", "Error: Connection error. reconnecting"} {
			if got := ProviderDetect("idle", screen); got != nil {
				t.Errorf("false positive for %q: %#v", screen, got)
			}
		}
		for _, screen := range []string{"API Error: unexpected status 503", "API Error: Incorrect API Key"} {
			if got := ProviderDetect("idle", screen); got == nil {
				t.Errorf("ASCII case-insensitive match lost for %q", screen)
			}
		}
	})
}

func TestQuotaCases(t *testing.T) {
	t.Run("quotaDetect: the test-quota.sh hit/miss matrix", func(t *testing.T) {
		// JS: "quotaDetect: the test-quota.sh hit/miss matrix"
		hits := []string{"You have hit your usage limit for grok", "Individual quota reached", "Error: quota exceeded", "RESOURCE_EXHAUSTED: project", "429 Too Many Requests", "rate limit exceeded, retry later", "You've hit your limit for today", "You exceeded your current quota, please check your plan and billing details.", "You have reached your API usage limits: monthly threshold", "You've reached your API usage limits", "INDIVIDUAL QUOTA REACHED"}
		for _, screen := range hits {
			if got := QuotaDetect("idle", screen); len(got) != 2 || got[0] == "" {
				t.Errorf("QuotaDetect(%q) = %#v", screen, got)
			}
		}
		misses := []struct{ state, screen string }{{"idle", "implement a rate limit for the API client"}, {"idle", "return \"rate limit\""}, {"idle", "return \"rate limit exceeded\""}, {"idle", "// 429 Too Many Requests"}, {"idle", "# quota exceeded"}, {"idle", "/* RESOURCE_EXHAUSTED */"}, {"idle", "func Limit() { quota exceeded }"}, {"idle", "function check() { quota exceeded }"}, {"idle", `msg = "quota exceeded"`}, {"idle", `"rate limit exceeded"`}, {"idle", "You've hit your stride"}, {"working", "429 Too Many Requests"}, {"working", "hit your usage limit"}, {"idle", ""}}
		for _, row := range misses {
			if got := QuotaDetect(row.state, row.screen); got != nil {
				t.Errorf("QuotaDetect(%q) = %#v", row.screen, got)
			}
		}
	})
	t.Run("quotaDetect: renewal line kept, secrets redacted (test-quota.sh case)", func(t *testing.T) {
		// JS: "quotaDetect: renewal line kept, secrets redacted (test-quota.sh case)"
		got := QuotaDetect("idle", "Individual quota reached token=sk_live_abcdefghij\nResets at 5:00pm token=sk_live_klmnopqrst")
		want := []string{"Individual quota reached token=[redacted]", "Resets at 5:00pm token=[redacted]"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("QuotaDetect() = %#v, want %#v", got, want)
		}
	})
	t.Run("quotaDetect: the exact test-quota.sh renewal input (ported)", func(t *testing.T) {
		// JS: "quotaDetect: the exact test-quota.sh renewal input (ported)"
		got := QuotaDetect("idle", "Individual quota reached token=sk_live_abcdefghij\nResets at 5:00pm")
		if len(got) != 2 || got[1] != "Resets at 5:00pm" || !strings.Contains(got[0], "[redacted]") {
			t.Fatalf("QuotaDetect() = %#v", got)
		}
	})
	t.Run("quotaDetect: insufficient_quota without a message key stays provider data", func(t *testing.T) {
		screen := `{"error":{"code":"insufficient_quota"}} "rate limit exceeded"`
		got := QuotaDetect("idle", screen)
		if len(got) != 2 || got[0] != screen {
			t.Fatalf("QuotaDetect(%q) = %#v", screen, got)
		}
		if got := RenewalValue("retry after 2 Days"); got != "2 Days" {
			t.Fatalf("RenewalValue(day) = %q", got)
		}
	})
	t.Run("quotaDetect: Bearer, api_key=, sk-proj- and JSON message lines", func(t *testing.T) {
		// JS: "quotaDetect: Bearer, api_key=, sk-proj- and JSON \"message\" lines"
		cases := []struct{ screen, want string }{{"Bearer abc123.~+/ and hit your usage limit", "Bearer [redacted] and hit your usage limit"}, {"api_key=sk-proj-abcdefgh1234 quota exceeded", "api_key=[redacted] quota exceeded"}, {"key sk-proj-abcdefgh1234 rate limit exceeded", "key [redacted] rate limit exceeded"}, {`{"message":"rate limit exceeded"}`, `{"message":"rate limit exceeded"}`}, {`{"error":"quota exceeded"}`, ""}, {`{"error":{"code":"insufficient_quota","message":"rate limit exceeded"}}`, `{"error":{"code":"insufficient_quota","message":"rate limit exceeded"}}`}}
		for _, row := range cases {
			got := QuotaDetect("idle", row.screen)
			if row.want == "" && got != nil || row.want != "" && (len(got) != 2 || got[0] != row.want) {
				t.Errorf("QuotaDetect(%q) = %#v, want %q", row.screen, got, row.want)
			}
		}
	})
	t.Run("quotaDetect: insufficient_quota JSON and day renewal stay provider data", func(t *testing.T) {
		got := QuotaDetect("idle", `{"error":{"code":"insufficient_quota","message":"RATE LIMIT EXCEEDED"}}`+"\nretry after 2 Days")
		want := []string{`{"error":{"code":"insufficient_quota","message":"RATE LIMIT EXCEEDED"}}`, "retry after 2 Days"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("QuotaDetect() = %#v, want %#v", got, want)
		}
	})
	t.Run("quotaDetect: a CRLF screen is normalized before the scan (decision 7)", func(t *testing.T) {
		// JS: "quotaDetect: a CRLF screen is normalized before the scan (decision 7)"
		got := QuotaDetect("idle", "hit your usage limit\r\nResets at 10:00\r\n")
		if !reflect.DeepEqual(got, []string{"hit your usage limit", "Resets at 10:00"}) {
			t.Fatalf("QuotaDetect() = %#v", got)
		}
		if QuotaDetect("idle", "func Limit() { /* rate limit the handler */ }\r\n") != nil || QuotaDetect("working", "429 Too Many Requests\r\n") != nil {
			t.Fatal("code line or working quota matched")
		}
	})
	t.Run("renewalValue: clock time, AM/PM, ISO date, duration, nothing", func(t *testing.T) {
		// JS: "renewalValue: clock time, AM/PM, ISO date, duration, nothing"
		cases := map[string]string{"Resets at 5:00pm": "5:00", "Resets at 5:00a.p.": "5:00", "Resets at 09:15:00": "09:15:00", "Resets at 2:30PM.": "2:30", "Resets at 2:30P.M.": "2:30P.M.", "resets on 2026-09-24 at noon": "2026-09-24", "try again in 5 minutes": "5 minutes", "retry after 2 hours": "2 hours", "quota exceeded, resets at 5:00pm": "5:00", "2026-09-24 14:30": "2026-09-24", "in 5 minutes at 14:30": "5 minutes", "Try again in 5 Minutes": "5 Minutes", "resets in 2 HOURS": "2 HOURS", "Resets in 10 Seconds": "10 Seconds", "retry after 3 Days": "3 Days", "nothing usable here": "", "": ""}
		for input, want := range cases {
			if got := RenewalValue(input); got != want {
				t.Errorf("RenewalValue(%q) = %q, want %q", input, got, want)
			}
		}
	})
	t.Run("redactSecrets: the four sed passes in order", func(t *testing.T) {
		// JS: "redactSecrets: the four sed passes in order"
		for input, want := range map[string]string{"Bearer abc123.~+/": "Bearer [redacted]", "bearer xyz-123": "Bearer [redacted]", "pk-proj-abcdefgh12": "[redacted]", "token=sk_live_abcdefghij": "token=[redacted]", "secret: sk_test_a1b2c3 rest": "secret: [redacted] rest", "sk-ant-123": "sk-ant-123"} {
			if got := strings.ReplaceAll(input, "", ""); got != input {
				t.Fatal(got)
			}
			if got := herdrtext.RedactSecrets(input); got != want {
				t.Errorf("redact = %q, want %q", got, want)
			}
		}
	})
	t.Run("redactSecrets: the test-probe.sh hyphen-key inputs (ported)", func(t *testing.T) {
		// JS: "redactSecrets: the test-probe.sh hyphen-key inputs (ported)"
		for input, want := range map[string]string{"sk-proj-abcDEF123456": "[redacted]", "sk-ant-api01-XYZ12345": "[redacted]", "pk-live12345678": "[redacted]", "key sk_live_987654321": "key [redacted]"} {
			if got := herdrtext.RedactSecrets(input); got != want {
				t.Errorf("redact = %q, want %q", got, want)
			}
		}
	})
	t.Run("quotaLineIsCode / quotaPhraseQuoted: the unit rules", func(t *testing.T) {
		// JS: "quotaLineIsCode / quotaPhraseQuoted: the unit rules"
		for _, line := range []string{"# quota exceeded", "  // 429", "x /* quota exceeded */ y", `return "rate limit"`, "function f() { quota exceeded }", `const q = "x"`} {
			if !QuotaLineIsCode(line) {
				t.Errorf("QuotaLineIsCode(%q) = false", line)
			}
		}
		for _, line := range []string{"Error: quota exceeded", "quota exceeded token=abc"} {
			if QuotaLineIsCode(line) {
				t.Errorf("QuotaLineIsCode(%q) = true", line)
			}
		}
		re := regexp.MustCompile(`rate limit exceeded`)
		for _, line := range []string{`"rate limit exceeded"`, `let s = 'rate limit exceeded'`, "`rate limit exceeded`"} {
			if !QuotaPhraseQuoted(line, re) {
				t.Errorf("quoted phrase not detected: %q", line)
			}
		}
		for _, line := range []string{"Error: rate limit exceeded", `{"message":"rate limit exceeded"}`, "insufficient_quota: rate limit exceeded", "rate_limit_error rate limit exceeded", "no match here"} {
			if QuotaPhraseQuoted(line, re) {
				t.Errorf("provider phrase considered quoted: %q", line)
			}
		}
	})
	t.Run("sanitizeCause: the shared text helper (text.mjs)", func(t *testing.T) {
		// JS: "sanitizeCause: the shared text helper (text.mjs)"
		if got := herdrtext.SanitizeCause("a\nb\tc"); got != "a b c" {
			t.Fatalf("sanitize = %q", got)
		}
		if got := herdrtext.SanitizeCause("x\x1b[31my\r\n z"); got != "x[31my z" {
			t.Fatalf("sanitize controls = %q", got)
		}
		if got := herdrtext.SanitizeCause("a  b   c "); got != "a b c" {
			t.Fatalf("sanitize spaces = %q", got)
		}
		if got := herdrtext.SanitizeCause(""); got != "" {
			t.Fatalf("sanitize empty = %q", got)
		}
	})
}

func TestA14ProviderFundsQuota(t *testing.T) {
	t.Run("a 402 line with insufficient, payment required or funds is a quota stop", func(t *testing.T) {
		hits := []string{
			"opencode API error (402): Insufficient account funds\nRetrying (5/8)",
			"API error 402: payment required",
			"HTTP 402 funds depleted",
			"402 PAYMENT REQUIRED",
			"insufficient account funds",
			"Insufficient balance",
			"insufficient credits",
			"insufficient account credits",
		}
		for _, screen := range hits {
			if got := QuotaDetect("idle", screen); len(got) != 2 || got[0] == "" {
				t.Errorf("QuotaDetect(%q) = %#v, want a quota line", screen, got)
			}
		}
	})
	t.Run("the appliance screen: the 402 line is the cause, the retry line is no renewal", func(t *testing.T) {
		got := QuotaDetect("idle", "opencode API error (402): Insufficient account funds\nRetrying (5/8)")
		want := []string{"opencode API error (402): Insufficient account funds", ""}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("QuotaDetect() = %#v, want %#v", got, want)
		}
		// The shared working-state gate still holds for the new patterns.
		if got := QuotaDetect("working", "opencode API error (402): Insufficient account funds"); got != nil {
			t.Fatalf("working screen matched: %#v", got)
		}
	})
	t.Run("a bare 402, a 4012, or an insufficient without a funds word is no quota", func(t *testing.T) {
		misses := []string{
			"Error: 402",
			"402 too many requests",
			"error 4012 funds",
			"insufficient permissions",
			"insufficient account capacity",
			"insufficient",
		}
		for _, screen := range misses {
			if got := QuotaDetect("idle", screen); got != nil {
				t.Errorf("QuotaDetect(%q) = %#v, want nil", screen, got)
			}
		}
	})
	t.Run("the same 402 line inside source code is no quota (QuotaLineIsCode)", func(t *testing.T) {
		codeLines := []string{
			`return "opencode API error (402): Insufficient account funds"`,
			"# 402 insufficient funds",
			"x = \"402: insufficient funds\"",
			"func handler() { // 402 funds }",
		}
		for _, line := range codeLines {
			if !QuotaLineIsCode(line) {
				t.Fatalf("QuotaLineIsCode(%q) = false", line)
			}
			if got := QuotaDetect("idle", line); got != nil {
				t.Errorf("QuotaDetect(%q) = %#v, want nil", line, got)
			}
		}
	})
}

func TestDialogCases(t *testing.T) {
	t.Run("dialogKind: the question-marker table with the counterexamples", func(t *testing.T) {
		// JS: "dialogKind: the question-marker table with the counterexamples"
		rows := []struct{ kind, screen, want string }{
			{"codex", "Choose an option:\n  1. Use the local cache\n  2. Fetch from remote\n\nEnter to submit answer, esc to cancel", "question"},
			{"codex", "Apply all changes?\n\nEnter to submit all, esc to cancel", "question"},
			{"codex", "SELECT LANE\n\nENTER TO SUBMIT ANSWER, ESC TO CANCEL", "question"},
			{"codex", "Allow command? git push\n\n❯ 1. Yes, proceed\n  2. No\n\nPress enter to confirm or esc to cancel", "approval"},
			{"claude", "Select an option (1-2):\n❯ 1. Use the local cache\n  2. Fetch remote\n\n↑↓ to navigate · Enter to select", "question"},
			{"claude", "Answer 1 of 2\n❯ 1. Yes\n  2. No\n\nEnter to select · Submit answers", "question"},
			{"claude", "Do you want to proceed?\n❯ 1. Yes\n  2. No\n\n↑↓ to navigate · Enter to select", "approval"},
			{"claude", "Do you want to proceed?\n❯ 1. Yes\n  2. No", "approval"}, {"claude", "Type to navigate the list", "approval"}, {"claude", "Enter to select a value", "approval"},
			{"opencode", "  1. Continue the plan\n  2. Stop\n\nEnter submit · Esc dismiss", "question"}, {"opencode", "  1. A\n  2. B\n\nEnter toggle · Esc dismiss", "question"},
			{"opencode", "Permission required: read /etc/hosts\n\nEnter submit · Esc dismiss", "approval"}, {"opencode", "Bash command pending\n\nEsc dismiss", "approval"},
			{"grok", "Enter to submit answer, esc to cancel", "approval"}, {"cursor", "Enter to select", "approval"}, {"agy", "Esc dismiss · Enter submit", "approval"}, {"pi", "Enter to submit answer, esc to cancel", "approval"}, {"gemini", "Enter to submit answer", "approval"}, {"unknown-kind", "anything at all", "approval"}, {"", "anything at all", "approval"}, {"codex", "", "approval"},
		}
		for _, row := range rows {
			if got := DialogKind(row.kind, row.screen); got != row.want {
				t.Errorf("DialogKind(%s, %q) = %s", row.kind, row.screen, got)
			}
		}
	})
	t.Run("questionText: last 20 non-empty lines, trimmed, redacted, 1200 cap", func(t *testing.T) {
		// JS: "questionText: last 20 non-empty lines, trimmed, redacted, 1200 cap"
		many := make([]string, 25)
		for i := range many {
			many[i] = "line " + strconv.Itoa(i+1)
		}
		got := QuestionText(strings.Join(many, "\n") + "\n")
		parts := strings.Split(got, "\n")
		if len(parts) != 20 || parts[0] != "line 6" {
			t.Fatalf("last lines = %q", got)
		}
		if got := QuestionText("a   \n\nb\t\nc\n"); got != "a\nb\nc" {
			t.Fatalf("trim lines = %q", got)
		}
		if got := QuestionText("token=abc123\nkey sk_live_abcdefghijklmnop here\nBearer zz9.999 and more\n"); got != "token=[redacted]\nkey [redacted] here\nBearer [redacted] and more" {
			t.Fatalf("redaction = %q", got)
		}
		long := strings.Repeat("x", 500)
		capped := QuestionText(long + "\n" + long + "\n" + long + "\n")
		if len(capped) != 1200 || capped != long+"\n"+long+"\n"+strings.Repeat("x", 198) {
			t.Fatalf("capped text length=%d", len(capped))
		}
		if QuestionText("") != "" || QuestionText("\n\n   \n\t\n") != "" {
			t.Fatal("empty text was not empty")
		}
		splitEmoji := QuestionText(strings.Repeat("x", 1199) + "😀")
		if got := []byte(splitEmoji[1199:]); !reflect.DeepEqual(got, []byte{0xed, 0xa0, 0xbd}) {
			t.Fatalf("UTF-16 cut lost lone high surrogate: % x", got)
		}
		if QuestionText(strings.Repeat("x", 1198)+"á😀") != strings.Repeat("x", 1198)+"á"+string([]byte{0xed, 0xa0, 0xbd}) {
			t.Fatal("accent + emoji UTF-16 boundary changed")
		}
	})
}

func TestProviderDifferential(t *testing.T) {
	data, err := os.ReadFile("testdata/provider.json")
	if err != nil {
		t.Fatal(err)
	}
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for i, item := range raw {
		var header struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(item["kind"], &header.Kind); err != nil {
			t.Fatalf("provider.json row %d kind decode: %v", i, err)
		}
		if header.Kind == "" {
			t.Fatalf("provider.json row %d has empty kind", i)
		}
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			switch header.Kind {
			case "provider", "quota":
				var r struct {
					State    string          `json:"state"`
					Screen   string          `json:"screen"`
					Expected json.RawMessage `json:"expected"`
				}
				if err := json.Unmarshal(itemBytes(t, item), &r); err != nil {
					t.Fatal(err)
				}
				if header.Kind == "provider" {
					got := ProviderDetect(r.State, r.Screen)
					matches, err := equalJSON(got, r.Expected)
					if err != nil {
						t.Fatal(err)
					}
					if !matches {
						t.Fatalf("ProviderDetect() = %#v, JS %s", got, r.Expected)
					}
				} else {
					got := QuotaDetect(r.State, r.Screen)
					matches, err := equalJSON(got, r.Expected)
					if err != nil {
						t.Fatal(err)
					}
					if !matches {
						t.Fatalf("QuotaDetect() = %#v, JS %s", got, r.Expected)
					}
				}
			case "quota-code":
				var r struct {
					Line     string `json:"line"`
					Expected bool   `json:"expected"`
				}
				if err := json.Unmarshal(itemBytes(t, item), &r); err != nil {
					t.Fatal(err)
				}
				if got := QuotaLineIsCode(r.Line); got != r.Expected {
					t.Fatalf("QuotaLineIsCode(%q) = %t, JS %t", r.Line, got, r.Expected)
				}
			case "renewal":
				var r struct{ Line, Expected string }
				if err := json.Unmarshal(itemBytes(t, item), &r); err != nil {
					t.Fatal(err)
				}
				if got := RenewalValue(r.Line); got != r.Expected {
					t.Fatalf("RenewalValue(%q) = %q, JS %q", r.Line, got, r.Expected)
				}
			case "quoted":
				var r struct {
					Line     string `json:"line"`
					Expected bool   `json:"expected"`
				}
				if err := json.Unmarshal(itemBytes(t, item), &r); err != nil {
					t.Fatal(err)
				}
				if got := QuotaPhraseQuoted(r.Line, regexp.MustCompile(`rate limit exceeded`)); got != r.Expected {
					t.Fatalf("QuotaPhraseQuoted() = %t, JS %t", got, r.Expected)
				}
			case "dialog":
				var r struct {
					Provider string `json:"provider"`
					Screen   string `json:"screen"`
					Expected string `json:"expected"`
				}
				if err := json.Unmarshal(itemBytes(t, item), &r); err != nil {
					t.Fatal(err)
				}
				if got := DialogKind(r.Provider, r.Screen); got != r.Expected {
					t.Fatalf("DialogKind() = %q, JS %q", got, r.Expected)
				}
			case "question-text":
				var r struct {
					Screen   string `json:"screen"`
					Expected string `json:"expected"`
				}
				if err := json.Unmarshal(itemBytes(t, item), &r); err != nil {
					t.Fatal(err)
				}
				if got := herdrtext.ToWellFormedUTF8(QuestionText(r.Screen)); got != r.Expected {
					t.Fatalf("QuestionText() = %q, JS %q", got, r.Expected)
				}
			case "last-lines", "prompt":
				var r struct {
					Screen   string          `json:"screen"`
					N        int             `json:"n"`
					Expected json.RawMessage `json:"expected"`
				}
				if err := json.Unmarshal(itemBytes(t, item), &r); err != nil {
					t.Fatal(err)
				}
				if header.Kind == "last-lines" {
					var want []string
					if err := json.Unmarshal(r.Expected, &want); err != nil {
						t.Fatal(err)
					}
					if got := LastNonEmptyLines(r.Screen, r.N); !reflect.DeepEqual(got, want) {
						t.Fatalf("LastNonEmptyLines() = %#v, JS %#v", got, want)
					}
				} else {
					var want bool
					if err := json.Unmarshal(r.Expected, &want); err != nil {
						t.Fatal(err)
					}
					if got := PromptSitsInInput(r.Screen); got != want {
						t.Fatalf("PromptSitsInInput() = %t, JS %t", got, want)
					}
				}
			case "marker-seq":
				var r struct {
					Marker   string `json:"marker"`
					Expected string `json:"expected"`
				}
				if err := json.Unmarshal(itemBytes(t, item), &r); err != nil {
					t.Fatal(err)
				}
				if got := MarkerSeq(r.Marker); got != r.Expected {
					t.Fatalf("MarkerSeq() = %q, JS %q", got, r.Expected)
				}
			case "marker-changed":
				var r struct {
					Marker   string `json:"marker"`
					Current  string `json:"current"`
					Expected bool   `json:"expected"`
				}
				if err := json.Unmarshal(itemBytes(t, item), &r); err != nil {
					t.Fatal(err)
				}
				if got := MarkerSeqChanged(r.Marker, r.Current); got != r.Expected {
					t.Fatalf("MarkerSeqChanged() = %t, JS %t", got, r.Expected)
				}
			default:
				t.Fatalf("provider.json row %d has unknown kind %q", i, header.Kind)
			}
		})
	}
}

func itemBytes(t *testing.T, item map[string]json.RawMessage) []byte {
	t.Helper()
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRetryLineCases(t *testing.T) {
	t.Run("retryLine: the most recent retry line of a working agent, sanitized", func(t *testing.T) {
		cases := []struct{ screen, want string }{
			{"Retrying (5/8) in 4s…\n", "Retrying (5/8) in 4s"},
			{"┃ Retrying (5/8) in 4s…\n", "Retrying (5/8) in 4s"},
			{"busy\n┃ Reconnecting…\n", "Reconnecting"},
		}
		for _, row := range cases {
			if got := RetryLine("working", row.screen); got != row.want {
				t.Errorf("RetryLine(working, %q) = %q, want %q", row.screen, got, row.want)
			}
		}
	})
	t.Run("retryLine: the bottom-up scan returns the most recent retry line", func(t *testing.T) {
		if got := RetryLine("working", "Reconnecting…\nRetrying (5/8) in 4s…\n"); got != "Retrying (5/8) in 4s" {
			t.Errorf("RetryLine() = %q, want the bottom line", got)
		}
	})
	t.Run("retryLine: a box decoration line does not consume a window slot", func(t *testing.T) {
		// Eleven raw non-empty lines; the `┃` decoration counts for nothing
		// without its prefix, so the retry line stays inside the ten.
		screen := "Retrying (5/8) in 4s…\n" +
			"b1\nb2\nb3\nb4\nb5\nb6\nb7\nb8\n┃\nb9\n"
		if got := RetryLine("working", screen); got != "Retrying (5/8) in 4s" {
			t.Errorf("RetryLine() = %q, want the retry line despite the decoration line", got)
		}
	})
	t.Run("retryLine: a retry line above the bottom ten non-empty lines is not the line", func(t *testing.T) {
		screen := "Retrying (5/8) in 4s…\n" +
			"a1\na2\na3\na4\na5\na6\na7\na8\na9\na10\n"
		if got := RetryLine("working", screen); got != "" {
			t.Errorf("RetryLine() = %q, want empty (eleventh line from the bottom)", got)
		}
	})
	t.Run("retryLine: no retry line, empty screen, or a non-working state returns empty", func(t *testing.T) {
		cases := []struct{ state, screen string }{
			{"working", "Reading the file\nRunning tests\n"},
			{"working", ""},
			{"working", "\n  \n\t\n"},
			{"idle", "Retrying (5/8) in 4s…\n"},
			{"blocked", "Retrying (5/8) in 4s…\n"},
		}
		for _, row := range cases {
			if got := RetryLine(row.state, row.screen); got != "" {
				t.Errorf("RetryLine(%q, %q) = %q, want empty", row.state, row.screen, got)
			}
		}
	})
	t.Run("retryLine: a secret on the line is redacted before it is returned", func(t *testing.T) {
		if got := RetryLine("working", "Retrying (5/8) with sk_live_abcdefgh1234\n"); got != "Retrying (5/8) with [redacted]" {
			t.Errorf("RetryLine() = %q, want the redacted line", got)
		}
	})
}

func TestRetryLineModelPhraseNotProviderRetry(t *testing.T) {
	t.Run("retryLine: the four provider retry shapes count", func(t *testing.T) {
		cases := []struct{ screen, want string }{
			{"Retrying (5/8) in 4s…\n", "Retrying (5/8) in 4s"},
			{"API Error (Request timed out) · Retrying in 5 seconds…\n", "API Error (Request timed out) Retrying in 5 seconds"},
			{"Reconnecting…\n", "Reconnecting"},
			{"Will retry in 30 seconds\n", "Will retry in 30 seconds"},
		}
		for _, row := range cases {
			if got := RetryLine("working", row.screen); got != row.want {
				t.Errorf("RetryLine(working, %q) = %q, want %q", row.screen, got, row.want)
			}
		}
	})
	t.Run("retryLine: the model's own retry wording is not a provider retry", func(t *testing.T) {
		for _, screen := range []string{
			"Retrying with the correct text.\n",
			"I'll try again: retrying the edit\n",
			// Has a digit but does not start with a retry word and carries no
			// `· Retrying`/`- Retrying` tail: isolates the line-start rule.
			"Now retrying the build, attempt 2\n",
		} {
			if got := RetryLine("working", screen); got != "" {
				t.Errorf("RetryLine(working, %q) = %q, want empty (model wording)", screen, got)
			}
		}
	})
	t.Run("retryLine: a boxed retry line with a counter still counts", func(t *testing.T) {
		if got := RetryLine("working", "┃ Retrying (2/5) in 3s\n"); got != "Retrying (2/5) in 3s" {
			t.Errorf("RetryLine() = %q, want the boxed retry line", got)
		}
	})
	t.Run("providerDetect: the model's retry phrase is skipped as today", func(t *testing.T) {
		if got := ProviderDetect("idle", "Retrying with the correct text.\n"); got != nil {
			t.Errorf("ProviderDetect() = %+v, want nil (same as today)", got)
		}
	})
}

func TestRetryLineCodexMidLineCounter(t *testing.T) {
	t.Run("retryLine: codex's mid-line retrying with an n/m counter counts, with or without a glyph", func(t *testing.T) {
		line := "stream disconnected - retrying sampling request (1/5 in 211ms)..."
		want := "stream disconnected - retrying sampling request (1/5 in 211ms)..."
		for _, screen := range []string{
			line + "\n",
			"■ " + line + "\n",
			"⚠ " + line + "\n",
		} {
			if got := RetryLine("working", screen); got != want {
				t.Errorf("RetryLine(working, %q) = %q, want %q", screen, got, want)
			}
		}
	})
	t.Run("retryLine: Reconnecting lines count", func(t *testing.T) {
		cases := []struct{ screen, want string }{
			{"Reconnecting... 2/5\n", "Reconnecting... 2/5"},
			{"Reconnecting... waiting for network\n", "Reconnecting... waiting for network"},
		}
		for _, row := range cases {
			if got := RetryLine("working", row.screen); got != row.want {
				t.Errorf("RetryLine(working, %q) = %q, want %q", row.screen, got, row.want)
			}
		}
	})
	t.Run("retryLine: the negatives still do not count", func(t *testing.T) {
		for _, screen := range []string{
			"Retrying with the correct text.\n",
			"I'll try again: retrying the edit\n",
			"Now retrying the build, attempt 2\n",
		} {
			if got := RetryLine("working", screen); got != "" {
				t.Errorf("RetryLine(working, %q) = %q, want empty", screen, got)
			}
		}
	})
}

func TestRetryLineModelPhrasesWithoutParenCounter(t *testing.T) {
	t.Run("retryLine: model wording with an n/m figure but no (n/m) counter does not count", func(t *testing.T) {
		for _, screen := range []string{
			"retrying the 1/2 migration\n",
			"Now retrying the 1/2 migration\n",
			"I'll try again: retrying the 1/2 migration\n",
			"I hit an error - retrying the 1/2 migration\n",
			"Error - retrying the 1/2 migration\n",
			"Finished retrying the 2026/10 cutover\n",
		} {
			if got := RetryLine("working", screen); got != "" {
				t.Errorf("RetryLine(working, %q) = %q, want empty (model wording)", screen, got)
			}
		}
	})
	t.Run("retryLine: a line-start form needs the (n/m) counter or an in-N deadline, not any digit", func(t *testing.T) {
		for _, screen := range []string{
			"Retrying 2 files\n",
			// A digit without the (n/m) counter or the in-N deadline: the
			// line-start form the brief no longer counts on any digit alone.
			"Will retry, 3 files left\n",
		} {
			if got := RetryLine("working", screen); got != "" {
				t.Errorf("RetryLine(working, %q) = %q, want empty", screen, got)
			}
		}
	})
}

func equalJSON(got any, want json.RawMessage) (bool, error) {
	b, err := json.Marshal(got)
	if err != nil {
		return false, err
	}
	return string(b) == string(want), nil
}

func TestRetryLineErrorTailNeedsCounterOrDeadline(t *testing.T) {
	cases := []struct {
		screen string
		counts bool
	}{
		{"API Error (Request timed out) · Retrying in 5 seconds…\n", true},
		{"API Error (Request timed out) - Retrying in 5 seconds\n", true},
		{"stream disconnected - retrying sampling request (1/5 in 211ms)...\n", true},
		{"Error - retrying the 1/2 migration\n", false},
		{"I hit an error - retrying the 1/2 migration\n", false},
		{"Error · Retrying the migration\n", false},
	}
	for _, c := range cases {
		got := RetryLine("working", c.screen)
		if (got != "") != c.counts {
			t.Errorf("RetryLine(%q) = %q, want counts=%v", c.screen, got, c.counts)
		}
	}
}

func TestProviderDetectResponseStoppedArriving(t *testing.T) {
	const phrase = "The response stopped arriving"
	positive := []struct {
		name, state, screen, cause string
	}{
		{"settled api error with neutral tail", "idle", "API Error: " + phrase + ". Please try again.\n❯\n", "API Error: " + phrase + ". Please try again."},
		{"settled error glyph", "idle", "■ Error: " + phrase + "\n", "Error: " + phrase},
		{"Claude screen", "idle", "● API Error: " + phrase + ". The response above may be incomplete.\n❯\n", "API Error: " + phrase + ". The response above may be incomplete."},
		{"redacted cause", "idle", "API Error: " + phrase + " token=sk_live_fakekey000\n", "API Error: " + phrase + " token=[redacted]"},
	}
	for _, c := range positive {
		t.Run(c.name, func(t *testing.T) {
			got := ProviderDetect(c.state, c.screen)
			if got == nil || got.Status != "provider-error" || got.Auth || got.Cause != c.cause {
				t.Errorf("ProviderDetect(%q, %q) = %#v, want provider-error cause %q", c.state, c.screen, got, c.cause)
			}
		})
	}

	if got := ProviderDetect("idle", "Error: "+phrase+"; 529 at capacity"); got == nil || got.Status != "capacity" {
		t.Errorf("capacity precedence = %#v", got)
	}
	if got := ProviderDetect("idle", "Error: "+phrase+" 401"); got == nil || got.Status != "provider-error" || !got.Auth {
		t.Errorf("auth precedence = %#v", got)
	}

	negative := []struct {
		name, state, screen string
	}{
		{"working", "working", "API Error: " + phrase + ". Please try again.\n"},
		{"active retry on the same line", "idle", "API Error: " + phrase + ". Retrying in 5 seconds\n"},
		{"quoted code", "idle", `const msg = "API Error: The response stopped arriving"`},
		{"error diagnostic quoting the message", "idle", `Error: expected "The response stopped arriving" in the fixture`},
		{"output bullet quoting the message", "idle", `● Error: expected "The response stopped arriving" in the fixture`},
		{"phrase inside a longer word", "idle", "Error: " + phrase + "ly"},
		{"model description", "idle", "I saw " + phrase + " in the log\n"},
	}
	for _, c := range negative {
		t.Run(c.name, func(t *testing.T) {
			if got := ProviderDetect(c.state, c.screen); got != nil {
				t.Errorf("ProviderDetect(%q, %q) = %#v, want nil", c.state, c.screen, got)
			}
		})
	}
}
