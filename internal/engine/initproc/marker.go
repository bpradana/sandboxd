//go:build linux

package initproc

import (
	"bytes"
	"strconv"
)

// markerScanner finds the sentinel line a session appends after each command
// ("\n<marker><exit code>\n") in a stream of output chunks, passing through
// everything before it. Bytes that might be the start of a split marker are
// held back until the next chunk disambiguates them.
type markerScanner struct {
	needle []byte // "\n" + marker
	buf    []byte
	done   bool
	code   int
}

func newMarkerScanner(marker string) *markerScanner {
	return &markerScanner{needle: []byte("\n" + marker)}
}

// Feed consumes a chunk and returns the output that is safe to emit.
func (s *markerScanner) Feed(chunk []byte) []byte {
	if s.done {
		return nil
	}
	s.buf = append(s.buf, chunk...)

	if idx := bytes.Index(s.buf, s.needle); idx >= 0 {
		rest := s.buf[idx+len(s.needle):]
		end := bytes.IndexByte(rest, '\n')
		if end < 0 {
			// Marker found but its line is incomplete; emit what precedes it.
			out := append([]byte(nil), s.buf[:idx]...)
			s.buf = s.buf[idx:]
			return out
		}
		if code, err := strconv.Atoi(string(rest[:end])); err == nil {
			s.code = code
		}
		out := append([]byte(nil), s.buf[:idx]...)
		s.buf = nil
		s.done = true
		return out
	}

	// Hold back the longest suffix that is a prefix of the needle.
	keep := 0
	for n := min(len(s.needle)-1, len(s.buf)); n > 0; n-- {
		if bytes.HasPrefix(s.needle, s.buf[len(s.buf)-n:]) {
			keep = n
			break
		}
	}
	out := append([]byte(nil), s.buf[:len(s.buf)-keep]...)
	s.buf = s.buf[len(s.buf)-keep:]
	return out
}

// Flush returns held-back bytes when the stream ends without a marker.
func (s *markerScanner) Flush() []byte {
	out := s.buf
	s.buf = nil
	return out
}

func (s *markerScanner) Done() bool { return s.done }

func (s *markerScanner) Code() int { return s.code }

// shellQuote wraps value in single quotes for POSIX sh.
func shellQuote(value string) string {
	return "'" + string(bytes.ReplaceAll([]byte(value), []byte("'"), []byte(`'\''`))) + "'"
}
