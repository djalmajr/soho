package deliverymetrics

import (
	"strings"
	"testing"
	"time"
)

func TestMeasureDelivery(t *testing.T) {
	record := Record{
		State: Delivered,
		Landmarks: Landmarks{
			Request: mark(0), Start: mark(2), Merge: mark(8), Released: mark(10),
		},
		Activity: []Interval{
			window("implement", Implementation, 2, 4),
			window("review", Review, 5, 6),
			window("decision", Waiting, 6, 7),
		},
		CI: []CIReceipt{ci(1, 4, 5, 7)},
	}
	got, err := Measure(record)
	if err != nil {
		t.Fatal(err)
	}
	assertDuration(t, "lead", got.Lead, 10*time.Hour)
	assertDuration(t, "cycle", got.Cycle, 8*time.Hour)
	assertDuration(t, "request to start", got.RequestToStart, 2*time.Hour)
	assertDuration(t, "start to merge", got.StartToMerge, 6*time.Hour)
	assertDuration(t, "merge to release", got.MergeToRelease, 2*time.Hour)
	assertDuration(t, "unclassified", got.Unclassified, 3*time.Hour)
	if got.Active != 4*time.Hour || got.Waiting != 2*time.Hour ||
		got.CIQueue != time.Hour || got.CIRuntime != 2*time.Hour {
		t.Fatalf("unexpected duration totals: %+v", got)
	}
	wantCoverage := Coverage{Landmarks: 4, ActivityWindows: 2, WaitingWindows: 1, CIJobs: 1, CIQueueWindows: 1, CIRuntimeWindows: 1}
	if got.Coverage != wantCoverage {
		t.Fatalf("coverage = %+v, want %+v", got.Coverage, wantCoverage)
	}
}

func TestMeasureParallelAndRepeatedReceipts(t *testing.T) {
	first := ci(1, 1, 2, 5)
	retry := ci(2, 3, 4, 8)
	repeated := retry
	repeated.Completed = repeated.Completed.In(time.FixedZone("same instant", -3*60*60))
	implement := window("implementation", Implementation, 1, 6)
	record := Record{
		State:     Delivered,
		Landmarks: Landmarks{Start: mark(1), Released: mark(9)},
		Activity:  []Interval{window("revision", Rework, 5, 7), implement, implement},
		CI:        []CIReceipt{retry, repeated, first},
	}
	got, err := Measure(record)
	if err != nil {
		t.Fatal(err)
	}
	if got.Active != 7*time.Hour || got.CIQueue != 2*time.Hour || got.CIRuntime != 6*time.Hour || got.Waiting != 2*time.Hour {
		t.Fatalf("parallel work or repeated receipts inflated durations: %+v", got)
	}
	assertDuration(t, "unclassified", got.Unclassified, time.Hour)
	if got.Coverage.ActivityWindows != 2 || got.Coverage.CIJobs != 2 ||
		got.Coverage.CIQueueWindows != 2 || got.Coverage.CIRuntimeWindows != 2 {
		t.Fatalf("parallel work or repeated receipts changed coverage: %+v", got.Coverage)
	}
}

func TestMeasureMissingMarksAndIncompleteCI(t *testing.T) {
	receipt := ci(1, 3, 4, 6)
	receipt.Started = time.Time{}
	record := Record{
		State:     Delivered,
		Landmarks: Landmarks{Request: mark(0), Merge: mark(7), Released: mark(9)},
		Activity:  []Interval{window("review", Review, 3, 5)},
		CI:        []CIReceipt{receipt},
	}
	got, err := Measure(record)
	if err != nil {
		t.Fatal(err)
	}
	assertDuration(t, "lead", got.Lead, 9*time.Hour)
	assertDuration(t, "merge to release", got.MergeToRelease, 2*time.Hour)
	if got.Cycle != nil || got.StartToMerge != nil || got.RequestToStart != nil || got.Unclassified != nil {
		t.Fatalf("derived durations from missing start: %+v", got)
	}
	if got.Active != 2*time.Hour || got.CIQueue != 0 || got.CIRuntime != 0 ||
		got.Coverage != (Coverage{Landmarks: 3, ActivityWindows: 1, CIJobs: 1}) {
		t.Fatalf("lost observed work or invented CI coverage: %+v", got)
	}
}

