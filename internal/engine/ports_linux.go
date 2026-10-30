//go:build linux

package engine

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
)

// tcpStateListen is the hexadecimal st column of a listening socket in
// /proc/net/tcp.
const tcpStateListen = "0A"

// ListeningPorts reads the sandbox network namespace's TCP tables. Those
// tables live in /proc/<pid>/net, which is per-namespace, so this needs
// neither setns nor a command inside the sandbox.
func (e *linuxEngine) ListeningPorts(ctx context.Context, id string) ([]ListeningPort, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	inst, err := e.running(id)
	if err != nil {
		return nil, err
	}
	inst.teardownMu.Lock()
	pid := inst.pid
	destroying := inst.destroying.Load()
	inst.teardownMu.Unlock()
	if destroying {
		return nil, ErrSandboxNotFound
	}

	// A port can be bound several times -- IPv4 and IPv6, or one address per
	// interface. Keep the binding the preview proxy has the best chance of
	// reaching, which is the one listening on the widest address.
	found := map[int]ListeningPort{}
	for _, table := range []string{"tcp", "tcp6"} {
		file, err := os.Open(fmt.Sprintf("/proc/%d/net/%s", pid, table)) // #nosec G304 -- engine-owned init pid, not user input.
		if err != nil {
			if os.IsNotExist(err) {
				continue // The kernel was built without IPv6.
			}
			return nil, err
		}
		ports, err := parseListeners(file)
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		for _, port := range ports {
			if previous, ok := found[port.Port]; !ok || reach(port) > reach(previous) {
				found[port.Port] = port
			}
		}
	}
	list := slices.Collect(maps.Values(found))
	slices.SortFunc(list, func(a, b ListeningPort) int { return a.Port - b.Port })
	return list, nil
}

// reach ranks how reachable a binding is from inside the sandbox: anything
// bound to the unspecified address accepts loopback connections, a loopback
// binding is reachable only from inside, and any other address is not
// reachable over loopback at all.
func reach(port ListeningPort) int {
	switch {
	case port.Unspecified:
		return 2
	case port.Loopback:
		return 1
	default:
		return 0
	}
}

// parseListeners returns the listening sockets of one /proc/net/tcp table.
// Malformed rows are skipped: the table is a diagnostic, not a contract.
func parseListeners(r io.Reader) ([]ListeningPort, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	var ports []ListeningPort
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 || fields[3] != tcpStateListen {
			continue
		}
		local, port, ok := parseProcAddress(fields[1])
		if !ok {
			continue
		}
		ports = append(ports, ListeningPort{
			Port:        port,
			Address:     local.String(),
			Loopback:    local.IsLoopback(),
			Unspecified: local.IsUnspecified(),
		})
	}
	return ports, scanner.Err()
}

// parseProcAddress decodes an "ADDRESS:PORT" column. The address is the
// socket's raw bytes in hexadecimal, host byte order per 32-bit word, which
// on every Linux target is little-endian.
func parseProcAddress(field string) (netip.Addr, int, bool) {
	host, portText, ok := strings.Cut(field, ":")
	if !ok || (len(host) != 8 && len(host) != 32) {
		return netip.Addr{}, 0, false
	}
	raw, err := hex.DecodeString(host)
	if err != nil {
		return netip.Addr{}, 0, false
	}
	port, err := strconv.ParseUint(portText, 16, 16)
	if err != nil || port == 0 {
		return netip.Addr{}, 0, false
	}
	for word := 0; word < len(raw); word += 4 {
		binary.BigEndian.PutUint32(raw[word:word+4], binary.LittleEndian.Uint32(raw[word:word+4]))
	}
	address, ok := netip.AddrFromSlice(raw)
	if !ok {
		return netip.Addr{}, 0, false
	}
	return address.Unmap(), int(port), true
}
