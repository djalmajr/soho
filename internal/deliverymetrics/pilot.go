// Package deliverymetrics measures calendar time from delivery evidence.
package deliverymetrics

import (
	"fmt"
	"slices"
	"time"
)

type State string

const (
	Open      State = "open"
	Delivered State = "delivered"
	Canceled  State = "canceled"
	Reopened  State = "reopened"
)

// Record links one delivery episode to its demand, change and publication.
// Identifiers can be URLs or stable identifiers in the source system.
type Record struct {
	Demand, Change, Publication string
	State                       State
	Landmarks                   Landmarks
	Activity                    []Interval
	CI                          []CIReceipt
	Gaps                        []string
	ValidationPlan              *ValidationPlan
	Intake                      *Assessment
	Revisions                   []Assessment
}

// Evidence records a verified instant and its source. A nil landmark is missing.
type Evidence struct {
	At         time.Time
	Source     string
	Resolution time.Duration // Optional precision of the recorded instant.
}

type Landmarks struct {
	Request, Start, Merge, Released *Evidence
}

type IntervalKind string

const (
	Implementation IntervalKind = "implementation"
	Review         IntervalKind = "review"
	Validation     IntervalKind = "validation"
	Rework         IntervalKind = "rework"
	Waiting        IntervalKind = "waiting"
)

// Interval is a half-open calendar window. Receipt is a stable, source-qualified
// event identifier. Cause is optional and requires evidence in that receipt.
type Interval struct {
	Receipt    string
	Kind       IntervalKind
	Start, End time.Time
	Cause      string
}

// CIReceipt identifies a job within a specific run attempt. A zero timestamp
// means that event was unobserved. Head and Outcome preserve supplied validation evidence.
type CIReceipt struct {
	Source, Run, Job           string
	Attempt                    int
	Queued, Started, Completed time.Time
	Head, Outcome              string
}

type ValidationPlan struct {
	Source                       string
	Scenarios, Layers, Platforms []string
}

// Assessment retains the basis of a profile separately from measured timestamps.
// Retrospective marks a reconstructed intake profile. Revisions carry a reason.
type Assessment struct {
	RubricVersion  string
	Retrospective  bool
	Evidence       string
	KnownAtIntake  string
	RevisionReason string
	Flows          []Flow
	Technical      TechnicalProfile
	Uncertainty    *Rating
}

type Flow struct {
	Name                          string
	Entries, Exits, Reads, Writes int
}

type TechnicalProfile struct {
	Interfaces, State, Risk, Validation *Rating
}

// Rating is one rubric dimension on a 0–2 scale. A nil rating means unknown.
type Rating struct {
	Level     int
	Rationale string
}

// Metrics uses nil for unavailable landmark spans. Observed window totals
// are accompanied by counts so zero observations are distinguishable from zero time.
type Metrics struct {
	Lead, Cycle, RequestToStart, StartToMerge, MergeToRelease *time.Duration
	Unclassified                                              *time.Duration
	Active, Waiting, CIQueue, CIRuntime                       time.Duration
	Coverage                                                  Coverage
}

type Coverage struct {
	Landmarks, ActivityWindows, WaitingWindows int
	CIJobs, CIQueueWindows, CIRuntimeWindows   int
}

