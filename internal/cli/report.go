package cli

// Read-only projection of a worker report: the same item rules, test-layer
// parse and warning builders wait uses, without writing a wait marker or any
// other file.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/reportscan"
	waitpkg "github.com/djalmajr/soho/internal/wait"
)

const reportUsage = "usage: report <agent> | --file <path> [--json]"

type reportSelection struct {
	agent, file string
	json        bool
}

func parseReportArgs(argv []string) (reportSelection, bool) {
	var out reportSelection
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch {
		case arg == "--json":
			out.json = true
		case arg == "--file":
			if out.file != "" || i+1 >= len(argv) {
				return reportSelection{}, false
			}
			i++
			out.file = argv[i]
		case strings.HasPrefix(arg, "-"):
			return reportSelection{}, false
		case out.agent != "":
			return reportSelection{}, false
		default:
			out.agent = arg
		}
	}
	if (out.agent == "") == (out.file == "") {
		return reportSelection{}, false
	}
	return out, true
}

type reportCountsJSON struct {
	Done    int `json:"done"`
	Partial int `json:"partial"`
	Env     int `json:"env"`
	Skipped int `json:"skipped"`
}

type reportItemJSON struct {
	Line int    `json:"line"`
	Mark string `json:"mark"`
	Text string `json:"text"`
}

type reportLayerJSON struct {
	Name   string `json:"name"`
	Result string `json:"result"`
	Reason string `json:"reason"`
}

type reportLayersJSON struct {
	Found    bool              `json:"found"`
	Pass     int               `json:"pass"`
	Fail     int               `json:"fail"`
	NotRun   int               `json:"not_run"`
	Blocked  int               `json:"blocked"`
	NA       int               `json:"na"`
	Unmarked int               `json:"unmarked"`
	Layers   []reportLayerJSON `json:"layers"`
}

type reportResponsesJSON struct {
	Found      bool  `json:"found"`
	Fixed      []int `json:"fixed"`
	Declined   []int `json:"declined"`
	Unmarked   int   `json:"unmarked"`
	Missing    []int `json:"missing"`
	Unknown    []int `json:"unknown"`
	Unnumbered int   `json:"unnumbered"`
}

type reportFindingMarksJSON struct {
	Brief    int            `json:"brief"`
	Impl     int            `json:"impl"`
	Unknown  int            `json:"unknown"`
	Classes  map[string]int `json:"classes"`
	Unmarked int            `json:"unmarked"`
	Numbers  []int          `json:"numbers"`
}

type reportWarningJSON struct {
	Kind          string `json:"kind"`
	Message       string `json:"message"`
	AlreadyWarned bool   `json:"already_warned"`
}

type reportJSON struct {
	Schema          int                            `json:"schema"`
	Agent           string                         `json:"agent"`
	ReportPath      string                         `json:"report_path"`
	Role            string                         `json:"role"`
	Kind            string                         `json:"kind"`
	Model           string                         `json:"model"`
	Counts          reportCountsJSON               `json:"counts"`
	Items           []reportItemJSON               `json:"items"`
	TestLayers      reportLayersJSON               `json:"test_layers"`
	Validation      any                            `json:"validation"`
	ReviewHeader    *reportscan.ReviewHeaderResult `json:"review_header"`
	ReviewResponses reportResponsesJSON            `json:"review_responses"`
	FindingMarks    *reportFindingMarksJSON        `json:"finding_marks"`
	BlockedByScope  []string                       `json:"blocked_by_scope"`
	Warnings        []reportWarningJSON            `json:"warnings"`
}

