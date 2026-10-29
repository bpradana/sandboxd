package logging

import (
	"context"
	"testing"
	"unsafe"
)

// WithRequestID must detach from its input: a client-sent X-Request-ID is a
// zero-copy view of fasthttp's pooled request buffer, while this context feeds
// spans and detached goroutines that log after the request ends.
func TestWithRequestIDDetachesFromBackingBytes(t *testing.T) {
	buf := []byte("req-id-1")
	aliased := unsafe.String(unsafe.SliceData(buf), len(buf))
	ctx := WithRequestID(context.Background(), aliased)
	for i := range buf {
		buf[i] = 'x'
	}
	if got := RequestIDFromContext(ctx); got != "req-id-1" {
		t.Fatalf("request id aliases caller bytes: %q", got)
	}
}
