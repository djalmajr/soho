// Package stats implements the stats and collect commands.
package stats

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/djalmajr/soho/internal/collaboration"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/reportscan"
	"github.com/djalmajr/soho/internal/taskreport"
	textutil "github.com/djalmajr/soho/internal/text"
	"github.com/djalmajr/soho/internal/wait"
)

var pairRE = regexp.MustCompile(`^(.+)-(\d{8}T\d{6})(-\d+)?$`)
var roleRE = regexp.MustCompile("You are running as the `([^`]+)` role")
var dateRE = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})$`)
var isoRE = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2})(\.\d{1,9})?)?(Z|[+-]\d{2}:?\d{2})?$`)
var jsWhitespace = `\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}`
var shaLineRE = regexp.MustCompile(`^[` + jsWhitespace + `]*([0-9a-f]{64})[` + jsWhitespace + `]+\*?([^` + jsWhitespace + `].*?)[` + jsWhitespace + `]*$`)

// shaCommentRE finds each whitespace-then-# that may start a trailing
// comment after the path on a sha256 line ("<sha256>  <path>  # note").
var shaCommentRE = regexp.MustCompile(`[` + jsWhitespace + `]+#`)

// shaDropComment reads a path whose sha256 line ends in a comment: sha256sum
// never writes one. When the path as written does not open, it tries the path
// cut before each " #" from the right, only while the cut-off text holds no
// path separator (a " # " inside a directory name is part of the path), and
// returns the first cut that opens.
func shaDropComment(p string) (string, []byte, bool) {
	locs := shaCommentRE.FindAllStringIndex(p, -1)
	for i := len(locs) - 1; i >= 0; i-- {
		if strings.ContainsAny(p[locs[i][1]:], `/\`) {
			break
		}
		if b, err := os.ReadFile(p[:locs[i][0]]); err == nil {
			return p[:locs[i][0]], b, true
		}
	}
	return p, nil, false
}

var dimensions = []string{"role", "kind", "model", "kind-model", "agent", "effort"}
var reviewRoles = strings.Fields(core.ReviewRolesAll)

type CommandContext struct {
	Config      *core.Config
	Env         platform.Env
	Cwd         string
	FrictionLog string
}

type sidecar struct{ submission, kind, model, effort, arrival, session, taskReport, briefSHA string }
type sessionInfo struct{ session, kind, model string }

// sameSession decides whether two counted non-amendment pairs of the same
// agent belong to one worker session: the recorded session when both
// sidecars carry it, and the kind and model of the earlier pair when a
// legacy sidecar has no session.
func sameSession(prev, cur sessionInfo) bool {
	if prev.session != "" && cur.session != "" {
		return prev.session == cur.session
	}
	return prev.kind == cur.kind && prev.model == cur.model
}

type prompt struct {
	agent, ts, path, report, sidecarPath, role, resolved, noReport string
	suffix                                                         int
	mtime, reportM                                                 float64
	amendment, hasReport, counted, reuse                           bool
	scratch                                                        bool // the pair is in the scratch reports dir
	snapshot                                                       *sidecar
	minutes                                                        *float64
	partials                                                       int
	header                                                         *reportscan.ReviewHeaderResult
	isLast                                                         bool
}
type aggregate struct {
	tasks, amendments, reuses, pending, lost, notReceived, partials int
	minutes                                                         []float64
	lostBriefs                                                      []string
}
type review struct{ header, pass, fail, p0, p1, p2, p3, noHeader int }

func output(s string) { _, _ = platform.Stdout.Write([]byte(s)) }
func die(msg string, code int, command, logFile string) {
	core.DieFriction(msg, code, logFile, command)
}
func warn(msg, logFile string)             { core.Warn(msg, logFile, "stats") }
func jsObject(pairs ...any) *jsonjs.Object { return jsonjs.O(pairs...) }
func number(v int) any                     { return v }

// ParseSince reads a `--since` value: AAAA-MM-DD (local midnight) or an ISO
// 8601 date (with optional seconds, fraction and zone).
func ParseSince(value string) (time.Time, bool) {
	if m := dateRE.FindStringSubmatch(value); m != nil {
		y, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		d, _ := strconv.Atoi(m[3])
		t := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.Local)
		if t.Year() != y || int(t.Month()) != mo || t.Day() != d {
			return time.Time{}, false
		}
		return t, true
	}
	m := isoRE.FindStringSubmatch(value)
	if m == nil {
		return time.Time{}, false
	}
	parts := make([]int, 5)
	for i, s := range m[1:6] {
		parts[i], _ = strconv.Atoi(s)
	}
	sec := 0
	if m[6] != "" {
		sec, _ = strconv.Atoi(m[6])
	}
	if parts[1] < 1 || parts[1] > 12 || parts[2] < 1 || parts[2] > 31 || parts[3] > 23 || parts[4] > 59 || sec > 59 {
		return time.Time{}, false
	}
	local := time.Date(parts[0], time.Month(parts[1]), parts[2], 0, 0, 0, 0, time.UTC)
	if local.Month() != time.Month(parts[1]) || local.Day() != parts[2] {
		return time.Time{}, false
	}
	ns := int64(0)
	if m[7] != "" {
		f := m[7][1:]
		ns, _ = strconv.ParseInt(f+strings.Repeat("0", 9-len(f)), 10, 64)
	}
	zone := time.Local
	if m[8] != "" {
		if m[8] == "Z" {
			zone = time.UTC
		} else {
			z := strings.ReplaceAll(m[8], ":", "")
			sign := 1
			if z[0] == '-' {
				sign = -1
			}
			hh, _ := strconv.Atoi(z[1:3])
			mm, _ := strconv.Atoi(z[3:5])
			if hh > 23 || mm > 59 {
				return time.Time{}, false
			}
			zone = time.FixedZone("", sign*(hh*3600+mm*60))
		}
	}
	return time.Date(parts[0], time.Month(parts[1]), parts[2], parts[3], parts[4], sec, int(ns), zone), true
}

func collectPairs(sd, tmp, logFile string) []prompt {
	out := []prompt{}
	seen := map[string]bool{}
	scan := func(dir string, isTmp bool) {
		if dir == "" {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			f := e.Name()
			if isTmp && !strings.HasSuffix(f, ".brief.md") || !isTmp && !strings.HasSuffix(f, ".md") {
				continue
			}
			suffix := ".md"
			if isTmp {
				suffix = ".brief.md"
			}
			base := strings.TrimSuffix(f, suffix)
			m := pairRE.FindStringSubmatch(base)
			if m == nil {
				continue
			}
			key := m[1] + "\t" + m[2] + "\t" + m[3]
			if seen[key] {
				continue
			}
			seen[key] = true
			path := filepath.Join(dir, f)
			st, err := os.Stat(path)
			if isTmp {
				// A scratch brief is used only as a regular file of this
				// user, never through a link.
				st, err = core.StatScratchReport(path)
				if refusedScratch(err) {
					warn("stats: skipped "+f+" ("+err.Error()+")", logFile)
				}
			}
			if err != nil {
				continue
			}
			n := 1
			if m[3] != "" {
				n, _ = strconv.Atoi(m[3][1:])
			}
			report := filepath.Join(sd, "reports", base+".md")
			if isTmp {
				report = filepath.Join(dir, base+".md")
			}
			out = append(out, prompt{agent: m[1], ts: m[2], suffix: n, path: path, report: report, sidecarPath: filepath.Join(dir, base+".dispatch.json"), mtime: float64(st.ModTime().UnixNano()) / 1e6, counted: true, scratch: isTmp})
		}
	}
	scan(filepath.Join(sd, "briefs"), false)
	scan(tmp, true)
	return out
}

// refusedScratch reports a scratch file the private-scratch check refused.
func refusedScratch(err error) bool {
	var refused *core.ScratchRefusedError
	return errors.As(err, &refused)
}

// readStatsFile reads a pair file: through core.ReadScratchReport (never a
// link) for a scratch pair, with a warning when it is refused.
func readStatsFile(p *prompt, path, logFile string) ([]byte, error) {
	if !p.scratch {
		return os.ReadFile(path)
	}
	b, err := core.ReadScratchReport(path)
	if refusedScratch(err) {
		warn("stats: skipped "+filepath.Base(path)+" ("+err.Error()+")", logFile)
	}
	return b, err
}

func readSidecar(p *prompt, logFile string) {
	b, e := readStatsFile(p, p.sidecarPath, logFile)
	if os.IsNotExist(e) {
		return
	}
	if refusedScratch(e) {
		p.counted = false
		return
	}
	if e != nil {
		invalidSidecar(p, logFile)
		return
	}
	v, e := jsonjs.Parse(b)
	if e != nil {
		invalidSidecar(p, logFile)
		return
	}
	o, ok := v.(*jsonjs.Object)
	if !ok {
		invalidSidecar(p, logFile)
		return
	}
	get := func(k string) (string, bool) {
		x, present := o.Get(k)
		s, good := x.(string)
		return s, present && good
	}
	version, _ := o.Get("version")
	sub, sok := get("submission")
	kind, kok := get("kind")
	model, mok := get("model")
	effort, eok := get("effort")
	arrival, aok := get("arrival")
	session, seok := get("session")
	// s72 durable fields: optional; a sidecar without them stays valid, and a
	// wrong-typed value is ignored rather than invalidating the sidecar.
	taskReport, _ := get("task_report")
	briefSHA, _ := get("brief_sha256")
	av, ap := o.Get("arrival")
	_, sp := o.Get("session")
	_ = av
	if version != float64(1) || !sok || !kok || !mok || !eok || (sub != "attempted" && sub != "accepted" && sub != "failed") || (ap && !aok) || (sp && !seok) {
		invalidSidecar(p, logFile)
		return
	}
	ar := ""
	if ap {
		ar = arrival
	}
	ses := ""
	if sp {
		ses = session
	}
	p.snapshot = &sidecar{sub, kind, model, effort, ar, ses, taskReport, briefSHA}
	p.counted = sub == "accepted"
}
func invalidSidecar(p *prompt, logFile string) {
	p.counted = false
	warn("stats: "+filepath.Base(p.sidecarPath)+" is not a valid attempt sidecar; the pair is not counted as accepted", logFile)
}
func loadMeta(p *prompt, logFile string) {
	raw, _ := readStatsFile(p, p.path, logFile)
	b := platform.DecodeText(raw)
	p.amendment = strings.SplitN(b, "\n", 2)[0] == "# Amendment to your current brief"
	if m := roleRE.FindStringSubmatch(b); m != nil {
		p.role = m[1]
	}
	readSidecar(p, logFile)
	st, e := os.Stat(p.report)
	if p.scratch {
		st, e = core.StatScratchReport(p.report)
		if refusedScratch(e) {
			warn("stats: skipped "+filepath.Base(p.report)+" ("+e.Error()+")", logFile)
		}
	}
	if e != nil || st.Size() == 0 {
		return
	}
	p.hasReport = true
	p.reportM = float64(st.ModTime().UnixNano()) / 1e6
	mins := (p.reportM - p.mtime) / 60000
	p.minutes = &mins
	reportRaw, _ := readStatsFile(p, p.report, logFile)
	report := platform.DecodeText(reportRaw)
	p.partials = reportscan.PartialCount(report)
	p.header = reportscan.ReviewHeader(report)
}
func dim(p prompt, d string) string {
	switch d {
	case "role":
		return p.resolved
	case "agent":
		return p.agent
	}
	if p.snapshot == nil {
		return "(unknown)"
	}
	var v string
	switch d {
	case "kind":
		v = p.snapshot.kind
	case "model":
		v = p.snapshot.model
	case "effort":
		v = p.snapshot.effort
	case "kind-model":
		if p.snapshot.kind == "" {
			return "(unknown)"
		}
		if p.snapshot.model == "" {
			return p.snapshot.kind + "/-"
		}
		return p.snapshot.kind + "/" + p.snapshot.model
	}
	if v == "" {
		return "(unknown)"
	}
	return v
}
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// JS sorts these keys by UTF-16 code unit (compareCodeUnits); sort.Strings
	// compares UTF-8 bytes and puts U+E000 before an astral character.
	sort.Slice(out, func(i, j int) bool { return textutil.CompareUTF16(out[i], out[j]) < 0 })
	return out
}

// groupFor records first-seen order for the ordinary-object roles JSON output.
func groupFor(groups map[string]*aggregate, groupOrder *[]string, key string) *aggregate {
	if a := groups[key]; a != nil {
		return a
	}
	a := &aggregate{}
	groups[key] = a
	*groupOrder = append(*groupOrder, key)
	return a
}

func toFixedOne(n float64) string {
	switch {
	case math.IsNaN(n):
		return "NaN"
	case math.IsInf(n, 1):
		return "Infinity"
	case math.IsInf(n, -1):
		return "-Infinity"
	case n == 0:
		return "0.0"
	case math.Abs(n) >= 1e21:
		return strconv.FormatFloat(n, 'g', -1, 64)
	}
	sign := ""
	if n < 0 {
		sign = "-"
		n = -n
	}
	ratio := new(big.Rat).SetFloat64(n)
	scaled := new(big.Int).Mul(ratio.Num(), big.NewInt(10))
	whole, remainder := new(big.Int).QuoRem(scaled, ratio.Denom(), new(big.Int))
	if new(big.Int).Lsh(remainder, 1).Cmp(ratio.Denom()) >= 0 {
		whole.Add(whole, big.NewInt(1))
	}
	digits := whole.String()
	if len(digits) == 1 {
		digits = "0" + digits
	}
	return sign + digits[:len(digits)-1] + "." + digits[len(digits)-1:]
}

func round1(n float64) float64 {
	rounded, _ := strconv.ParseFloat(toFixedOne(n), 64)
	return rounded
}
func minuteStats(xs []float64) any {
	if len(xs) == 0 {
		return nil
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	sum := 0.0
	for _, x := range xs {
		sum += x
	}
	med := s[len(s)/2]
	if len(s)%2 == 0 {
		med = (s[len(s)/2-1] + s[len(s)/2]) / 2
	}
	return jsObject("avg", round1(sum/float64(len(xs))), "median", round1(med), "max", round1(s[len(s)-1]))
}
func aggregateJSON(a aggregate) *jsonjs.Object {
	return jsObject("tasks", number(a.tasks), "briefs", number(a.tasks+a.reuses), "amendments", number(a.amendments), "reuses", number(a.reuses), "no_report", jsObject("pending", number(a.pending), "lost", number(a.lost)), "not_received", number(a.notReceived), "minutes", minuteStats(a.minutes), "partials", number(a.partials))
}
func reviewJSON(r review) *jsonjs.Object {
	return jsObject("header", number(r.header), "pass", number(r.pass), "fail", number(r.fail), "severity", jsObject("P0", number(r.p0), "P1", number(r.p1), "P2", number(r.p2), "P3", number(r.p3)), "no_header", number(r.noHeader))
}

func CmdStats(args []string, command CommandContext) int {
	ctx, env, cwd, logFile := command.Config, command.Env, command.Cwd, command.FrictionLog
	bySet := false
	sinceRaw, by := "", ""
	asJSON := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--since":
			if i+1 == len(args) {
				die("stats: --since expects a date", 2, "stats", logFile)
			}
			i++
			sinceRaw = args[i]
		case "--by":
			bySet = true
			if i+1 == len(args) {
				die("stats: --by expects a dimension", 2, "stats", logFile)
			}
			i++
			by = args[i]
		case "--json":
			asJSON = true
		default:
			die("stats: unknown option "+a, 2, "stats", logFile)
		}
	}
	if bySet {
		ok := false
		for _, d := range dimensions {
			if by == d {
				ok = true
			}
		}
		if !ok {
			die("stats: --by expects one of role, kind, model, kind-model, agent, effort (got '"+by+"')", 2, "stats", logFile)
		}
	}
	var since time.Time
	hasSince := sinceRaw != ""
	if hasSince {
		var ok bool
		since, ok = ParseSince(sinceRaw)
		if !ok {
			die("stats: --since expects AAAA-MM-DD or an ISO date (got '"+sinceRaw+"')", 2, "stats", logFile)
		}
	}
	sd := core.StateDir(ctx, env, cwd)
	// The scratch reports dir is read only when its three levels are this
	// user's private directories (DJA-443): a refused one is skipped with a
	// warning, a missing one has nothing to read.
	tmp, scratchErr := core.PrivateScratchReports(env, core.WorkspaceID(ctx, env, cwd), core.ScratchFix)
	if scratchErr != nil {
		if !errors.Is(scratchErr, os.ErrNotExist) {
			warn("stats: skipped the scratch reports dir ("+scratchErr.Error()+")", logFile)
		}
		tmp = ""
	}
	pairs := collectPairs(sd, tmp, logFile)
	if len(pairs) == 0 {
		output("no dispatches recorded under " + filepath.Join(sd, "briefs") + "\n")
		return 0
	}
	for i := range pairs {
		loadMeta(&pairs[i], logFile)
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		a, b := pairs[i], pairs[j]
		if a.agent != b.agent {
			return textutil.CompareUTF16(a.agent, b.agent) < 0
		}
		if a.ts != b.ts {
			return a.ts < b.ts
		}
		return a.suffix < b.suffix
	})
	// A non-amendment pair is a reuse when the agent already had an earlier
	// counted non-amendment pair of the same worker session (dispatch order),
	// with or without a role change.
	seenNonAmendment := map[string][]sessionInfo{}
	prevAgent := ""
	prevResolved := ""
	for i := range pairs {
		p := &pairs[i]
		if p.agent != prevAgent {
			prevAgent = p.agent
			prevResolved = ""
		}
		if !p.counted {
			continue
		}
		if !p.amendment {
			p.resolved = p.role
			if p.resolved == "" {
				p.resolved = "(unknown)"
			}
			info := sessionInfo{}
			if p.snapshot != nil {
				info = sessionInfo{p.snapshot.session, p.snapshot.kind, p.snapshot.model}
			}
			for _, prev := range seenNonAmendment[p.agent] {
				if sameSession(prev, info) {
					p.reuse = true
					break
				}
			}
			seenNonAmendment[p.agent] = append(seenNonAmendment[p.agent], info)
		} else {
			p.resolved = prevResolved
			if p.resolved == "" {
				p.resolved = "(unknown)"
			}
		}
		prevResolved = p.resolved
	}
	last := map[string]int{}
	for i, p := range pairs {
		if p.counted && !p.amendment {
			last[p.agent] = i
		}
	}
	// An amendment belongs to the brief it amends (SKILL.md): the task report
	// pointer (task-report-<agent>.json) keeps the task's report paths in
	// dispatch order (history plus current), and a member whose report exists
	// closes every earlier member of the same task — the worker reports on the
	// last amendment's path, covering the amended brief. A closed brief is
	// never pending or lost.
	closedReports := map[string]bool{}
	seenAgents := map[string]bool{}
	for _, p := range pairs {
		if seenAgents[p.agent] {
			continue
		}
		seenAgents[p.agent] = true
		pointer := taskreport.ReadTaskReportPointer(sd, p.agent)
		if pointer == nil {
			continue
		}
		sequence := []string{}
		if v, ok := pointer.Get("history"); ok {
			if items, ok := v.([]any); ok {
				for _, item := range items {
					if s, ok := item.(string); ok {
						sequence = append(sequence, s)
					}
				}
			}
		}
		if v, ok := pointer.Get("current"); ok {
			if s, ok := v.(string); ok {
				sequence = append(sequence, s)
			}
		}
		reported := false
		for i := len(sequence) - 1; i >= 0; i-- {
			if reported {
				closedReports[filepath.Clean(sequence[i])] = true
			}
			if st, e := os.Stat(sequence[i]); e == nil && st.Size() > 0 {
				reported = true
			}
		}
	}
	// The durable closure (s72): the sidecar's task_report ties each brief to
	// its task and survives a later dispatch that repoints the pointer (D20);
	// when a member has a non-empty report, every earlier member of the same
	// task is closed, in dispatch order. This adds to the pointer closure
	// above, which stays for the old sidecars without the field.
	closedByTask := map[string]bool{}
	for i := range pairs {
		p := &pairs[i]
		if p.snapshot == nil || p.snapshot.taskReport == "" || !p.hasReport {
			continue
		}
		// A member whose sidecar is not an accepted submission (failed,
		// attempted, or the field absent) does not close the group: a failed
		// plain send points at the restored pointer's task and its late
		// report belongs to its own, different task.
		if p.snapshot.submission != "accepted" {
			continue
		}
		for j := 0; j < i; j++ {
			if q := &pairs[j]; q.snapshot != nil && q.snapshot.taskReport == p.snapshot.taskReport {
				closedByTask[filepath.Clean(q.report)] = true
			}
		}
	}
	for i := range pairs {
		pairs[i].isLast = pairs[i].counted && last[pairs[i].agent] == i
	}
	if hasSince {
		filtered := pairs[:0]
		for _, p := range pairs {
			if p.mtime >= float64(since.UnixNano())/1e6 {
				filtered = append(filtered, p)
			}
		}
		pairs = filtered
	}
	roster := map[string]bool{}
	for _, line := range core.RosterRows(sd) {
		f := strings.Split(line, "\t")
		if len(f) > 0 && f[0] != "" {
			roster[f[0]] = true
		}
	}
	groups := map[string]*aggregate{}
	groupOrder := []string{}
	reviews := map[string]*review{}
	for _, p := range pairs {
		if !p.counted {
			continue
		}
		key := p.resolved
		if bySet {
			key = dim(p, by)
		}
		a := groupFor(groups, &groupOrder, key)
		if p.amendment {
			a.amendments++
		} else if p.reuse {
			a.reuses++
		} else {
			a.tasks++
		}
		if !p.amendment && !p.hasReport && !closedReports[filepath.Clean(p.report)] && !closedByTask[filepath.Clean(p.report)] {
			if roster[p.agent] && p.isLast {
				p.noReport = "pending"
				a.pending++
			} else {
				p.noReport = "lost"
				a.lost++
				a.lostBriefs = append(a.lostBriefs, p.path)
			}
		}
		if p.snapshot != nil && p.snapshot.submission == "accepted" && p.snapshot.arrival == "not-received" {
			a.notReceived++
		}
		if p.minutes != nil {
			a.minutes = append(a.minutes, *p.minutes)
		}
		a.partials += p.partials
		if contains(reviewRoles, p.resolved) && (by == "" || p.hasReport) {
			rk := p.resolved
			if bySet {
				rk = dim(p, by)
			}
			r := reviews[rk]
			if r == nil {
				r = &review{}
				reviews[rk] = r
			}
			if p.hasReport {
				addReview(r, p.header)
			}
		}
	}
	if asJSON {
		return printStatsJSON(groups, reviews, bySet, by, groupOrder)
	}
	return printStatsText(groups, reviews, bySet, by)
}
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
func addReview(r *review, h *reportscan.ReviewHeaderResult) {
	if h == nil {
		r.noHeader++
		return
	}
	r.header++
	if h.Verdict == "pass" {
		r.pass++
	} else {
		r.fail++
	}
	r.p0 += int(h.Severity["P0"])
	r.p1 += int(h.Severity["P1"])
	r.p2 += int(h.Severity["P2"])
	r.p3 += int(h.Severity["P3"])
}

func printStatsJSON(groups map[string]*aggregate, reviews map[string]*review, bySet bool, by string, groupOrder []string) int {
	g := jsonjs.O()
	groupKeys := sortedKeys(groups)
	if !bySet {
		groupKeys = groupOrder
	}
	for _, k := range groupKeys {
		g.Set(k, aggregateJSON(*groups[k]))
	}
	lb := jsonjs.O()
	for _, k := range sortedKeys(groups) {
		if len(groups[k].lostBriefs) > 0 {
			lb.Set(k, groups[k].lostBriefs)
		}
	}
	r := jsonjs.O()
	for _, k := range sortedKeys(reviews) {
		r.Set(k, reviewJSON(*reviews[k]))
	}
	obj := jsonjs.O()
	if bySet {
		obj.Set("by", by)
		obj.Set("groups", g)
	} else {
		obj.Set("roles", g)
	}
	obj.Set("lost_briefs", lb)
	obj.Set("review", r)
	output(jsonjs.Stringify(obj) + "\n")
	return 0
}
func pad(c string, n int, left bool) string {
	width := len(utf16.Encode([]rune(c)))
	if width >= n {
		return c
	}
	p := strings.Repeat(" ", n-width)
	if left {
		return p + c
	}
	return c + p
}
func table(headers []string, rows [][]string) string {
	w := make([]int, len(headers))
	for i, h := range headers {
		w[i] = len(utf16.Encode([]rune(h)))
	}
	for _, r := range rows {
		for i, c := range r {
			if width := len(utf16.Encode([]rune(c))); width > w[i] {
				w[i] = width
			}
		}
	}
	fmtrow := func(c []string) string {
		out := make([]string, len(c))
		for i, x := range c {
			out[i] = pad(x, w[i], i > 0)
		}
		return strings.Join(out, "  ")
	}
	out := []string{fmtrow(headers)}
	for _, r := range rows {
		out = append(out, fmtrow(r))
	}
	return strings.Join(out, "\n")
}
func printStatsText(groups map[string]*aggregate, reviews map[string]*review, bySet bool, by string) int {
	dimName := by
	if dimName == "" {
		dimName = "role"
	}
	rows := [][]string{}
	lost := []string{}
	for _, k := range sortedKeys(groups) {
		a := groups[k]
		mins := minuteStats(a.minutes)
		av, med, mx := "-", "-", "-"
		if o, ok := mins.(*jsonjs.Object); ok {
			v, _ := o.Get("avg")
			av = toFixedOne(v.(float64))
			v, _ = o.Get("median")
			med = toFixedOne(v.(float64))
			v, _ = o.Get("max")
			mx = toFixedOne(v.(float64))
		}
		rows = append(rows, []string{k, strconv.Itoa(a.tasks), strconv.Itoa(a.tasks + a.reuses), strconv.Itoa(a.amendments), strconv.Itoa(a.reuses), fmt.Sprintf("%d (%d/%d)", a.pending+a.lost, a.pending, a.lost), strconv.Itoa(a.notReceived), av, med, mx, strconv.Itoa(a.partials)})
		for _, p := range a.lostBriefs {
			lost = append(lost, k+": "+p)
		}
	}
	output("tasks by " + dimName + ":\n" + table([]string{dimName, "tasks", "briefs", "amendments", "reuses", "no-report (pending/lost)", "not-received", "avg min", "median min", "max min", "partials"}, rows) + "\n\n")
	rRows := [][]string{}
	keys := sortedKeys(reviews)
	if !bySet {
		keys = []string{}
		for _, k := range reviewRoles {
			if reviews[k] != nil {
				keys = append(keys, k)
			}
		}
	}
	for _, k := range keys {
		r := reviews[k]
		rRows = append(rRows, []string{k, strconv.Itoa(r.header), strconv.Itoa(r.pass), strconv.Itoa(r.fail), strconv.Itoa(r.p0), strconv.Itoa(r.p1), strconv.Itoa(r.p2), strconv.Itoa(r.p3), strconv.Itoa(r.noHeader)})
	}
	output("reviews by " + dimName + ":\n" + table([]string{dimName, "header", "pass", "fail", "P0", "P1", "P2", "P3", "no-header"}, rRows) + "\n")
	if len(lost) > 0 {
		output("\nlost briefs by " + dimName + ":\n" + strings.Join(lost, "\n") + "\n")
	}
	return 0
}

func CmdCollect(args []string, command CommandContext) int {
	ctx, env, cwd, logFile := command.Config, command.Env, command.Cwd, command.FrictionLog
	if len(args) == 0 {
		platform.Die("agent: Parameter not set", 1)
	}
	agent := args[0]
	lines := "120"
	linesSet, verify := false, false
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--lines":
			if i+1 == len(args) {
				core.DieFriction("collect: --lines expects a value", 2, logFile, "collect")
			}
			i++
			lines = args[i]
			linesSet = true
		case "--verify":
			verify = true
		default:
			core.DieFriction("collect: unknown option "+args[i], 2, logFile, "collect")
		}
	}
	sd := core.StateDir(ctx, env, cwd)
	// An active collaboration outranks the report: while its assignment is
	// active the participant is still working, so the initial or stale
	// report is an artifact, never the completion. The cycle is shown
	// instead, read-only: no SyncTaskReport, no terminal fallback, no
	// retry. The inspection is the same one status/wait use (the active
	// assignment, the verified participants and the single policy engine's
	// mode), and the workspace is derived at most once, only when needed.
	if line := core.RosterLine(sd, agent); line != "" {
		pane := ""
		if fields := strings.Split(line, "\t"); len(fields) > 1 {
			pane = fields[1]
		}
		if view, ok := wait.CollaborationViewForPane(sd, pane, ctx, wait.DerivedWorkspaceEnv(ctx, env, cwd)); ok {
			return collectCollaboration(agent, view, verify, env, cwd, logFile, sd)
		}
	}
	pointer := taskreport.ReadTaskReportPointer(sd, agent)
	taskPath := ""
	if pointer != nil {
		// SyncTaskReport rewrites (or removes) the stable task report, so
		// under SOHO_NOWRITE the read skips it: the pointer is still
		// consulted for its path, nothing is written.
		if !core.Nowrite(env) {
			_, _ = taskreport.SyncTaskReport(sd, agent)
		}
		v, _ := pointer.Get("task_report")
		taskPath, _ = v.(string)
	}
	report := core.LastReport(sd, agent)
	if report != "" {
		if _, e := os.Stat(report); e != nil {
			copy := filepath.Join(sd, "reports", filepath.Base(report))
			if _, ce := os.Stat(copy); ce == nil {
				report = copy
			}
		}
	}
	if report != "" {
		if st, e := os.Stat(report); e == nil && st.Size() > 0 {
			if verify {
				return verifyFiles(sd, agent, report, env, cwd, logFile)
			}
			output("<!-- report: " + report + " -->\n")
			if taskPath != "" {
				if _, e := os.Stat(taskPath); e == nil {
					output("<!-- task report: " + taskPath + " -->\n")
				}
			}
			raw, e := os.ReadFile(report)
			if e != nil {
				core.DieFriction("collect: cannot read "+report, 4, logFile, "collect")
			}
			_, _ = platform.Stdout.Write(raw)
			return 0
		}
	}
	if core.RosterLine(sd, agent) != "" {
		st := herdr.AgentState(agent, env, herdr.Timeout, nil)
		if st.State == "unavailable" {
			core.Warn("no report file yet for '"+agent+"', and herdr agent get failed: "+st.Cause+". The worker may still be live.", logFile, "collect")
			return 4
		}
		if (st.State == "working" || st.State == "blocked") && !linesSet {
			core.Warn("'"+agent+"' is "+st.State+" and has no report yet ("+orNone(report)+"); wait for it: soho wait "+agent, logFile, "collect")
			return 4
		}
	}
	core.Warn("no report file yet for '"+agent+"' (expected "+orNone(report)+"); falling back to recent terminal output", logFile, "collect")
	r := platform.RunCli("herdr", []string{"agent", "read", agent, "--source", "recent-unwrapped", "--lines", lines}, platform.RunOptions{Env: env, TimeoutMs: int(herdr.Timeout / time.Millisecond)})
	if r.Stdout != "" {
		output(r.Stdout)
	}
	if r.Stderr != "" {
		_, _ = platform.Stderr.Write([]byte(r.Stderr))
	}
	if r.NotFound {
		platform.Die("herdr CLI not found in PATH", 2)
	}
	if r.Status == nil {
		return 1
	}
	if *r.Status != 0 {
		return *r.Status
	}
	return 6
}
func orNone(s string) string {
	if s == "" {
		return "<none dispatched>"
	}
	return s
}

// collectCollaboration is the collect output while the agent's roster pane
// hosts an active collaboration: the cycle's state — the assignment, its
// participants, the phase, round, revision and last delivery — and the
// last evidence (the last event's report, or the initial/stale report
// before any event). The report is an artifact, never the completion: it
// is named but never printed, and nothing is finalized or approved here —
// only the orchestrator may finalize it. Without evidence the state is
// shown with an explicit absence and exit 0; the two failures (an
// unreadable or unverifiable state, or an off or unreadable policy) show
// the cause and exit 4, keeping the assignment's metadata when it was
// read. --verify checks the evidence's hashes when it exists; it never
// finalizes the cycle.
func collectCollaboration(agent string, view wait.CollaborationView, verify bool, env platform.Env, cwd, logFile, sd string) int {
	stale := ""
	if p := core.LastReport(sd, agent); p != "" {
		if _, e := os.Stat(p); e == nil {
			if _, ce := os.Stat(filepath.Join(sd, "reports", filepath.Base(p))); ce == nil {
				p = filepath.Join(sd, "reports", filepath.Base(p))
			}
			stale = p
		}
	}
	evidence := ""
	if view.Assignment != nil {
		if events := view.Assignment.Events; len(events) > 0 {
			evidence = events[len(events)-1].Report
		}
	}
	if evidence == "" {
		evidence = stale
	}
	fields := []any{"agent", agent, "status", view.State, "report", absent(stale)}
	if view.Assignment != nil {
		fields = append(fields,
			"assignment", view.Assignment.ID,
			"participants", []any{view.Assignment.Author.Name, view.Assignment.Reviewer.Name},
			"phase", view.Assignment.Phase, "round", view.Assignment.Round,
			"max_rounds", view.Assignment.MaxRounds, "revision", view.Assignment.Revision,
			"delivery", view.Delivery)
	} else {
		fields = append(fields, "assignment", "none")
	}
	fields = append(fields, "evidence", absent(evidence))
	if view.State != "collaborating" {
		fields = append(fields, "cause", view.Cause)
	}
	line := jsonjs.Stringify(jsObject(fields...)) + "\n"
	if verify && view.State == "collaborating" && evidence != "" {
		output(line)
		hash, err := collaboration.ValidReport(evidence)
		if err != nil {
			core.DieFriction("collect --verify: cannot read evidence: "+err.Error(), 4, logFile, "collect")
		}
		if view.Assignment != nil && len(view.Assignment.Events) > 0 {
			events := view.Assignment.Events
			if hash != events[len(events)-1].ReportHash {
				core.DieFriction("collect --verify: round evidence changed after publication", 16, logFile, "collect")
			}
		}
		return verifyFiles(sd, agent, evidence, env, cwd, logFile)
	}
	output(line)
	return view.Code
}

// absent renders an empty path as the explicit absence.
func absent(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
func verifyFiles(sd, agent, report string, env platform.Env, cwd, logFile string) int {
	raw, e := platform.ReadTextFile(report)
	if e != nil {
		core.DieFriction("collect --verify: cannot read "+report, 4, logFile, "collect")
	}
	files := map[string]string{}
	order := []string{}
	for _, line := range strings.Split(raw, "\n") {
		m := shaLineRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if _, ok := files[m[2]]; !ok {
			order = append(order, m[2])
		}
		files[m[2]] = m[1]
	}
	if len(files) == 0 {
		// Nothing was verified: that is not a pass. Hashes in a table or in
		// another layout are not read.
		output("no sha256 lines in " + report + ": nothing verified (write one line per file: <sha256>  <path>, as sha256sum prints it)\n")
		return 16
	}
	line := core.RosterLine(sd, agent)
	fields := strings.Split(line, "\t")
	worker := ""
	if len(fields) > 6 {
		worker = fields[6]
	}
	if !filepath.IsAbs(worker) {
		worker = filepath.Join(platform.ProjectRoot(env, cwd), worker)
	}
	okN, changed, missing := 0, 0, 0
	for _, name := range order {
		p := name
		if !filepath.IsAbs(p) {
			p = filepath.Join(worker, p)
		}
		b, e := os.ReadFile(p)
		if e != nil {
			if bare, bb, found := shaDropComment(p); found {
				p, b, e = bare, bb, nil
			}
		}
		state := ""
		if e != nil {
			state = "missing"
			missing++
		} else {
			sum := sha256.Sum256(b)
			if fmt.Sprintf("%x", sum) == files[name] {
				state = "ok"
				okN++
			} else {
				state = "changed"
				changed++
			}
		}
		output(state + " " + p + "\n")
	}
	output(fmt.Sprintf("verified %d: ok %d, changed %d, missing %d\n", okN+changed+missing, okN, changed, missing))
	if changed+missing > 0 {
		return 16
	}
	return 0
}
