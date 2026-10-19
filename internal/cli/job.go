package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/job"
	"github.com/djalmajr/soho/internal/platform"
)

// jobStdin is the stdin source for the job subcommands' "-" bodies; tests
// swap it.
var jobStdin io.Reader = os.Stdin

// jobFlagSpec names the flags each job subcommand accepts. The writing
// subcommands validate their inputs here and refuse with exit 2 until the
// phase 2 side effects land.
var jobFlagSpec = map[string]struct {
	valued []string
	bare   []string
}{
	"status":     {valued: []string{"--id"}},
	"wait":       {valued: []string{"--id", "--timeout"}},
	"events":     {valued: []string{"--id", "--since", "--wait"}},
	"collect":    {valued: []string{"--id"}, bare: []string{"--md", "--verify"}},
	"list":       {valued: []string{"--state"}},
	"start":      {valued: []string{"--id", "--repo", "--base", "--mode", "--brief", "--timeout"}, bare: []string{"--dry-run"}},
	"ack":        {valued: []string{"--id", "--upto"}},
	"close":      {valued: []string{"--id"}, bare: []string{"--force"}},
	"note":       {valued: []string{"--id", "--tipo", "--escopo", "--motivo", "--refs"}},
	"amend":      {valued: []string{"--id"}},
	"send":       {valued: []string{"--id"}},
	"cancel":     {valued: []string{"--id", "--grace"}},
	"checkpoint": {valued: []string{"--id"}},
	"supervise":  {valued: []string{"--id", "--repo"}},
}

var jobNoteTypes = map[string]bool{
	"checkpoint": true, "question": true, "decision": true, "note": true,
}

var (
	jobRepoPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}/[A-Za-z0-9._-]{1,100}$`)
	jobBasePattern = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,100}$`)
)

const (
	jobAmendBodyLimit = 64 << 10
	jobSendBodyLimit  = 16 << 10
	jobNoteBodyLimit  = 16 << 10
)

// jobLifecycleStates are the contract lifecycle states accepted by --state.
var jobLifecycleStates = map[string]bool{
	job.StatusAccepted: true, job.StatusPreparing: true, job.StatusRunning: true,
	job.StatusBlocked: true, job.StatusFinishing: true, job.StatusDone: true,
	job.StatusFailed: true, job.StatusTimeout: true, job.StatusCanceled: true,
	job.StatusCollected: true, job.StatusClosed: true,
}

const jobWaitBoundMS = 600000

// cmdJob dispatches the job command family. It runs outside Herdr; the
// read subcommands (status, wait, events, collect, list) read files only;
// start (without --dry-run) records the job, prepares the checkout, opens
// the Herdr workspace and launches the supervisor; supervise runs the
// supervisor inside the job's workspace; ack, close and note change state.
func cmdJob(args []string, env platform.Env, cwd string, launch job.LaunchLocators) int {
	if len(args) == 0 {
		platform.Die("usage: job <status|wait|events|collect|list> --id <id> [--flags]", 2)
	}
	sub := args[0]
	rest := args[1:]
	if _, ok := jobFlagSpec[sub]; !ok {
		platform.Die("job: unknown subcommand '"+sub+"'", 2)
	}
	switch sub {
	case "status":
		return jobStatus(rest, env, cwd)
	case "wait":
		return jobWait(rest, env, cwd)
	case "events":
		return jobEvents(rest, env, cwd)
	case "collect":
		return jobCollect(rest, env, cwd)
	case "list":
		return jobList(rest, env)
	case "start":
		return jobStart(rest, env, cwd)
	case "ack":
		return jobAck(rest, env, cwd)
	case "close":
		return jobClose(rest, env, cwd)
	case "note":
		return jobNote(rest, env, cwd)
	case "amend":
		return jobAmend(rest, env, cwd)
	case "send":
		return jobSend(rest, env, cwd)
	case "cancel":
		return jobCancel(rest, env, cwd)
	case "checkpoint":
		return jobCheckpoint(rest, env, cwd)
	case "supervise":
		return jobSupervise(rest, env, launch)
	default:
		// Every subcommand in the flag spec has a case above; the spec
		// check already refused the unknown ones.
		platform.Die("job "+sub+": not available yet", 2)
		return 0
	}
}

// parseJobFlags parses one subcommand's flags strictly: only the spec's
// flags, in "--flag value" or "--flag=value" form, each at most once;
// anything else dies with exit 2. Non-flag tokens are returned as the
// positional arguments; the caller decides whether they are allowed.
func parseJobFlags(sub string, args []string) (vals map[string]string, bare map[string]bool, pos []string) {
	spec := jobFlagSpec[sub]
	valued := map[string]bool{}
	for _, flag := range spec.valued {
		valued[flag] = true
	}
	vals = map[string]string{}
	bare = map[string]bool{}
	for i := 0; i < len(args); i++ {
		token := args[i]
		name, value, hasValue := strings.Cut(token, "=")
		if !strings.HasPrefix(name, "--") {
			pos = append(pos, token)
			continue
		}
		if hasValue {
			if !valued[name] {
				platform.Die("job "+sub+": unknown flag '"+name+"'", 2)
			}
			if _, seen := vals[name]; seen {
				platform.Die("job "+sub+": repeated flag "+name, 2)
			}
			vals[name] = value
			continue
		}
		if _, isBare := bare[name]; isBare || !valued[name] {
			if !flagIn(spec.bare, name) {
				platform.Die("job "+sub+": unknown flag '"+name+"'", 2)
			}
			bare[name] = true
			continue
		}
		if _, seen := vals[name]; seen {
			platform.Die("job "+sub+": repeated flag "+name, 2)
		}
		if i+1 >= len(args) {
			platform.Die("job "+sub+": missing value for "+name, 2)
		}
		i++
		vals[name] = args[i]
	}
	return vals, bare, pos
}

func flagIn(list []string, flag string) bool {
	for _, item := range list {
		if item == flag {
			return true
		}
	}
	return false
}

// jobRequiredID returns a validated --id or dies with exit 2 (job.Dir's
// contract regex, plus the "." / ".." refusal).
func jobRequiredID(sub string, vals map[string]string) string {
	id, ok := vals["--id"]
	if !ok {
		platform.Die("job "+sub+": missing --id", 2)
	}
	if _, err := job.Dir("", id); err != nil {
		platform.Die(err.Error(), 2)
	}
	return id
}

