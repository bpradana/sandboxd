package snapshot

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
)

func reported(t *testing.T) (context.Context, func() []int64) {
	t.Helper()
	var mu sync.Mutex
	var seen []int64
	ctx := WithReporter(context.Background(), func(transferred int64) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, transferred)
	})
	return ctx, func() []int64 {
		mu.Lock()
		defer mu.Unlock()
		return append([]int64(nil), seen...)
	}
}

func TestTransferSumsConcurrentPartsAndNeverFalls(t *testing.T) {
	ctx, seen := reported(t)
	transfer := newTransfer(ctx)

	var workers sync.WaitGroup
	for part := range 4 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			transfer.set(part, 25)
		}()
	}
	workers.Wait()

	all := seen()
	if len(all) != 4 || all[len(all)-1] != 100 {
		t.Fatalf("reports = %v, want one per part ending at the whole transfer", all)
	}
	for i := 1; i < len(all); i++ {
		if all[i] <= all[i-1] {
			t.Fatalf("reports are not monotonic: %v", all)
		}
	}

	// A part that restarts after a failure counts once, and the total it
	// already reached is not rewound while the other parts hold it up.
	transfer.set(0, 0)
	all = seen()
	if all[len(all)-1] != 100 {
		t.Fatalf("a restarted part rewound the total: %v", all)
	}
	transfer.set(0, 25)
	transfer.set(4, 25)
	if last := seen(); last[len(last)-1] != 125 {
		t.Fatalf("a new part did not add to the total: %v", last)
	}
}

func TestTransferWithoutReporterIsInert(t *testing.T) {
	transfer := newTransfer(context.Background())
	transfer.set(0, 100)
	if transfer.peak != 0 || len(transfer.parts) != 0 {
		t.Fatal("an unobserved transfer should not bother counting")
	}

	var dst bytes.Buffer
	if got := transfer.writer(0, &dst); got != io.Writer(&dst) {
		t.Fatal("an unobserved transfer should write straight to the destination")
	}
	if WithReporter(context.Background(), nil) == nil {
		t.Fatal("a nil reporter must still yield a usable context")
	}
}

func TestPartWriterReportsPerMegabyteAndFlushesTheRemainder(t *testing.T) {
	ctx, seen := reported(t)
	var dst bytes.Buffer
	writer := newTransfer(ctx).writer(0, &dst)

	chunk := make([]byte, 64<<10)
	for range 48 { // 3 MiB in 64 KiB writes.
		if _, err := writer.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	// Reporting every write would put the observer's lock in the copy loop.
	if all := seen(); len(all) != 3 || all[2] != 3<<20 {
		t.Fatalf("reports = %v, want one per megabyte", all)
	}

	// A tail below the step would otherwise never be reported.
	if _, err := writer.Write([]byte("tail")); err != nil {
		t.Fatal(err)
	}
	writer.(*partWriter).flush()
	all := seen()
	if all[len(all)-1] != 3<<20+4 {
		t.Fatalf("the final bytes were not reported: %v", all)
	}
	if dst.Len() != 3<<20+4 {
		t.Fatalf("wrote %d bytes, want everything passed through", dst.Len())
	}
}

func TestPartWriterReportsWhatItsDestinationAccepted(t *testing.T) {
	ctx, seen := reported(t)
	// A LimitedWriter rejects an overlong write without writing any of it,
	// which is how a ranged download refuses to cross into another part.
	limited := &LimitedWriter{Writer: io.Discard, Remaining: 4}
	writer := newTransfer(ctx).writer(0, limited)
	if _, err := writer.Write(make([]byte, 16)); err == nil {
		t.Fatal("the limit was not enforced")
	}
	for _, report := range seen() {
		if report != 0 {
			t.Fatalf("counted bytes the destination refused: %v", seen())
		}
	}
}
