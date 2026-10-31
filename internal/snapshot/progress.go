package snapshot

import (
	"context"
	"io"
	"sync"
)

// reportStep is how much one part must move between reports. Reporting every
// write would put the observer's lock in the copy loop for no visible gain,
// since progress is read by people.
const reportStep = 1 << 20

// Reporter receives the number of archive bytes a transfer has moved so far.
// The total never falls, so a part restarted after a failure cannot rewind a
// progress bar, and a retried part is never counted twice.
type Reporter func(transferred int64)

type reporterKey struct{}

// WithReporter attaches a progress sink to ctx, the way httptrace attaches
// client-side hooks: a transfer started with this context reports into it, and
// one started without it behaves identically. Progress is observable for a
// single operation, which is exactly the lifetime of a context.
func WithReporter(ctx context.Context, report Reporter) context.Context {
	if report == nil {
		return ctx
	}
	return context.WithValue(ctx, reporterKey{}, report)
}

// transfer accumulates one upload's or download's progress. Parallel workers
// each account for their own part, so the total moves between part boundaries
// instead of jumping a whole part at a time -- on a slow link that is the
// difference between a bar that moves and one that reads 0% for minutes.
type transfer struct {
	report Reporter
	mu     sync.Mutex
	parts  map[int]int64
	peak   int64
}

func newTransfer(ctx context.Context) *transfer {
	report, _ := ctx.Value(reporterKey{}).(Reporter)
	return &transfer{report: report, parts: map[int]int64{}}
}

// observed reports whether anything is watching, so callers can skip the
// bookkeeping entirely when nothing is.
func (t *transfer) observed() bool { return t.report != nil }

// set records how far one part has got and reports the running total.
func (t *transfer) set(index int, done int64) {
	if !t.observed() {
		return
	}
	t.mu.Lock()
	t.parts[index] = done
	var sum int64
	for _, part := range t.parts {
		sum += part
	}
	if sum > t.peak {
		t.peak = sum
	}
	peak := t.peak
	t.mu.Unlock()
	t.report(peak)
}

// writer counts what one part writes through to dst.
func (t *transfer) writer(index int, dst io.Writer) io.Writer {
	if !t.observed() {
		return dst
	}
	return &partWriter{transfer: t, index: index, dst: dst}
}

type partWriter struct {
	transfer *transfer
	index    int
	dst      io.Writer
	written  int64
	reported int64
}

func (w *partWriter) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	w.written += int64(n)
	if w.written-w.reported >= reportStep || err != nil {
		w.reported = w.written
		w.transfer.set(w.index, w.written)
	}
	return n, err
}

// flush reports a part's final count, which the step threshold may have held
// back. Callers run it once the part is safely stored or written.
func (w *partWriter) flush() {
	if w.written != w.reported {
		w.reported = w.written
		w.transfer.set(w.index, w.written)
	}
}