// jobMSValue returns a bounded integer flag (0..600000) or its fallback.
func jobMSValue(sub, flag string, vals map[string]string, fallback int) int {
	raw, ok := vals[flag]
	if !ok {
		return fallback
	}
	n, ok := core.ParseTimeoutMS(raw)
	if !ok || n > jobWaitBoundMS {
		platform.Die("job "+sub+": "+flag+" expects milliseconds or a duration such as 90s, 30m or 2h, from 0 to 600000", 2)
	}
	return int(n)
}

// jobSinceValue returns the events --since flag (integer >= 0).
func jobSinceValue(sub string, vals map[string]string) int {
	raw, ok := vals["--since"]
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		platform.Die("job "+sub+": --since must be an integer of at least 0", 2)
	}
	return n
}

// allJobStores opens a job store for every checkout <job_repos_root>/*/* on
// this machine, resolving each checkout's state root read-only. sel is the
// job supervisor's explicit user configuration selection (zero everywhere
// else): it chooses only the user file and the home job_repos_root expands
// from; env stays the environment of every git call the lookup makes.
func allJobStores(env platform.Env, sel core.UserSelection) ([]*job.Store, error) {
	machine, err := job.LoadMachineFor(platform.Current(), env, sel)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(machine.ReposRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var stores []*job.Store
	for _, org := range entries {
		if !org.IsDir() {
			continue
		}
		repos, err := os.ReadDir(filepath.Join(machine.ReposRoot, org.Name()))
		if err != nil {
			continue
		}
		for _, repo := range repos {
			if !repo.IsDir() {
				continue
			}
			checkout := filepath.Join(machine.ReposRoot, org.Name(), repo.Name())
			cfg := core.LoadConfigFor(env, checkout, sel)
			stores = append(stores, job.Open(core.StateRootPath(&cfg, env, checkout)))
		}
	}
	return stores, nil
}

// locateJob resolves the store that holds jobs/<id>/state.json across every
// checkout. None holds it and the error is nil (the caller prints the
// not_found line and exits 3); more than one is exit 2.
func locateJob(env platform.Env, id string, sel core.UserSelection) (*job.Store, error) {
	if _, err := job.Dir("", id); err != nil {
		return nil, err
	}
	stores, err := allJobStores(env, sel)
	if err != nil {
		return nil, err
	}
	var hit *job.Store
	for _, store := range stores {
		if _, err := os.Stat(filepath.Join(store.Root, "jobs", id, "state.json")); err == nil {
			if hit != nil {
				return nil, &platform.ExitError{Code: 2, Msg: "job: id " + id + " exists in more than one checkout"}
			}
			hit = store
		}
	}
	return hit, nil
}

// locateJobInRepo is the --repo lookup of job supervise: it reads only
// the single checkout <ReposRoot>/<org>/<repo> of the same selection the
// scan would use (LoadMachineFor, LoadConfigFor and StateRootPath on that
// checkout), and the job exists when jobs/<id>/state.json is there. No
// other checkout is read, so an id that exists in more than one checkout
// is not a conflict here: a hit is this checkout's job, and none is nil
// (the caller prints the not_found line and exits 3).
func locateJobInRepo(env platform.Env, id, repo string, sel core.UserSelection) (*job.Store, error) {
	if _, err := job.Dir("", id); err != nil {
		return nil, err
	}
	org, repoName, _ := strings.Cut(repo, "/")
	machine, err := job.LoadMachineFor(platform.Current(), env, sel)
	if err != nil {
		return nil, err
	}
	checkout := filepath.Join(machine.ReposRoot, org, repoName)
	cfg := core.LoadConfigFor(env, checkout, sel)
	store := job.Open(core.StateRootPath(&cfg, env, checkout))
	if _, err := os.Stat(filepath.Join(store.Root, "jobs", id, "state.json")); err != nil {
		return nil, nil
	}
	return store, nil
}

// locateOwnJob is the lookup of the job subcommands that take --id (all
// but start and supervise): every checkout under job_repos_root, as
// locateJob scans them, plus at most one own-checkout anchor
// (job.OwnCheckoutStore: the job whose recorded directory cwd lies in).
// The anchor is gathered on every lookup, not only on a miss, and the
// candidates are merged by the identity of their store root: none is
// nil (not_found), one is that store, and more than one distinct store is
// exit 2 before the subcommand changes anything (the lookup itself only
// reads: the checkouts' configuration, git and the anchor's state.json).
func locateOwnJob(env platform.Env, cwd, id string) (*job.Store, error) {
	if _, err := job.Dir("", id); err != nil {
		return nil, err
	}
	stores, err := allJobStores(env, core.UserSelection{})
	if err != nil {
		return nil, err
	}
	var hits []*job.Store
	for _, store := range stores {
		if _, err := os.Stat(filepath.Join(store.Root, "jobs", id, "state.json")); err == nil {
			hits = append(hits, store)
		}
	}
	if anchor := job.OwnCheckoutStore(env, cwd, id); anchor != nil {
		hits = append(hits, anchor)
	}
	// The same store reached twice (an alias, or a case variant on a
	// case-insensitive file system) is one store: identity, not strings.
	var distinct []*job.Store
	var infos []os.FileInfo
	for _, store := range hits {
		info, err := os.Stat(store.Root)
		if err != nil {
			return nil, err
		}
		same := false
		for _, seen := range infos {
			if os.SameFile(seen, info) {
				same = true
				break
			}
		}
		if !same {
			distinct = append(distinct, store)
			infos = append(infos, info)
		}
	}
	switch len(distinct) {
	case 0:
		return nil, nil
	case 1:
		return distinct[0], nil
	default:
		return nil, &platform.ExitError{Code: 2, Msg: "job: id " + id + " exists in more than one checkout"}
	}
}

// dieJob maps a *job.ExitError or *platform.ExitError to platform.Die; any
// other error dies with exit 2 and its message.
func dieJob(sub string, err error) {
	var jobExit *job.ExitError
	if errors.As(err, &jobExit) && jobExit != nil {
		msg := jobExit.Msg
		// A preparation step that kept a diagnostic says it on a second
		// line, after the message the events carry; the exit does not change.
		if step, detail, ok := job.PrepareFailure(jobExit); ok {
			msg += "\nsoho: " + job.PrepareFailureLine(step, detail)
		}
		platform.Die(msg, jobExit.Code)
	}
	var platformExit *platform.ExitError
	if errors.As(err, &platformExit) && platformExit != nil {
		platform.Die(platformExit.Msg, platformExit.Code)
	}
	platform.Die("job "+sub+": "+err.Error(), 2)
}

// jobHasCode reports whether err is a *job.ExitError with the given code.
func jobHasCode(err error, code int) bool {
	var jobExit *job.ExitError
	return errors.As(err, &jobExit) && jobExit != nil && jobExit.Code == code
}

func printJobNotFound() {
	_, _ = fmt.Fprintln(platform.Stdout, `{"status":"not_found"}`)
}

// jobStatusLine is the status/wait JSON line (contract field order).
type jobStatusLine struct {
	ID      string  `json:"id"`
	Status  string  `json:"status"`
	Motivo  *string `json:"motivo"`
	Eventos struct {
		Total     int `json:"total"`
		UltimoSeq int `json:"ultimo_seq"`
	} `json:"eventos"`
	DecisionsAckedSeq  int   `json:"decisions_acked_seq"`
	DecisionsPendentes []int `json:"decisions_pendentes"`
	// Partial and PartialCollaborations carry the persisted partial facts
	// of the job's own collaborations (derived from the state's outcomes):
	// always present, false and [] when none; a legacy state without the
	// fields prints the empty values.
	Partial               bool     `json:"partial"`
	PartialCollaborations []string `json:"partial_collaborations"`
	DuplicateOf           string   `json:"duplicate_of,omitempty"`
	// ReportMissing stands only when the status carries a stored report
	// (job.ReportExpected) and the job has none: a legacy terminal job,
	// collected or closed without its report, reads as missing, never as
	// complete. It is out of the line in every other case, and the read
	// writes nothing.
	ReportMissing bool `json:"report_missing,omitempty"`
	// HeldControls is the status-side view of the requests the supervisor
	// is holding while the job orchestrator is blocked or in a question:
	// always present, [] when there are none, in the requests' seq order.
	// The count is job.HeldControls's read of control/ and the event log;
	// the CLI repeats none of that rule, and the read writes nothing.
	HeldControls []job.HeldControl `json:"held_controls"`
}

func printJobStatusLine(snap job.Snapshot) {
	partial := job.PartialCollaborations(snap.State)
	held, err := job.HeldControls(snap)
	if err != nil {
		platform.Die("job: cannot read held controls: "+err.Error(), 2)
	}
	line := jobStatusLine{
		ID:                    snap.State.ID,
		Status:                snap.State.Status,
		Motivo:                snap.State.Motivo,
		DecisionsAckedSeq:     snap.State.DecisionsAckedSeq,
		DecisionsPendentes:    snap.Pending,
		Partial:               len(partial) > 0,
		PartialCollaborations: partial,
		DuplicateOf:           snap.State.DuplicateOf,
		ReportMissing:         snap.Report == nil && job.ReportExpected(snap.State.Status),
		HeldControls:          held,
	}
	line.Eventos.Total = snap.EventTotal
	line.Eventos.UltimoSeq = snap.LastSeq
	if line.DecisionsPendentes == nil {
		line.DecisionsPendentes = []int{}
	}
	raw, err := json.Marshal(line)
	if err != nil {
		platform.Die("job: cannot render status: "+err.Error(), 2)
	}
	_, _ = fmt.Fprintln(platform.Stdout, string(raw))
}

// jobSnapshotOrNotFound locates the job (locateOwnJob) and returns its
// snapshot; an unknown id prints the contract's not_found line and
// returns 3.
func jobSnapshotOrNotFound(sub, id string, env platform.Env, cwd string) (job.Snapshot, int) {
	store, err := locateOwnJob(env, cwd, id)
	if err != nil {
		dieJob(sub, err)
		return job.Snapshot{}, 0
	}
	if store == nil {
		printJobNotFound()
		return job.Snapshot{}, 3
	}
	snap, err := store.Snapshot(id)
	if err != nil {
		if jobHasCode(err, job.ExitNotFound) {
			printJobNotFound()
			return job.Snapshot{}, 3
		}
		dieJob(sub, err)
		return job.Snapshot{}, 0
	}
	return snap, 0
}

// jobStoreOrNotFound locates the job store (locateOwnJob); an unknown id
// prints the not_found line and returns (nil, 3).
func jobStoreOrNotFound(sub, id string, env platform.Env, cwd string) (*job.Store, int) {
	store, err := locateOwnJob(env, cwd, id)
	if err != nil {
		dieJob(sub, err)
		return nil, 0
	}
	if store == nil {
		printJobNotFound()
		return nil, 3
	}
	return store, 0
}

func jobStatus(args []string, env platform.Env, cwd string) int {
	vals, _, pos := parseJobFlags("status", args)
	if len(pos) != 0 {
		platform.Die("job status: unexpected argument '"+pos[0]+"'", 2)
	}
	id := jobRequiredID("status", vals)
	snap, code := jobSnapshotOrNotFound("status", id, env, cwd)
	if code != 0 {
		return code
	}
	printJobStatusLine(snap)
	return 0
}

func jobWait(args []string, env platform.Env, cwd string) int {
	vals, _, pos := parseJobFlags("wait", args)
	if len(pos) != 0 {
		platform.Die("job wait: unexpected argument '"+pos[0]+"'", 2)
	}
	id := jobRequiredID("wait", vals)
	bound := jobMSValue("wait", "--timeout", vals, jobWaitBoundMS)
	store, code := jobStoreOrNotFound("wait", id, env, cwd)
	if code != 0 {
		return code
	}
	var waitErr error
	if _, waitErr = store.Wait(id, time.Duration(bound)*time.Millisecond); waitErr != nil && !jobHasCode(waitErr, job.ExitTimeout) {
		dieJob("wait", waitErr)
	}
	snap, err := store.Snapshot(id)
	if err != nil {
		dieJob("wait", err)
	}
	printJobStatusLine(snap)
	if jobHasCode(waitErr, job.ExitTimeout) {
		return job.ExitTimeout
	}
	return jobWaitExit(snap)
}

// jobWaitExit maps the state a wait ended in to the contract exit code;
// collected/closed use the stored report's terminal outcome (0 when no report).
func jobWaitExit(snap job.Snapshot) int {
	switch snap.State.Status {
	case job.StatusCollected, job.StatusClosed:
		if snap.Report == nil {
			return 0
		}
		return jobOutcomeExit(snap.Report.Status)
	default:
		return jobOutcomeExit(snap.State.Status)
	}
}

func jobOutcomeExit(status string) int {
	switch status {
	case job.StatusDone:
		return 0
	case job.StatusBlocked:
		return job.ExitBlocked
	case job.StatusTimeout:
		return job.ExitTimeout
	case job.StatusFailed:
		return job.ExitFailed
	case job.StatusCanceled:
		return job.ExitCanceled
	default:
		return 0
	}
}

func jobEvents(args []string, env platform.Env, cwd string) int {
	vals, _, pos := parseJobFlags("events", args)
	if len(pos) != 0 {
		platform.Die("job events: unexpected argument '"+pos[0]+"'", 2)
	}
	id := jobRequiredID("events", vals)
	since := jobSinceValue("events", vals)
	bound := jobMSValue("events", "--wait", vals, 0)
	store, code := jobStoreOrNotFound("events", id, env, cwd)
	if code != 0 {
		return code
	}
	page, err := store.Events(id, since, time.Duration(bound)*time.Millisecond)
	if err != nil {
		dieJob("events", err)
	}
	for _, line := range page.Lines() {
		_, _ = fmt.Fprintln(platform.Stdout, line)
	}
	return 0
}

func jobCollect(args []string, env platform.Env, cwd string) int {
	vals, bare, pos := parseJobFlags("collect", args)
	if len(pos) != 0 {
		platform.Die("job collect: unexpected argument '"+pos[0]+"'", 2)
	}
	id := jobRequiredID("collect", vals)
	store, code := jobStoreOrNotFound("collect", id, env, cwd)
	if code != 0 {
		return code
	}
	snap, err := store.Snapshot(id)
	if err != nil {
		dieJob("collect", err)
	}
	dir, err := job.Dir(store.Root, id)
	if err != nil {
		dieJob("collect", err)
	}
	if bare["--md"] {
		data, err := os.ReadFile(filepath.Join(dir, "report.md"))
		if err != nil {
			if os.IsNotExist(err) {
				platform.Die("job collect: report.md not found", 4)
			}
			dieJob("collect", err)
		}
		_, _ = platform.Stdout.Write(data)
		return 0
	}
	raw, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		if os.IsNotExist(err) {
			printJobCollectMissing(snap)
			return 0
		}
		dieJob("collect", err)
	}
	var report map[string]any
	if err := json.Unmarshal(raw, &report); err != nil {
		platform.Die("job collect: report.json unreadable: "+err.Error(), 2)
	}
	// A body of JSON null unmarshals into a nil map without error; refuse it
	// before the assignment instead of panicking. The file is not changed.
	if report == nil {
		platform.Die("job collect: report.json unreadable: expected an object", 2)
	}
	// The legacy view (the job contract): a report whose state predates the
	// partial fields reads partial: false and partial_collaborations: []
	// — never null; a present key stands untouched, and the read writes
	// nothing (the verify below keeps hashing the original on-disk bytes).
	if v, ok := report["partial"]; !ok || v == nil {
		report["partial"] = false
	}
	if v, ok := report["partial_collaborations"]; !ok || v == nil {
		report["partial_collaborations"] = []any{}
	}
	report["decisions_pendentes"] = snap.Pending
	line, err := json.Marshal(report)
	if err != nil {
		platform.Die("job collect: cannot render report.json: "+err.Error(), 2)
	}
	_, _ = fmt.Fprintln(platform.Stdout, string(line))
	if !bare["--verify"] {
		return 0
	}
	return jobVerifyArtefatos(dir, raw)
}

