//go:build linux

package seccomp

import "testing"

func TestPolicyAssemblesForThisArch(t *testing.T) {
	t.Parallel()

	filter, err := Policy()
	if err != nil {
		t.Fatalf("Policy() error = %v", err)
	}
	if len(filter.Policy.Syscalls) != 1 || len(filter.Policy.Syscalls[0].Names) < 20 {
		t.Fatalf("unexpected policy: %+v", filter.Policy)
	}
	if _, err := filter.Policy.Assemble(); err != nil {
		t.Fatalf("Assemble() error = %v", err)
	}
}
