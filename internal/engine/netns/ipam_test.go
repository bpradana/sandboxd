//go:build linux

package netns

import "testing"

func TestIPAMSkipsReservedAddresses(t *testing.T) {
	t.Parallel()

	pool, err := newIPAM("10.88.0.0/29")
	if err != nil {
		t.Fatal(err)
	}
	if got := pool.gateway().String(); got != "10.88.0.1" {
		t.Fatalf("gateway = %s", got)
	}

	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		ip, err := pool.allocate("sbx")
		if err != nil {
			t.Fatalf("allocate %d: %v", i, err)
		}
		switch ip.String() {
		case "10.88.0.0", "10.88.0.1", "10.88.0.7":
			t.Fatalf("allocated reserved address %s", ip)
		}
		seen[ip.String()] = true
	}
	if len(seen) != 5 {
		t.Fatalf("duplicate allocations: %v", seen)
	}
	if _, err := pool.allocate("sbx"); err == nil {
		t.Fatal("allocate succeeded on an exhausted pool")
	}

	pool.release(pool.ip(3))
	ip, err := pool.allocate("sbx")
	if err != nil || ip.String() != "10.88.0.3" {
		t.Fatalf("allocate after release = %v, %v", ip, err)
	}
}