func printJobCollectMissing(snap job.Snapshot) {
	line := struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Report any    `json:"report"`
		// ReportMissing stands only when the status carries a stored
		// report (job.ReportExpected) and the report.json the collect
		// could not find is the one the state is expected to keep; the
		// read writes nothing.
		ReportMissing bool `json:"report_missing,omitempty"`
	}{ID: snap.State.ID, Status: snap.State.Status, ReportMissing: job.ReportExpected(snap.State.Status)}
	raw, err := json.Marshal(line)
	if err != nil {
		platform.Die("job collect: cannot render status: "+err.Error(), 2)
	}
	_, _ = fmt.Fprintln(platform.Stdout, string(raw))
}

// jobVerifyArtefatos recomputes the SHA-256 of every artefatos[].path
// (relative paths are relative to the job directory) and exits 16 when one
// differs or is missing.
func jobVerifyArtefatos(dir string, raw []byte) int {
	var doc struct {
		Artefatos []struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		} `json:"artefatos"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		platform.Die("job collect: report.json unreadable: "+err.Error(), 2)
	}
	for _, artefato := range doc.Artefatos {
		path := artefato.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			platform.Die("job collect: artefato "+artefato.Path+" changed or missing", 16)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != artefato.SHA256 {
			platform.Die("job collect: artefato "+artefato.Path+" changed or missing", 16)
		}
	}
	return 0
}

// jobReadBody reads a body from the file named path, or from stdin when
// path is "-"; at most cap+1 bytes are read and going over the cap dies
// with exit 2.
func jobReadBody(sub, what, path string, cap int, limit string) []byte {
	if path == "-" {
		data, err := io.ReadAll(io.LimitReader(jobStdin, int64(cap)+1))
		if err != nil {
			platform.Die("job "+sub+": cannot read "+what+" from stdin: "+err.Error(), 2)
		}
		if len(data) > cap {
			platform.Die("job "+sub+": "+what+" exceeds "+limit, 2)
		}
		return data
	}
	f, err := os.Open(path)
	if err != nil {
		platform.Die("job "+sub+": cannot read "+what+" from "+path+": "+err.Error(), 2)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(cap)+1))
	if err != nil {
		platform.Die("job "+sub+": cannot read "+what+" from "+path+": "+err.Error(), 2)
	}
	if len(data) > cap {
		platform.Die("job "+sub+": "+what+" exceeds "+limit, 2)
	}
	return data
}

// jobAck acknowledges the decisions up to --upto (a no-op when lower or
// equal) and prints the values after the call, from a fresh snapshot.
func jobAck(args []string, env platform.Env, cwd string) int {
	vals, _, pos := parseJobFlags("ack", args)
	if len(pos) != 0 {
		platform.Die("job ack: unexpected argument '"+pos[0]+"'", 2)
	}
	id := jobRequiredID("ack", vals)
	raw, ok := vals["--upto"]
	if !ok {
		platform.Die("job ack: missing --upto", 2)
	}
	upto, err := strconv.Atoi(raw)
	if err != nil || upto < 1 {
		platform.Die("job ack: --upto must be an integer of at least 1", 2)
	}
	store, code := jobStoreOrNotFound("ack", id, env, cwd)
	if code != 0 {
		return code
	}
	if _, err := store.Ack(id, upto); err != nil {
		dieJob("ack", err)
	}
	snap, err := store.Snapshot(id)
	if err != nil {
		dieJob("ack", err)
	}
	pending := snap.Pending
	if pending == nil {
		pending = []int{}
	}
	line, err := json.Marshal(struct {
		ID                 string `json:"id"`
		DecisionsAckedSeq  int    `json:"decisions_acked_seq"`
		DecisionsPendentes []int  `json:"decisions_pendentes"`
	}{ID: snap.State.ID, DecisionsAckedSeq: snap.State.DecisionsAckedSeq, DecisionsPendentes: pending})
	if err != nil {
		platform.Die("job ack: cannot render ack: "+err.Error(), 2)
	}
	_, _ = fmt.Fprintln(platform.Stdout, string(line))
	return 0
}

// jobClose moves a terminal or collected job to closed. Pending decisions
// without --force print the state with the seqs listed and exit 24; close
// is state-only in this phase (no Herdr, process or workspace release).
func jobClose(args []string, env platform.Env, cwd string) int {
	vals, bare, pos := parseJobFlags("close", args)
	if len(pos) != 0 {
		platform.Die("job close: unexpected argument '"+pos[0]+"'", 2)
	}
	id := jobRequiredID("close", vals)
	store, code := jobStoreOrNotFound("close", id, env, cwd)
	if code != 0 {
		return code
	}
	if _, err := store.Close(id, bare["--force"]); err != nil {
		if jobHasCode(err, job.ExitUnacked) {
			snap, snapErr := store.Snapshot(id)
			if snapErr != nil {
				dieJob("close", snapErr)
			}
			printJobCloseLine(snap.State.ID, snap.State.Status, snap.Pending)
			return job.ExitUnacked
		}
		dieJob("close", err)
	}
	snap, err := store.Snapshot(id)
	if err != nil {
		dieJob("close", err)
	}
	printJobCloseLine(snap.State.ID, snap.State.Status, snap.Pending)
	return 0
}

// printJobCloseLine is the close JSON line (contract field order).
func printJobCloseLine(id, status string, pending []int) {
	if pending == nil {
		pending = []int{}
	}
	line, err := json.Marshal(struct {
		ID                 string `json:"id"`
		Status             string `json:"status"`
		DecisionsPendentes []int  `json:"decisions_pendentes"`
	}{ID: id, Status: status, DecisionsPendentes: pending})
	if err != nil {
		platform.Die("job close: cannot render close: "+err.Error(), 2)
	}
	_, _ = fmt.Fprintln(platform.Stdout, string(line))
}

// jobParseRefs splits the comma-separated --refs items into key=value pairs;
// an item without "=" or with an empty key is exit 2. Unknown keys are
// refused by the package.
func jobParseRefs(sub string, vals map[string]string) map[string]string {
	raw, ok := vals["--refs"]
	if !ok {
		return nil
	}
	refs := map[string]string{}
	for _, item := range strings.Split(raw, ",") {
		key, value, hasEq := strings.Cut(item, "=")
		if !hasEq || key == "" {
			platform.Die("job "+sub+": --refs items must be key=value pairs", 2)
		}
		refs[key] = value
	}
	return refs
}

// jobNote appends one orchestrator event and prints the stored event as one
// JSON line. The escopo/decision rules stay in the package (appendEvent).
func jobNote(args []string, env platform.Env, cwd string) int {
	vals, _, pos := parseJobFlags("note", args)
	id := jobRequiredID("note", vals)
	tipo, ok := vals["--tipo"]
	if !ok {
		platform.Die("job note: missing --tipo", 2)
	}
	if !jobNoteTypes[tipo] {
		platform.Die("job note: --tipo must be checkpoint, question, decision, or note", 2)
	}
	if len(pos) != 1 {
		platform.Die("job note: expected exactly one summary argument (or -)", 2)
	}
	var resumo string
	if pos[0] == "-" {
		resumo = string(jobReadBody("note", "summary", "-", jobNoteBodyLimit, "16 KiB"))
	} else {
		resumo = pos[0]
	}
	refs := jobParseRefs("note", vals)
	store, code := jobStoreOrNotFound("note", id, env, cwd)
	if code != 0 {
		return code
	}
	event, err := store.Note(id, job.EventIn{Tipo: tipo, Resumo: resumo, Escopo: vals["--escopo"], Motivo: vals["--motivo"], Refs: refs})
	if err != nil {
		dieJob("note", err)
	}
	line, err := json.Marshal(event)
	if err != nil {
		platform.Die("job note: cannot render event: "+err.Error(), 2)
	}
	_, _ = fmt.Fprintln(platform.Stdout, string(line))
	return 0
}

// jobSupervise runs the job supervisor: it requires the job's Herdr
// workspace (HERDR_ENV=1), locates the job — in every checkout, or only
// in the named one with --repo, which is how job start launches it —
// builds the supervisor with this executable as the team command runner
// and the job friction file as its friction log, prints the job's status
// line and exits with the outcome code.
func jobSupervise(args []string, env platform.Env, launch job.LaunchLocators) int {
	vals, _, pos := parseJobFlags("supervise", args)
	if len(pos) > 1 {
		platform.Die("job supervise: unexpected argument '"+pos[1]+"'", 2)
	}
	id := jobRequiredID("supervise", vals)
	repo, hasRepo := vals["--repo"]
	if hasRepo && !jobRepoPattern.MatchString(repo) {
		platform.Die("job supervise: invalid --repo '"+repo+"'", 2)
	}
	if env.Get("HERDR_ENV") != "1" {
		platform.Die("job supervise: run it inside the job's Herdr workspace", 2)
	}
	// The job start's private launch context (locator.go): absent, the
	// supervisor resolves everything from its own environment as before;
	// present but unusable, it stops here without locating any store, so
	// it never runs a job found through another selection; usable, every
	// user-configuration read below comes from that selection only.
	if launch.Present && launch.Selection.IsZero() {
		platform.Die("job supervise: the private launch context is unusable", 2)
	}
	sel := launch.Selection
	var store *job.Store
	var err error
	if hasRepo {
		// With --repo the lookup reads only the named checkout, never the
		// rest of job_repos_root: the scan's per-checkout cost is off the
		// startup confirmation path (DJA-348).
		store, err = locateJobInRepo(env, id, repo, sel)
	} else {
		store, err = locateJob(env, id, sel)
	}
	if err != nil {
		dieJob("supervise", err)
	}
	if store == nil {
		printJobNotFound()
		return job.ExitNotFound
	}
	// The friction file of the job; the callback writes in the existing
	// friction format.
	friction := func(message string) {
		core.Warn(message, filepath.Join(store.Root, "jobs", id, "friction.log"))
	}
	launcher := platform.LauncherPath(env)
	sup, err := job.NewSupervisor(store, id, env, launcher, friction)
	if err != nil {
		dieJob("supervise", err)
	}
	// The machine's wake hook: an empty job_wake_cmd leaves Wake nil.
	machine, err := job.LoadMachineFor(platform.Current(), env, sel)
	if err != nil {
		dieJob("supervise", err)
	}
	if machine.WakeCmd != "" {
		sup.Wake = &job.WakeHook{Cmd: machine.WakeCmd, JobID: id, Env: env, Friction: friction}
	}
	if sel.IsZero() {
		// Without the private launch context: the private startup
		// confirmation the job start waits for, right before the
		// supervisor runs. It never refuses: a launch this process cannot
		// confirm leaves the supervisor running as before, and the reason
		// goes to the job friction log.
		if confirmed, reason := job.ConfirmLaunch(store, id, env); !confirmed && reason != "" {
			friction("job: the supervisor did not confirm the launch: " + reason)
		}
	} else {
		// With it, the supervisor runs only bound to its launch
		// (internal/job/managed.go): it confirmed the pending launch, or
		// took over a confirmed one whose supervisor is proven gone.
		// Unbound, it does nothing: no lane, worker, git, wake or
		// workspace step; it prints the job status and exits 4. The team
		// commands prove their parent is this executable, so a launcher
		// that is another file (SOHO_BIN) is refused before binding:
		// they would otherwise run with the server's configuration.
		bound, reason := false, "the team command executable is not this supervisor's"
		if job.SameExecutable(launcher) {
			bound, reason = job.BindLaunch(store, id, env, sel)
		}
		if !bound {
			friction("job: the supervisor is not bound to its launch: " + reason)
			snap, err := store.Snapshot(id)
			if err != nil {
				dieJob("supervise", err)
			}
			printJobStatusLine(snap)
			return job.ExitHerdr
		}
		sup.UseLaunchSelection(sel)
	}
	exit, runErr := sup.Run()
	if runErr != nil {
		friction("job: the job supervisor stopped with an error: " + runErr.Error())
	}
	snap, err := store.Snapshot(id)
	if err != nil {
		dieJob("supervise", err)
	}
	printJobStatusLine(snap)
	return exit
}

// jobControlStatus prints the fresh snapshot status line after a control
// request: the store and id are known, so only a read failure dies.
func jobControlStatus(sub string, store *job.Store, id string) {
	snap, err := store.Snapshot(id)
	if err != nil {
		dieJob(sub, err)
	}
	printJobStatusLine(snap)
}

// jobAmend queues the amendment body (a file or -) as a control request and
// prints the status line. The supervisor delivers it later and appends
// amend_received; no event is written here and no Herdr call is made.
func jobAmend(args []string, env platform.Env, cwd string) int {
	vals, _, pos := parseJobFlags("amend", args)
	if len(pos) > 1 {
		platform.Die("job amend: unexpected argument '"+pos[1]+"'", 2)
	}
	id := jobRequiredID("amend", vals)
	if len(pos) != 1 {
		platform.Die("job amend: missing body (a file or -)", 2)
	}
	store, code := jobStoreOrNotFound("amend", id, env, cwd)
	if code != 0 {
		return code
	}
	body := jobReadBody("amend", "body", pos[0], jobAmendBodyLimit, "64 KiB")
	if _, err := store.RequestAmend(id, body); err != nil {
		dieJob("amend", err)
	}
	jobControlStatus("amend", store, id)
	return 0
}

// jobSend queues the note body (a file or -) as a control request and prints
// the status line. Like amend, it writes no event and makes no Herdr call.
func jobSend(args []string, env platform.Env, cwd string) int {
	vals, _, pos := parseJobFlags("send", args)
	if len(pos) > 1 {
		platform.Die("job send: unexpected argument '"+pos[1]+"'", 2)
	}
	id := jobRequiredID("send", vals)
	if len(pos) != 1 {
		platform.Die("job send: missing body (a file or -)", 2)
	}
	store, code := jobStoreOrNotFound("send", id, env, cwd)
	if code != 0 {
		return code
	}
	body := jobReadBody("send", "body", pos[0], jobSendBodyLimit, "16 KiB")
	if _, err := store.RequestSend(id, body); err != nil {
		dieJob("send", err)
	}
	jobControlStatus("send", store, id)
	return 0
}

// jobCancel queues the control cancel request; the default grace is 120
// seconds when --grace is absent. A no-op cancel still prints the status
// line and exits 0.
func jobCancel(args []string, env platform.Env, cwd string) int {
	vals, _, pos := parseJobFlags("cancel", args)
	if len(pos) != 0 {
		platform.Die("job cancel: unexpected argument '"+pos[0]+"'", 2)
	}
	id := jobRequiredID("cancel", vals)
	grace := 120
	if raw, ok := vals["--grace"]; ok {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > 3600 {
			platform.Die("job cancel: --grace must be an integer from 0 to 3600", 2)
		}
		grace = n
	}
	store, code := jobStoreOrNotFound("cancel", id, env, cwd)
	if code != 0 {
		return code
	}
	// A crashed start (accepted or preparing with no supervisor) has
	// nothing left to cancel: the recovery ends the job failed before
	// anything is queued. A live starter keeps the job: the recovery
	// declines and the cancel queues as usual.
	if snap, err := store.Snapshot(id); err == nil && (snap.State.Status == job.StatusAccepted || snap.State.Status == job.StatusPreparing) {
		friction := func(message string) {
			core.Warn(message, filepath.Join(store.Root, "jobs", id, "friction.log"))
		}
		rawFriction := func(message string) {
			core.RecordFrictionError(message, job.ExitHerdr, filepath.Join(store.Root, "jobs", id, "friction.log"))
		}
		if _, recovered, err := job.NewStartRecovery(env, friction, rawFriction)(store, id); err != nil {
			dieJob("cancel", err)
		} else if recovered {
			snap, err := store.Snapshot(id)
			if err != nil {
				dieJob("cancel", err)
			}
			printJobStatusLine(snap)
			return 0
		}
	}
	if _, err := store.RequestCancel(id, grace); err != nil {
		dieJob("cancel", err)
	}
	jobControlStatus("cancel", store, id)
	return 0
}

// jobCheckpoint queues the immediate-push marker and prints the status line.
// A no-op checkpoint still exits 0.
func jobCheckpoint(args []string, env platform.Env, cwd string) int {
	vals, _, pos := parseJobFlags("checkpoint", args)
	if len(pos) != 0 {
		platform.Die("job checkpoint: unexpected argument '"+pos[0]+"'", 2)
	}
	id := jobRequiredID("checkpoint", vals)
	store, code := jobStoreOrNotFound("checkpoint", id, env, cwd)
	if code != 0 {
		return code
	}
	if _, err := store.RequestCheckpoint(id); err != nil {
		dieJob("checkpoint", err)
	}
	jobControlStatus("checkpoint", store, id)
	return 0
}

// jobStart validates the start inputs in the contract order and, with
// --dry-run, prints the resolved plan without writing any job state; without
// it, it refuses after the same validation (phase 2 prepares the job).
func jobStart(args []string, env platform.Env, cwd string) int {
	vals, bare, pos := parseJobFlags("start", args)
	if len(pos) != 0 {
		platform.Die("job start: unexpected argument '"+pos[0]+"'", 2)
	}
	id := jobRequiredID("start", vals)
	repo, ok := vals["--repo"]
	if !ok {
		platform.Die("job start: missing --repo", 2)
	}
	if !jobRepoPattern.MatchString(repo) {
		platform.Die("job start: invalid --repo '"+repo+"'", 2)
	}
	org := repo[:strings.IndexByte(repo, '/')]
	repoName := repo[strings.IndexByte(repo, '/')+1:]
	base := ""
	if raw, ok := vals["--base"]; ok {
		if !jobBasePattern.MatchString(raw) || strings.Contains(raw, "..") {
			platform.Die("job start: invalid --base '"+raw+"'", 2)
		}
		base = raw
	}
	modoFlag := ""
	if raw, ok := vals["--mode"]; ok {
		if raw != "worktree" && raw != "workspace" {
			platform.Die("job start: invalid --mode '"+raw+"'", 2)
		}
		modoFlag = raw
	}
	timeout := 0
	if raw, ok := vals["--timeout"]; ok {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 1440 {
			platform.Die("job start: --timeout must be an integer from 1 to 1440", 2)
		}
		timeout = n
	}
	briefPath, ok := vals["--brief"]
	if !ok {
		platform.Die("job start: missing --brief", 2)
	}
	raw := jobReadBody("start", "brief", briefPath, job.MaxBriefBytes, "256 KiB")
	info, err := job.ValidateBrief(raw, id)
	if err != nil {
		dieJob("start", err)
	}
	if info.Repo != repo {
		platform.Die("job start: brief repo '"+info.Repo+"' does not match --repo '"+repo+"'", 2)
	}
	machine, err := job.LoadMachine(platform.Current(), env)
	if err != nil {
		dieJob("start", err)
	}
	if err := machine.AllowsOrg(org); err != nil {
		dieJob("start", err)
	}
	if err := machine.LabelMatches(info.Maquina); err != nil {
		dieJob("start", err)
	}
	checkout := filepath.Join(machine.ReposRoot, org, repoName)
	team, err := job.ResolveTeam(platform.Current(), env, checkout, org, repoName, info.Equipe)
	if err != nil {
		dieJob("start", err)
	}
	// The rendered brief lints against the checkout config when the checkout
	// exists, else the cwd config; the temp file is removed before returning.
	tmp, err := os.CreateTemp("", "soho-job-brief-*.md")
	if err != nil {
		platform.Die("job start: cannot create temp brief: "+err.Error(), 2)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(info.Markdown); err != nil {
		tmp.Close()
		platform.Die("job start: cannot write temp brief: "+err.Error(), 2)
	}
	if err := tmp.Close(); err != nil {
		platform.Die("job start: cannot write temp brief: "+err.Error(), 2)
	}
	cfg := core.LoadConfig(env, cwd)
	if st, statErr := os.Stat(checkout); statErr == nil && st.IsDir() {
		cfg = core.LoadConfig(env, checkout)
	}
	if err := job.LintBrief(tmpPath, &cfg, env); err != nil {
		dieJob("start", err)
	}
	if timeout == 0 {
		timeout = machine.TimeoutMin
	}
	resolvedBase := info.Base
	if base != "" {
		resolvedBase = base
	}
	modo := "worktree"
	if info.Modo != "" {
		modo = info.Modo
	}
	if modoFlag != "" {
		modo = modoFlag
	}
	if !bare["--dry-run"] {
		selfExe := platform.LauncherPath(env)
		stateRootOf := func(checkout string) string {
			cfg := core.LoadConfig(env, checkout)
			return core.StateRootPath(&cfg, env, checkout)
		}
		// The friction file of the job; the callback is built once the id
		// is valid and writes in the existing friction format.
		friction := func(message string) {
			core.Warn(message, filepath.Join(stateRootOf(checkout), "jobs", id, "friction.log"))
		}
		// The raw friction sink: the raw create envelope reaches the job
		// friction log as an error line of the start's own exit code, and
		// never stderr (the start ends with exit 4 there when it fires).
		rawFriction := func(message string) {
			core.RecordFrictionError(message, job.ExitHerdr, filepath.Join(stateRootOf(checkout), "jobs", id, "friction.log"))
		}
		store, err := locateJob(env, id, core.UserSelection{})
		if err != nil {
			dieJob("start", err)
		}
		if store != nil {
			// The id is already recorded: a duplicate returns the status
			// line with duplicate_of, a different brief dies with 20.
			st, err := store.Start(id, raw)
			if err != nil {
				dieJob("start", err)
			}
			// A duplicate still accepted or preparing is a crashed start
			// (no supervisor will run it): the recovery ends it failed
			// before the status line is printed.
			if st.DuplicateOf != "" && (st.Status == job.StatusAccepted || st.Status == job.StatusPreparing) {
				if _, _, err := job.NewStartRecovery(env, friction, rawFriction)(store, id); err != nil {
					dieJob("start", err)
				}
			}
			snap, err := store.Snapshot(id)
			if err != nil {
				dieJob("start", err)
			}
			snap.State.DuplicateOf = st.DuplicateOf
			printJobStatusLine(snap)
			return 0
		}
		starter := job.NewStarter(env, machine, selfExe, stateRootOf, friction, rawFriction)
		st, store, err := starter.Start(job.StartRequest{
			ID: id, Org: org, Repo: repoName, Base: resolvedBase, Mode: modo,
			Brief: raw, TimeoutMin: timeout, Team: team,
		})
		if err != nil {
			// The job's status line when the store and state exist (the
			// failure wrote it), then the error on stderr with its code.
			if store != nil {
				if snap, err := store.Snapshot(id); err == nil {
					printJobStatusLine(snap)
				}
			}
			dieJob("start", err)
		}
		snap, err := store.Snapshot(id)
		if err != nil {
			dieJob("start", err)
		}
		snap.State.DuplicateOf = st.DuplicateOf
		printJobStatusLine(snap)
		return 0
	}
	var baseOut *string
	if resolvedBase != "" {
		baseOut = &resolvedBase
	}
	override := team.OverrideKeys
	if override == nil {
		override = []string{}
	}
	line, err := json.Marshal(struct {
		Status     string  `json:"status"`
		ID         string  `json:"id"`
		Repo       string  `json:"repo"`
		Base       *string `json:"base"`
		Modo       string  `json:"modo"`
		TimeoutMin int     `json:"timeout_min"`
		BriefSHA   string  `json:"brief_sha256"`
		Equipe     struct {
			Fonte         string   `json:"fonte"`
			OverrideBrief []string `json:"override_brief"`
		} `json:"equipe"`
	}{
		Status: "dry_run", ID: id, Repo: repo, Base: baseOut, Modo: modo,
		TimeoutMin: timeout, BriefSHA: info.Hash,
		Equipe: struct {
			Fonte         string   `json:"fonte"`
			OverrideBrief []string `json:"override_brief"`
		}{Fonte: team.Source, OverrideBrief: override},
	})
	if err != nil {
		platform.Die("job start: cannot render dry run: "+err.Error(), 2)
	}
	_, _ = fmt.Fprintln(platform.Stdout, string(line))
	return 0
}

func jobList(args []string, env platform.Env) int {
	vals, _, pos := parseJobFlags("list", args)
	if len(pos) != 0 {
		platform.Die("job list: unexpected argument '"+pos[0]+"'", 2)
	}
	filter := ""
	if raw, ok := vals["--state"]; ok {
		if !jobLifecycleStates[raw] {
			platform.Die("job list: invalid --state '"+raw+"'", 2)
		}
		filter = raw
	}
	stores, err := allJobStores(env, core.UserSelection{})
	if err != nil {
		dieJob("list", err)
	}
	counts := map[string]int{}
	total := 0
	for _, store := range stores {
		entries, err := os.ReadDir(filepath.Join(store.Root, "jobs"))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			dieJob("list", err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			snap, err := store.Snapshot(entry.Name())
			if err != nil {
				continue
			}
			if filter != "" && snap.State.Status != filter {
				continue
			}
			counts[snap.State.Status]++
			total++
		}
	}
	line, err := json.Marshal(struct {
		Total     int            `json:"total"`
		PorEstado map[string]int `json:"por_estado"`
	}{Total: total, PorEstado: counts})
	if err != nil {
		platform.Die("job list: cannot render counts: "+err.Error(), 2)
	}
	_, _ = fmt.Fprintln(platform.Stdout, string(line))
	return 0
}

// jobReadSubs are the read-only job subcommands; every other subcommand
// is a writing one and never passes the NOWRITE allowlist.
var jobReadSubs = map[string]bool{
	"status": true, "wait": true, "events": true, "collect": true, "list": true,
}

// nowriteJobRead accepts the read-only job invocations by shape (values are
// not validated here): status/wait/events/collect with --id and their flags,
// list with an optional --state.
func nowriteJobRead(argv []string) bool {
	if len(argv) == 0 || !jobReadSubs[argv[0]] {
		return false
	}
	spec := jobFlagSpec[argv[0]]
	valued := map[string]bool{}
	for _, flag := range spec.valued {
		valued[flag] = true
	}
	seen := map[string]bool{}
	sawID := false
	for i := 1; i < len(argv); i++ {
		token := argv[i]
		name, value, hasValue := strings.Cut(token, "=")
		if !strings.HasPrefix(name, "--") {
			return false
		}
		if hasValue {
			if !valued[name] || value == "" || strings.HasPrefix(value, "-") {
				return false
			}
		} else if valued[name] {
			if i+1 >= len(argv) || strings.HasPrefix(argv[i+1], "-") {
				return false
			}
			i++
		} else if !flagIn(spec.bare, name) {
			return false
		}
		if seen[name] {
			return false
		}
		seen[name] = true
		if name == "--id" {
			sawID = true
		}
	}
	if argv[0] == "list" {
		return true
	}
	return sawID
}
