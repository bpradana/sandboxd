package sandbox

import (
	"bytes"
	"sync"
	"testing"
)

func TestProgressTrackerReportsOnlyRunningJobs(t *testing.T) {
	tracker := newProgressTracker()
	if tracker.response("snap-1") != nil {
		t.Fatal("a job that is not running has no progress")
	}

	tracker.enter("snap-1", operationBackup, phaseUploading, 0, 300)
	tracker.reporter("snap-1", phaseUploading)(100)
	got := tracker.response("snap-1")
	if got == nil || got.Operation != operationBackup || got.Phase != phaseUploading || got.BytesDone != 100 || got.BytesTotal != 300 {
		t.Fatalf("progress = %+v", got)
	}

	tracker.done("snap-1")
	if tracker.response("snap-1") != nil {
		t.Fatal("a finished job still reports progress")
	}
}

func TestProgressTrackerNeverRewinds(t *testing.T) {
	tracker := newProgressTracker()
	tracker.enter("snap-1", operationBackup, phaseUploading, 0, 300)
	tracker.advance("snap-1", phaseUploading, 200)

	// A part of the previous phase finishing late must not drag the bar back,
	// nor resurrect the phase the job has already left.
	tracker.advance("snap-1", phaseUploading, 50)
	if tracker.response("snap-1").BytesDone != 200 {
		t.Fatalf("progress went backwards: %+v", tracker.response("snap-1"))
	}

	tracker.enter("snap-1", operationBackup, phaseVerifying, 0, 300)
	tracker.advance("snap-1", phaseUploading, 300)
	got := tracker.response("snap-1")
	if got.Phase != phaseVerifying || got.BytesDone != 0 {
		t.Fatalf("a stale report changed the current phase: %+v", got)
	}
}

func TestProgressTrackerIgnoresJobsItDoesNotKnow(t *testing.T) {
	tracker := newProgressTracker()
	tracker.advance("snap-missing", phaseUploading, 10)
	tracker.enter("", operationRestore, phaseDownloading, 0, 10)
	tracker.done("")
	if tracker.response("snap-missing") != nil || len(tracker.jobs) != 0 {
		t.Fatalf("tracker holds %+v", tracker.jobs)
	}
}

func TestProgressTrackerSurvivesConcurrentUse(t *testing.T) {
	tracker := newProgressTracker()
	tracker.enter("snap-1", operationRestore, phaseDownloading, 0, 1000)
	report := tracker.reporter("snap-1", phaseDownloading)

	var workers sync.WaitGroup
	for i := range 20 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			report(int64(i+1) * 50)
			tracker.response("snap-1")
		}()
	}
	workers.Wait()
	if got := tracker.response("snap-1"); got.BytesDone != 1000 {
		t.Fatalf("highest report not kept: %+v", got)
	}
}

func TestCountingWriterReportsPerMegabyteWithoutLosingBytes(t *testing.T) {
	var dst bytes.Buffer
	var reports []int64
	writer := &countingWriter{dst: &dst, report: func(written int64) { reports = append(reports, written) }}

	chunk := make([]byte, 64<<10)
	for range 48 { // 3 MiB in 64 KiB writes.
		if _, err := writer.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if dst.Len() != 3<<20 {
		t.Fatalf("wrote %d bytes, want the whole archive", dst.Len())
	}
	// Reporting every write would put the tracker's lock in the archive's
	// write path; reporting per megabyte keeps it out without losing detail.
	if len(reports) != 3 || reports[2] != 3<<20 {
		t.Fatalf("reports = %v", reports)
	}
}