func TestMeasurePreservesZeroAndClipsUnclassifiedWindow(t *testing.T) {
	got, err := Measure(Record{
		State:     Delivered,
		Landmarks: Landmarks{Request: mark(0), Start: mark(0), Released: mark(5)},
		Activity:  []Interval{window("later", Review, 4, 7)},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertDuration(t, "same instant", got.RequestToStart, 0)
	assertDuration(t, "unclassified", got.Unclassified, 4*time.Hour)
	if got.Active != 3*time.Hour {
		t.Fatalf("observed active = %s, want 3h", got.Active)
	}
}

func TestMeasureAcceptsOverlappingLandmarkPrecision(t *testing.T) {
	request, start, merge, release := mark(1), mark(1), mark(5), mark(5)
	merge.At = merge.At.Add(30 * time.Second)
	release.Resolution = time.Minute
	got, err := Measure(Record{
		State:     Delivered,
		Landmarks: Landmarks{Request: request, Start: start, Merge: merge, Released: release},
		Activity:  []Interval{window("review", Review, 3, 5)},
		CI:        []CIReceipt{ci(1, 1, 2, 4)},
	})
	if err != nil {
		t.Fatalf("compatible release minute rejected: %v", err)
	}
	assertDuration(t, "exact zero request to start", got.RequestToStart, 0)
	assertDuration(t, "exact start to merge", got.StartToMerge, 4*time.Hour+30*time.Second)
	if got.Lead != nil || got.Cycle != nil || got.MergeToRelease != nil || got.Unclassified != nil {
		t.Fatalf("reported durations from an uncertain release: %+v", got)
	}
	wantCoverage := Coverage{Landmarks: 4, CoarseLandmarks: 1, ActivityWindows: 1, CIJobs: 1, CIQueueWindows: 1, CIRuntimeWindows: 1}
	if got.Coverage != wantCoverage || got.Active != 3*time.Hour || got.Waiting != time.Hour ||
		got.CIQueue != time.Hour || got.CIRuntime != 2*time.Hour {
		t.Fatalf("compatible precision lost activity or CI data: %+v", got)
	}
}

func TestMeasureWithholdsSpansFromCoarseRequest(t *testing.T) {
	request := mark(1)
	request.Resolution = time.Minute
	got, err := Measure(Record{
		State:     Delivered,
		Landmarks: Landmarks{Request: request, Start: mark(2), Merge: mark(5), Released: mark(7)},
		Activity:  []Interval{window("review", Review, 3, 5)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Lead != nil || got.RequestToStart != nil || got.Coverage.Landmarks != 4 || got.Coverage.CoarseLandmarks != 1 {
		t.Fatalf("coarse request produced exact durations or lost coverage: %+v", got)
	}
	assertDuration(t, "exact cycle", got.Cycle, 5*time.Hour)
	assertDuration(t, "exact start to merge", got.StartToMerge, 3*time.Hour)
	assertDuration(t, "exact merge to release", got.MergeToRelease, 2*time.Hour)
	assertDuration(t, "unclassified", got.Unclassified, 3*time.Hour)
}

func TestMeasureRejectsImpossibleLandmarkPrecision(t *testing.T) {
	request, start, merge := mark(5), mark(5), mark(5)
	request.At = request.At.Add(90 * time.Second)
	start.At = start.At.Add(time.Minute)
	start.Resolution = time.Minute
	merge.Resolution = 75 * time.Second
	endedRelease := mark(6)
	endedRelease.At = endedRelease.At.Add(-time.Minute)
	endedRelease.Resolution = time.Minute
	negative := mark(5)
	negative.Resolution = -time.Second
	for _, tc := range []struct {
		name  string
		marks Landmarks
		want  string
	}{
		{"coarse range ends at prior exact mark", Landmarks{Merge: mark(6), Released: endedRelease}, "landmark order"},
		{"overlapping adjacent pairs with no global order", Landmarks{Request: request, Start: start, Merge: merge}, "landmark order"},
		{"negative resolution", Landmarks{Request: negative}, "negative landmark resolution"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Measure(Record{State: Delivered, Landmarks: tc.marks}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Measure error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestMeasureRejectsInconsistentEvidence(t *testing.T) {
	conflict := ci(1, 1, 2, 3)
	changed := conflict
	changed.Head = "other"
	receipt := ci(1, 3, 2, 4)
	noSource := mark(1)
	noSource.Source = ""
	for _, tc := range []struct {
		name string
		in   Record
		want string
	}{
		{"landmark order across gap", Record{Landmarks: Landmarks{Request: mark(5), Merge: mark(3)}}, "landmark order"},
		{"landmark source", Record{Landmarks: Landmarks{Start: noSource}}, "landmark source"},
		{"interval order", Record{Activity: []Interval{window("review", Review, 5, 2)}}, "interval order"},
		{"empty interval", Record{Activity: []Interval{window("review", Review, 5, 5)}}, "interval order"},
		{"conflicting interval", Record{Activity: []Interval{window("review", Review, 1, 3), window("review", Review, 1, 4)}}, "conflicting interval"},
		{"CI order", Record{CI: []CIReceipt{receipt}}, "CI timestamp order"},
		{"conflicting CI receipt", Record{CI: []CIReceipt{conflict, changed}}, "conflicting CI receipt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Measure(tc.in); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Measure error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestMeasureSeparatesUndeliveredAndReopenedRecords(t *testing.T) {
	for _, state := range []State{Open, Canceled, Reopened} {
		t.Run(string(state), func(t *testing.T) {
			got, err := Measure(Record{
				State:     state,
				Landmarks: Landmarks{Request: mark(0), Start: mark(1), Merge: mark(2), Released: mark(3)},
				Activity:  []Interval{window("observed", Implementation, 1, 2)},
			})
			if err != nil {
				t.Fatal(err)
			}
			if got.Lead != nil || got.Cycle != nil || got.Unclassified != nil || got.Coverage.Landmarks != 4 || got.Active != time.Hour {
				t.Fatalf("undelivered or reopened history entered completion metrics: %+v", got)
			}
		})
	}
}

func instant(hour int) time.Time {
	return time.Date(2020, time.January, 2, hour, 0, 0, 0, time.UTC)
}

func mark(hour int) *Evidence {
	return &Evidence{At: instant(hour), Source: "fixture event"}
}

func window(id string, kind IntervalKind, start, end int) Interval {
	return Interval{Receipt: id, Kind: kind, Start: instant(start), End: instant(end)}
}

func ci(attempt, queued, started, completed int) CIReceipt {
	return CIReceipt{
		Source: "fixture CI", Run: "checks", Job: "test", Attempt: attempt,
		Queued: instant(queued), Started: instant(started), Completed: instant(completed),
		Head: "fixture revision", Outcome: "success",
	}
}

func assertDuration(t *testing.T, name string, actual *time.Duration, want time.Duration) {
	t.Helper()
	if actual == nil {
		t.Fatalf("%s is missing, want %s", name, want)
	}
	if *actual != want {
		t.Fatalf("%s = %s, want %s", name, *actual, want)
	}
}
