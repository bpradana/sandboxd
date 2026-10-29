//go:build e2e

package e2e

import (
	"fmt"
	"strings"
	"testing"

	"github.com/bpradana/sandboxd/internal/presentation"
)

func (s *e2eSuite) runNetworkCase(t *testing.T) {
	t.Run("none mode has only loopback", func(t *testing.T) {
		sandbox := s.createSandbox(t, presentation.CreateSandboxRequest{Network: "none"})
		out := s.mustSucceed(t, sandbox.ID, "ip -o link | awk -F': ' '{print $2}'")
		if out != "lo\n" {
			t.Fatalf("interfaces = %q", out)
		}
		if result := s.exec(t, sandbox.ID, "wget -q -T 3 -O /dev/null http://1.1.1.1", 10); result.ExitCode == 0 {
			t.Fatal("none mode reached the internet")
		}
	})

	a := s.createSandbox(t, presentation.CreateSandboxRequest{Network: "egress"})
	b := s.createSandbox(t, presentation.CreateSandboxRequest{Network: "egress"})
	if a.IPAddress == "" || b.IPAddress == "" || a.IPAddress == b.IPAddress {
		t.Fatalf("addresses %q and %q", a.IPAddress, b.IPAddress)
	}

	t.Run("egress blocks private, metadata, and host destinations", func(t *testing.T) {
		for _, target := range []string{"169.254.169.254", "10.0.0.1", "172.17.0.1", "192.168.65.1", "100.100.100.200"} {
			result := s.exec(t, a.ID, fmt.Sprintf("wget -q -T 2 -O /dev/null http://%s/", target), 10)
			if result.ExitCode == 0 {
				t.Errorf("reached %s", target)
			}
		}
		gateway := strings.TrimSpace(s.mustSucceed(t, a.ID, "ip route | awk '/default/ {print $3}'"))
		if result := s.exec(t, a.ID, "nc -w 2 "+gateway+" 8080 </dev/null", 10); result.ExitCode == 0 {
			t.Errorf("reached the host through the gateway %s", gateway)
		}
	})

	t.Run("sandboxes cannot reach each other", func(t *testing.T) {
		s.mustSucceed(t, b.ID, "(nc -l -p 9000 -e echo reachable >/dev/null 2>&1 &); sleep 0.3; nc -z 127.0.0.1 9000")
		if result := s.exec(t, a.ID, "nc -w 2 "+b.IPAddress+" 9000 </dev/null", 10); result.ExitCode == 0 || strings.Contains(result.Stdout, "reachable") {
			t.Fatalf("sandbox %s reached %s: %+v", a.ID, b.ID, result)
		}
	})

	t.Run("public internet is reachable", func(t *testing.T) {
		if !s.internet {
			t.Skipf("%s is not 1", envInternet)
		}
		if out := s.mustSucceed(t, a.ID, "wget -q -T 10 -O- http://example.com | grep -c 'Example Domain'"); out == "0\n" {
			t.Fatalf("unexpected body count %q", out)
		}
	})

	t.Run("root installs packages", func(t *testing.T) {
		if !s.internet {
			t.Skipf("%s is not 1", envInternet)
		}
		result := s.exec(t, a.ID, "apk add --no-cache jq >/dev/null && echo '{\"a\":42}' | jq .a", 120)
		if result.ExitCode != 0 || result.Stdout != "42\n" {
			t.Fatalf("package install: %+v", result)
		}
	})
}
