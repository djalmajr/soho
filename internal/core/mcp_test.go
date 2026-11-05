package core

import (
	"reflect"
	"strings"
	"testing"
)

// DJA-693: the mcp value is inherit, none, or a comma-separated list of
// server names. Each name is trimmed of surrounding spaces and must match
// ^[A-Za-z0-9_-]{1,64}$; none and inherit are the whole value only; an empty
// part is a missing name; duplicates count once.

func TestMcpValueGrammar(t *testing.T) {
	valid := []string{"inherit", "none", "a", "a,b", "a, b", "a,b,c", "a,a", "open-pencil", "playwright,chrome-devtools", "a-1_x", strings.Repeat("a", 64)}
	for _, value := range valid {
		if detail := McpValueError(value); detail != "" {
			t.Fatalf("value %q reported invalid: %s", value, detail)
		}
	}
	invalid := map[string]string{
		"":                             "empty",
		",":                            "empty",
		"a,":                           "empty",
		"a,,b":                         "empty",
		" ":                            "empty",
		"a b":                          "name",
		"none,x":                       "none",
		"x,none":                       "none",
		"a.b":                          "name",
		"a b,c d":                      "name",
		"a," + strings.Repeat("b", 65): "name",
	}
	for value, want := range invalid {
		detail := McpValueError(value)
		switch want {
		case "empty":
			if detail != "empty name in the list" {
				t.Fatalf("value %q: detail=%q, want the empty-name detail", value, detail)
			}
		case "none":
			if detail == "" || !strings.Contains(detail, "whole value") {
				t.Fatalf("value %q: detail=%q, want the whole-value detail", value, detail)
			}
		case "name":
			if detail == "" {
				t.Fatalf("value %q accepted, want invalid", value)
			}
			if !strings.Contains(detail, "server name") {
				t.Fatalf("value %q: detail=%q, want the server-name detail", value, detail)
			}
		}
	}
	// The detail cites the offending name, for the spawn's usage message.
	if detail := McpValueError("a bad b"); !strings.Contains(detail, "a bad b") {
		t.Fatalf("detail does not cite the filtered value: %q", detail)
	}
	if detail := McpValueError("x,none"); !strings.Contains(detail, "none") {
		t.Fatalf("detail does not cite the list name: %q", detail)
	}
}

func TestMcpNames(t *testing.T) {
	cases := map[string][]string{
		"a, b ,a":    {"a", "b"},
		"alpha,beta": {"alpha", "beta"},
		"a":          {"a"},
		"inherit":    nil,
		"none":       nil,
		"a b":        nil,
		",":          nil,
	}
	for value, want := range cases {
		if got := McpNames(value); !reflect.DeepEqual(got, want) {
			t.Fatalf("McpNames(%q)=%#v, want %#v", value, got, want)
		}
	}
}

// The value that travels on (roster column, reuse comparison, reopen flag) is
// one canonical form: a tab or a newline around a name would split the
// tab-separated roster row, and "a, b" and "a,b" are the same choice.
func TestMcpCanonical(t *testing.T) {
	cases := map[string]string{
		"inherit":   "inherit",
		"none":      "none",
		"alpha":     "alpha",
		"a, b ,a":   "a,b",
		"a,\tb":     "a,b",
		"a,\nb":     "a,b",
		" a ,\r\nb": "a,b",
	}
	for value, want := range cases {
		if got := McpCanonical(value); got != want {
			t.Fatalf("McpCanonical(%q)=%q, want %q", value, got, want)
		}
	}
}

func TestMcpValueErrorFiltersTheName(t *testing.T) {
	detail := McpValueError("a\x1b[31mb")
	if detail == "" {
		t.Fatal("a name with a control character must be refused")
	}
	if strings.ContainsRune(detail, 0x1b) {
		t.Fatalf("the detail carries the control character: %q", detail)
	}
}
