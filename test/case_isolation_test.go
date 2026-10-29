//go:build e2e

package e2e

import (
	"strings"
	"testing"

	"github.com/bpradana/sandboxd/internal/presentation"
)

func (s *e2eSuite) runIsolationCase(t *testing.T) {
	sandbox := s.createSandbox(t, presentation.CreateSandboxRequest{})

	t.Run("commands run as the sandbox's root by default", func(t *testing.T) {
		out := s.mustSucceed(t, sandbox.ID, "id -u; id -g; whoami; echo $HOME; pwd")
		if out != "0\n0\nroot\n/root\n/root\n" {
			t.Fatalf("identity = %q", out)
		}
		// Root inside maps to an unprivileged range on the host.
		if out := s.mustSucceed(t, sandbox.ID, "awk '{print $1, $2}' /proc/self/uid_map"); out != "0 100000\n" {
			t.Fatalf("uid_map = %q", out)
		}
	})

	t.Run("root holds only the reduced capability set", func(t *testing.T) {
		// CHOWN DAC_OVERRIDE FOWNER FSETID KILL SETGID SETUID NET_BIND_SERVICE SYS_CHROOT
		const want = "00000000000404fb"
		out := s.mustSucceed(t, sandbox.ID, "awk '/^Cap(Eff|Bnd)/ {print $2}' /proc/self/status")
		if out != want+"\n"+want+"\n" {
			t.Fatalf("CapEff/CapBnd = %q, want %s", out, want)
		}
	})

	t.Run("the sandbox user is unprivileged", func(t *testing.T) {
		result := s.execAs(t, sandbox.ID, "sandbox", "id -u; whoami; echo $HOME; pwd; awk '/^CapEff/ {print $2}' /proc/self/status", 10)
		if result.ExitCode != 0 || result.Stdout != "1000\nsandbox\n/home/sandbox\n/home/sandbox\n0000000000000000\n" {
			t.Fatalf("sandbox user = %+v", result)
		}
		if result := s.execAs(t, sandbox.ID, "sandbox", "touch /etc/x", 10); result.ExitCode == 0 {
			t.Fatal("the sandbox user can write /etc")
		}
	})

	t.Run("root administers its own filesystem", func(t *testing.T) {
		s.mustSucceed(t, sandbox.ID, "chown 1000:1000 /etc/hostname && touch /usr/bin/new && rm /usr/bin/new && echo 10.9.9.9 x >> /etc/hosts")
	})

	t.Run("pid namespace shows only sandbox processes", func(t *testing.T) {
		out := s.mustSucceed(t, sandbox.ID, "ps -o pid,comm | awk 'NR>1 {print $1, $2}'")
		if !strings.HasPrefix(out, "1 sandboxd-init\n") {
			t.Fatalf("process list = %q", out)
		}
		if strings.Count(out, "\n") > 4 {
			t.Fatalf("unexpected processes visible: %q", out)
		}
	})

	t.Run("hostname is the sandbox's own", func(t *testing.T) {
		if out := s.mustSucceed(t, sandbox.ID, "hostname"); out != "sandbox\n" {
			t.Fatalf("hostname = %q", out)
		}
	})

	t.Run("privileged operations are denied, even to root", func(t *testing.T) {
		for _, user := range []string{"root", "sandbox"} {
			for _, command := range []string{
				"unshare -U true",
				"unshare -m true",
				"mount -t tmpfs none /tmp",
				"cat /proc/1/environ",
				"readlink /proc/1/exe",
				"hostname other",
				"ip link add dummy0 type dummy",
				"mknod /tmp/mem c 1 1",
				"echo 1 > /proc/sys/net/ipv4/ip_forward",
				"echo max > /sys/fs/cgroup/memory.max",
			} {
				if result := s.execAs(t, sandbox.ID, user, command, 10); result.ExitCode == 0 {
					t.Errorf("%q succeeded as %s inside the sandbox", command, user)
				}
			}
		}
	})

	t.Run("nested user namespaces and IPv6 are disabled", func(t *testing.T) {
		// Seccomp cannot filter clone3 flags, so the namespace limit is what
		// stops clone3(CLONE_NEWUSER).
		if out := s.mustSucceed(t, sandbox.ID, "cat /proc/sys/user/max_user_namespaces /proc/sys/net/ipv6/conf/all/disable_ipv6"); out != "0\n1\n" {
			t.Fatalf("sysctls = %q", out)
		}
	})

	t.Run("sensitive kernel interfaces are masked", func(t *testing.T) {
		out := s.mustSucceed(t, sandbox.ID, "wc -c < /proc/kcore 2>/dev/null || echo 0; echo x > /proc/sys/kernel/hostname 2>/dev/null; echo $?")
		if !strings.HasSuffix(out, "\n1\n") || !strings.HasPrefix(out, "0") {
			t.Fatalf("kcore/sysctl = %q", out)
		}
	})

	t.Run("environment is applied", func(t *testing.T) {
		envSandbox := s.createSandbox(t, presentation.CreateSandboxRequest{Env: map[string]string{"GREETING": "hello"}})
		if out := s.mustSucceed(t, envSandbox.ID, "echo $GREETING"); out != "hello\n" {
			t.Fatalf("GREETING = %q", out)
		}
	})

	t.Run("sandboxes cannot see each other's files", func(t *testing.T) {
		other := s.createSandbox(t, presentation.CreateSandboxRequest{})
		s.mustSucceed(t, sandbox.ID, "echo secret > /tmp/only-here")
		if result := s.exec(t, other.ID, "cat /tmp/only-here", 10); result.ExitCode == 0 {
			t.Fatal("file leaked between sandboxes")
		}
	})
}