// Measure computes elapsed and observed calendar durations. Duplicate receipts
// are counted once, conflicting receipts are rejected and missing times stay unknown.
func Measure(record Record) (Metrics, error) {
	var result Metrics
	for _, mark := range []*Evidence{record.Landmarks.Request, record.Landmarks.Start, record.Landmarks.Merge, record.Landmarks.Released} {
		if mark != nil && (mark.At.IsZero() || mark.Source == "") {
			return Metrics{}, fmt.Errorf("landmark source and timestamp are required")
		}
		if mark != nil {
			result.Coverage.Landmarks++
		}
	}
	request, start := landmarkTime(record.Landmarks.Request), landmarkTime(record.Landmarks.Start)
	merge, release := landmarkTime(record.Landmarks.Merge), landmarkTime(record.Landmarks.Released)
	if !chronological(request, start, merge, release) {
		return Metrics{}, fmt.Errorf("landmark order is inconsistent")
	}

	var active, waiting, ciQueue, ciRuntime []timeWindow
	intervals := make(map[string]Interval)
	for _, item := range record.Activity {
		if item.Receipt == "" || item.Start.IsZero() || item.End.IsZero() || !item.Start.Before(item.End) {
			return Metrics{}, fmt.Errorf("interval order or receipt is invalid: %q", item.Receipt)
		}
		item.Start, item.End = item.Start.UTC(), item.End.UTC()
		if previous, ok := intervals[item.Receipt]; ok {
			if previous != item {
				return Metrics{}, fmt.Errorf("conflicting interval receipt %q", item.Receipt)
			}
			continue
		}
		intervals[item.Receipt] = item
		window := timeWindow{item.Start, item.End}
		switch item.Kind {
		case Implementation, Review, Validation, Rework:
			active = append(active, window)
			result.Coverage.ActivityWindows++
		case Waiting:
			waiting = append(waiting, window)
			result.Coverage.WaitingWindows++
		default:
			return Metrics{}, fmt.Errorf("unknown interval kind %q", item.Kind)
		}
	}

	type ciID struct {
		source, run, job string
		attempt          int
	}
	receipts := make(map[ciID]CIReceipt)
	for _, job := range record.CI {
		id := ciID{job.Source, job.Run, job.Job, job.Attempt}
		if id.source == "" || id.run == "" || id.job == "" || id.attempt < 1 {
			return Metrics{}, fmt.Errorf("CI identity requires source, run, job and positive attempt")
		}
		if !chronological(job.Queued, job.Started, job.Completed) {
			return Metrics{}, fmt.Errorf("CI timestamp order is inconsistent for %+v", id)
		}
		job.Queued, job.Started, job.Completed = job.Queued.UTC(), job.Started.UTC(), job.Completed.UTC()
		if previous, ok := receipts[id]; ok {
			if previous != job {
				return Metrics{}, fmt.Errorf("conflicting CI receipt for %+v", id)
			}
			continue
		}
		receipts[id] = job
		result.Coverage.CIJobs++
		if !job.Queued.IsZero() && !job.Started.IsZero() {
			ciQueue = append(ciQueue, timeWindow{job.Queued, job.Started})
			result.Coverage.CIQueueWindows++
		}
		if !job.Started.IsZero() && !job.Completed.IsZero() {
			ciRuntime = append(ciRuntime, timeWindow{job.Started, job.Completed})
			result.Coverage.CIRuntimeWindows++
		}
	}

	active = append(active, ciRuntime...)
	waiting = append(waiting, ciQueue...)
	result.CIQueue, result.CIRuntime = unionDuration(ciQueue), unionDuration(ciRuntime)
	result.Active, result.Waiting = unionDuration(active), unionDuration(waiting)
	if record.State == Reopened || record.State == Canceled {
		return result, nil
	}
	result.RequestToStart = elapsed(request, start)
	result.StartToMerge = elapsed(start, merge)
	if record.State != Delivered {
		return result, nil
	}
	result.Lead = elapsed(request, release)
	result.Cycle = elapsed(start, release)
	result.MergeToRelease = elapsed(merge, release)
	if result.Cycle != nil {
		covered := make([]timeWindow, 0, len(active)+len(waiting))
		for _, window := range append(active, waiting...) {
			clipped := window
			if start.After(clipped.start) {
				clipped.start = start
			}
			if release.Before(clipped.end) {
				clipped.end = release
			}
			if clipped.start.Before(clipped.end) {
				covered = append(covered, clipped)
			}
		}
		unclassified := *result.Cycle - unionDuration(covered)
		result.Unclassified = &unclassified
	}
	return result, nil
}

func landmarkTime(mark *Evidence) time.Time {
	if mark == nil {
		return time.Time{}
	}
	return mark.At.UTC()
}

func chronological(marks ...time.Time) bool {
	var previous time.Time
	for _, mark := range marks {
		if mark.IsZero() {
			continue
		}
		if !previous.IsZero() && mark.Before(previous) {
			return false
		}
		previous = mark
	}
	return true
}

func elapsed(start, end time.Time) *time.Duration {
	if start.IsZero() || end.IsZero() {
		return nil
	}
	duration := end.Sub(start)
	return &duration
}

type timeWindow struct {
	start, end time.Time
}

// unionDuration sorts and coalesces parallel windows before summing their lengths.
func unionDuration(windows []timeWindow) time.Duration {
	if len(windows) == 0 {
		return 0
	}
	slices.SortFunc(windows, func(a, b timeWindow) int { return a.start.Compare(b.start) })
	current := windows[0]
	var total time.Duration
	for _, next := range windows[1:] {
		if next.start.After(current.end) {
			total += current.end.Sub(current.start)
			current = next
		} else if next.end.After(current.end) {
			current.end = next.end
		}
	}
	return total + current.end.Sub(current.start)
}
