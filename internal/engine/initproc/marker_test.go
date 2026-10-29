//go:build linux

package initproc

import (
	"strings"
	"testing"
)

func feedAll(s *markerScanner, chunks ...string) string {
	var out strings.Builder
	for _, chunk := range chunks {
		out.Write(s.Feed([]byte(chunk)))
	}
	return out.String()
}

func TestMarkerScannerWholeChunk(t *testing.T) {
	t.Parallel()

	s := newMarkerScanner("__SBX_abc_")
	out := feedAll(s, "hello\n\n__SBX_abc_3\n")
	if out != "hello\n" || !s.Done() || s.Code() != 3 {
		t.Fatalf("out=%q done=%v code=%d", out, s.Done(), s.Code())
	}
}

func TestMarkerScannerSplitAcrossChunks(t *testing.T) {
	t.Parallel()

	s := newMarkerScanner("__SBX_abc_")
	out := feedAll(s, "partial line", "\n__SB", "X_ab", "c_0", "\n")
	if out != "partial line" || !s.Done() || s.Code() != 0 {
		t.Fatalf("out=%q done=%v code=%d", out, s.Done(), s.Code())
	}
}

func TestMarkerScannerPassesLookalikes(t *testing.T) {
	t.Parallel()

	s := newMarkerScanner("__SBX_abc_")
	out := feedAll(s, "a\n__SBX_xyz_1\nb\n", "\n__SBX_abc_42\n")
	if out != "a\n__SBX_xyz_1\nb\n" || s.Code() != 42 {
		t.Fatalf("out=%q code=%d", out, s.Code())
	}
}

func TestMarkerScannerFlushWithoutMarker(t *testing.T) {
	t.Parallel()

	s := newMarkerScanner("__SBX_abc_")
	out := feedAll(s, "tail\n__S")
	out += string(s.Flush())
	if out != "tail\n__S" || s.Done() {
		t.Fatalf("out=%q done=%v", out, s.Done())
	}
}

func TestShellQuote(t *testing.T) {
	t.Parallel()

	if got := shellQuote(`echo 'hi' $X`); got != `'echo '\''hi'\'' $X'` {
		t.Fatalf("shellQuote() = %s", got)
	}
}
