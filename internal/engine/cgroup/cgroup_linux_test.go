//go:build linux

package cgroup

import "testing"

func TestDefaultRootNeverNests(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"/":                                     "/sys/fs/cgroup",
		"/daemon":                               "/sys/fs/cgroup",
		"/system.slice/sandboxd.service":        "/sys/fs/cgroup/system.slice/sandboxd.service",
		"/system.slice/sandboxd.service/daemon": "/sys/fs/cgroup/system.slice/sandboxd.service",
	}
	for self, want := range cases {
		if got := DefaultRoot(self); got != want {
			t.Errorf("DefaultRoot(%q) = %q, want %q", self, got, want)
		}
	}
}
