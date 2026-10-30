//go:build linux

package engine

import (
	"strings"
	"testing"
)

// Rows copied from a real /proc/net/tcp and /proc/net/tcp6, trimmed after the
// inode column, which this parser never reads.
const (
	procTCP = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:0BB8 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 14823 1
   1: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 14824 1
   2: 0201A8C0:2328 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 14825 1
   3: 0100007F:1F90 0100007F:A1B2 01 00000000:00000000 00:00000000 00000000  1000        0 14826 1
`
	procTCP6 = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000000000000:170C 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 14830 1
   1: 00000000000000000000000001000000:1F91 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 14831 1
`
)

func TestParseListenersReadsEveryAddressFamily(t *testing.T) {
	ports, err := parseListeners(strings.NewReader(procTCP))
	if err != nil {
		t.Fatalf("parse tcp: %v", err)
	}
	want := []ListeningPort{
		{Port: 3000, Address: "0.0.0.0", Unspecified: true},
		{Port: 8080, Address: "127.0.0.1", Loopback: true},
		{Port: 9000, Address: "192.168.1.2"},
	}
	if len(ports) != len(want) {
		t.Fatalf("got %d listeners, want %d: %+v", len(ports), len(want), ports)
	}
	for i, expected := range want {
		if ports[i] != expected {
			t.Errorf("listener %d = %+v, want %+v", i, ports[i], expected)
		}
	}

	ports, err = parseListeners(strings.NewReader(procTCP6))
	if err != nil {
		t.Fatalf("parse tcp6: %v", err)
	}
	want = []ListeningPort{
		{Port: 5900, Address: "::", Unspecified: true},
		{Port: 8081, Address: "::1", Loopback: true},
	}
	if len(ports) != len(want) {
		t.Fatalf("got %d listeners, want %d: %+v", len(ports), len(want), ports)
	}
	for i, expected := range want {
		if ports[i] != expected {
			t.Errorf("listener %d = %+v, want %+v", i, ports[i], expected)
		}
	}
}

func TestParseListenersSkipsRowsItCannotTrust(t *testing.T) {
	table := `  sl  local_address rem_address   st
   0: 0100007F:0000 00000000:0000 0A
   1: ZZZZZZZZ:1F90 00000000:0000 0A
   2: 0100 00000000:0000 0A
   3: 0100007F 00000000:0000 0A
   4: 0100007F:1F90 00000000:0000
`
	ports, err := parseListeners(strings.NewReader(table))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(ports) != 0 {
		t.Fatalf("got %+v, want no listeners", ports)
	}
}

func TestReachPrefersTheWidestBinding(t *testing.T) {
	unspecified := ListeningPort{Port: 3000, Address: "0.0.0.0", Unspecified: true}
	loopback := ListeningPort{Port: 3000, Address: "127.0.0.1", Loopback: true}
	external := ListeningPort{Port: 3000, Address: "192.168.1.2"}
	if reach(unspecified) <= reach(loopback) || reach(loopback) <= reach(external) {
		t.Fatalf("reach ranking is wrong: %d %d %d", reach(unspecified), reach(loopback), reach(external))
	}
}
