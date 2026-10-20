package doctor

// DJA-393 L1: the doctor reports the declared caller-tab columns. The
// `config: split_max_panes=… split_min_pane=…` line gains the pane_columns
// and pane_overflow suffix (with the cap shown as `none` when the declared
// overflow leaves the caller tab uncapped), and three new warnings appear
// only when pane_columns is set. Without the key the line is identical to
// today's — the frozen doctor goldens pin that.

import (
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func paneColumnsDoctor(t *testing.T, entries map[string]string) string {
	t.Helper()
	base := []fakecli.Rule{
		{Argv: []string{"--version"}, Stdout: "herdr 1.2.3\n"},
		{Argv: []string{"status", "server"}, Stdout: "server 1.2.3\n"},
	}
	env, root := paneContextFixture(t, base)
	ctx := core.Config{Entries: map[string]core.ConfigEntry{}}
	for key, value := range entries {
		ctx.Entries[key] = core.ConfigEntry{Value: value, Source: "project"}
	}
	var out strings.Builder
	DoctorCheck(&ctx, env, root, &out)
	return out.String()
}

func TestDoctorPaneColumnsLineAndWarnings(t *testing.T) {
	t.Run(`the config line gains the declared columns and the uncapped cap reads none`, func(t *testing.T) {
		got := paneColumnsDoctor(t, map[string]string{"pane_columns": "1,2,3"})
		want := "ok     config: split_max_panes=none split_min_pane=0.18 pane_columns=1,2,3 pane_overflow=last-column"
		if !strings.Contains(got, want) {
			t.Fatalf("the uncapped line is missing:\n%s", got)
		}
	})
	t.Run(`tab overflow: the cap is the declared total`, func(t *testing.T) {
		got := paneColumnsDoctor(t, map[string]string{"pane_columns": "1,2,3", "pane_overflow": "tab"})
		want := "ok     config: split_max_panes=6 split_min_pane=0.18 pane_columns=1,2,3 pane_overflow=tab"
		if !strings.Contains(got, want) {
			t.Fatalf("the tab-cap line is missing:\n%s", got)
		}
	})
	t.Run(`an invalid pane_overflow shows raw in the line and is read as last-column`, func(t *testing.T) {
		got := paneColumnsDoctor(t, map[string]string{"pane_columns": "1,2", "pane_overflow": "side"})
		want := "ok     config: split_max_panes=none split_min_pane=0.18 pane_columns=1,2 pane_overflow=side"
		if !strings.Contains(got, want) {
			t.Fatalf("the raw pane_overflow is hidden:\n%s", got)
		}
	})
	t.Run(`an explicit split_max_panes wins in the line too`, func(t *testing.T) {
		got := paneColumnsDoctor(t, map[string]string{"pane_columns": "1,2,3", "split_max_panes": "9"})
		want := "ok     config: split_max_panes=9 split_min_pane=0.18 pane_columns=1,2,3 pane_overflow=last-column"
		if !strings.Contains(got, want) {
			t.Fatalf("the explicit-cap line is missing:\n%s", got)
		}
	})
	t.Run(`no key: the line is today's and none of the three warnings appears`, func(t *testing.T) {
		got := paneColumnsDoctor(t, nil)
		if !strings.Contains(got, "ok     config: split_max_panes=4 split_min_pane=0.18\n") {
			t.Fatalf("today's line is missing:\n%s", got)
		}
		for _, forbidden := range []string{"pane_columns", "has no effect with layout=tab", "workers are placed by split only"} {
			if strings.Contains(got, forbidden) {
				t.Fatalf("a pane_columns mention leaked without the key:\n%s", got)
			}
		}
	})
	t.Run(`an invalid value warns with the exact text and is read as the default grid`, func(t *testing.T) {
		got := paneColumnsDoctor(t, map[string]string{"pane_columns": "1,,2"})
		want := "warn   config: pane_columns='1,,2' must be auto or 1 to 6 comma-separated row counts from 1 to 9, like 1,2,3 (using the default grid)"
		if !strings.Contains(got, want) {
			t.Fatalf("the invalid-value warning is missing:\n%s", got)
		}
		// Treated as empty: today's line, no suffix, no other warnings.
		if !strings.Contains(got, "ok     config: split_max_panes=4 split_min_pane=0.18\n") {
			t.Fatalf("the invalid value must fall back to today's line:\n%s", got)
		}
		if strings.Contains(got, "has no effect with layout=tab") {
			t.Fatalf("the invalid value must not trigger the layout warning:\n%s", got)
		}
	})
	t.Run(`layout=tab: the no-effect warning appears`, func(t *testing.T) {
		got := paneColumnsDoctor(t, map[string]string{"pane_columns": "1,2,3", "layout": "tab"})
		want := "warn   config: pane_columns has no effect with layout=tab"
		if !strings.Contains(got, want) {
			t.Fatalf("the layout=tab warning is missing:\n%s", got)
		}
	})
	t.Run(`regrid=off: the placement warning appears`, func(t *testing.T) {
		got := paneColumnsDoctor(t, map[string]string{"pane_columns": "1,2,3", "regrid": "off"})
		want := "warn   config: pane_columns is set but regrid=off; workers are placed by split only, not in the declared columns"
		if !strings.Contains(got, want) {
			t.Fatalf("the regrid=off warning is missing:\n%s", got)
		}
	})
	t.Run(`layout=tab and regrid=off together: both warnings`, func(t *testing.T) {
		got := paneColumnsDoctor(t, map[string]string{"pane_columns": "1,2,3", "layout": "tab", "regrid": "off"})
		for _, want := range []string{
			"warn   config: pane_columns has no effect with layout=tab",
			"warn   config: pane_columns is set but regrid=off; workers are placed by split only, not in the declared columns",
		} {
			if !strings.Contains(got, want) {
				t.Fatalf("warning missing:\n%s", got)
			}
		}
	})
}

func TestDoctorAutoPaneColumns(t *testing.T) {
	for _, value := range []string{"", "1", "3", "9", "0", "10", "x"} {
		t.Run(value, func(t *testing.T) {
			keys := map[string]string{"pane_columns": "auto", "pane_overflow": "tab"}
			if value != "" {
				keys["pane_column_rows"] = value
			}
			out := paneColumnsDoctor(t, keys)
			invalid := value == "0" || value == "10" || value == "x"
			rows := value
			if rows == "" || invalid {
				rows = "3"
			}
			if !strings.Contains(out, "pane_columns=auto pane_column_rows="+rows+" pane_overflow=tab") || strings.Contains(out, "pane_columns='auto' must") {
				t.Fatalf("value=%q output=%s", value, out)
			}
			if strings.Contains(out, "pane_column_rows='") != invalid {
				t.Fatalf("value=%q invalid=%v output=%s", value, invalid, out)
			}
		})
	}
}