func cmdReport(args []string, env platform.Env, cwd string) int {
	selection, ok := parseReportArgs(args)
	if !ok {
		platform.Die(reportUsage, 2)
	}
	readEnv := env.Clone()
	readEnv["SOHO_NOWRITE"] = "1"
	agent, report, stateDir := "", "", ""
	role, kind, model := "", "", ""
	editRole, reviewRole := false, false
	if selection.file != "" {
		report = selection.file
	} else {
		agent = selection.agent
		ctx := core.LoadConfigFor(readEnv, cwd, core.UserSelection{})
		stateDir = core.StateDirPath(&ctx, readEnv, cwd)
		report = core.LastReport(stateDir, agent)
		fields := strings.Split(core.RosterLine(stateDir, agent), "\t")
		role, kind, model = rosterField(fields, 3), rosterField(fields, 2), rosterField(fields, 8)
		editRole = core.RoleIsEditReadOnly(role, readEnv, cwd)
		reviewRole = core.IsReviewRole(role)
	}
	if report == "" {
		platform.Die("report: no report for "+agent+selection.file, 1)
	}
	text, err := platform.ReadTextFile(report)
	if err != nil {
		platform.Die("report: no report for "+agent+selection.file, 1)
	}
	analysis := waitpkg.AnalyzeReport(waitpkg.ReportWarningContext{
		Agent: agent, Report: report, Text: text, StateDir: stateDir,
		Role: role, Cwd: cwd, Env: readEnv, EditRole: editRole, ReviewRole: reviewRole,
	})
	items := reportscan.Items(text)
	payload := reportJSON{
		Schema:     1,
		Agent:      agent,
		ReportPath: report,
		Role:       role,
		Kind:       kind,
		Model:      model,
		Counts: reportCountsJSON{
			Done:    countMarks(items, "done"),
			Partial: analysis.Partial,
			Env:     analysis.EnvCount,
			Skipped: countMarks(items, "skipped"),
		},
		Items:        make([]reportItemJSON, 0, len(items)),
		TestLayers:   projectLayers(analysis),
		ReviewHeader: analysis.Header,
		ReviewResponses: reportResponsesJSON{
			Found: analysis.Responses.Found,
			Fixed: intsOrEmpty(analysis.Responses.Fixed), Declined: intsOrEmpty(analysis.Responses.Declined),
			Unmarked: analysis.Responses.Unmarked, Missing: intsOrEmpty(analysis.Missing),
			Unknown: intsOrEmpty(analysis.Unknown), Unnumbered: analysis.Unnumbered,
		},
		FindingMarks:   projectFindingMarks(analysis),
		BlockedByScope: stringsOrEmpty(analysis.BlockedByScope),
		Warnings:       make([]reportWarningJSON, 0, len(analysis.Warnings)),
	}
	for _, item := range items {
		payload.Items = append(payload.Items, reportItemJSON{Line: item.Line, Mark: item.Mark, Text: item.Text})
	}
	if analysis.HasValidation {
		payload.Validation = map[string]string{"result": analysis.Validation}
	}
	for _, warning := range analysis.Warnings {
		payload.Warnings = append(payload.Warnings, reportWarningJSON{
			Kind: warning.Kind, Message: warning.Message,
			AlreadyWarned: waitpkg.AlreadyWarned(stateDir, agent, report, warning.Kind),
		})
	}
	if !selection.json {
		_, _ = fmt.Fprintf(platform.Stdout, "report: %s\n", report)
		_, _ = fmt.Fprintf(platform.Stdout, "done: %d partial: %d env: %d skipped: %d\n", payload.Counts.Done, payload.Counts.Partial, payload.Counts.Env, payload.Counts.Skipped)
		for _, warning := range payload.Warnings {
			_, _ = fmt.Fprintln(platform.Stdout, warning.Message)
		}
		return 0
	}
	encoder := json.NewEncoder(platform.Stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(payload); err != nil {
		return 1
	}
	return 0
}

func rosterField(fields []string, index int) string {
	if index < len(fields) {
		return fields[index]
	}
	return ""
}

func countMarks(items []reportscan.Item, mark string) int {
	count := 0
	for _, item := range items {
		if item.Mark == mark {
			count++
		}
	}
	return count
}

func projectLayers(analysis waitpkg.ReportAnalysis) reportLayersJSON {
	layers := reportLayersJSON{
		Found: analysis.Layers.Found, Pass: analysis.Layers.Pass, Fail: analysis.Layers.Fail,
		NotRun: analysis.Layers.NotRun, Blocked: analysis.Layers.Blocked,
		NA: analysis.Layers.NA, Unmarked: analysis.Layers.Unmarked,
		Layers: make([]reportLayerJSON, 0, len(analysis.LayerEntries)),
	}
	for _, entry := range analysis.LayerEntries {
		layers.Layers = append(layers.Layers, reportLayerJSON{Name: entry.Name, Result: entry.Result, Reason: entry.Reason})
	}
	return layers
}

func projectFindingMarks(analysis waitpkg.ReportAnalysis) *reportFindingMarksJSON {
	if analysis.FindingMarks == nil {
		return nil
	}
	marks := analysis.FindingMarks
	return &reportFindingMarksJSON{
		Brief: marks.Brief, Impl: marks.Impl, Unknown: marks.Unknown,
		Classes: marks.Classes, Unmarked: marks.Unmarked, Numbers: intsOrEmpty(marks.Numbers),
	}
}

func intsOrEmpty(values []int) []int {
	if values == nil {
		return []int{}
	}
	return values
}

func stringsOrEmpty(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
