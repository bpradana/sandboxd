//go:build linux

package netns

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
)

var ErrPoolExhausted = errors.New("sandbox network address pool exhausted")

// ipam hands out host addresses of an IPv4 network. The network address,
// the gateway (first host), and the broadcast address are never allocated.
type ipam struct {
	mu        sync.Mutex
	network   *net.IPNet
	base      uint32
	size      uint32
	allocated map[uint32]string
	next      uint32
}

func newIPAM(cidr string) (*ipam, error) {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, err
	}
	ip4 := network.IP.To4()
	if ip4 == nil {
		return nil, fmt.Errorf("%s is not an IPv4 network", cidr)
	}
	ones, bits := network.Mask.Size()
	size := uint32(1) << uint(bits-ones) // #nosec G115 -- prefix length is 0..32.
	if size < 8 {
		return nil, fmt.Errorf("%s is too small", cidr)
	}
	return &ipam{
		network:   network,
		base:      binary.BigEndian.Uint32(ip4),
		size:      size,
		allocated: make(map[uint32]string),
		next:      2,
	}, nil
}

func (p *ipam) gateway() net.IP {
	return p.ip(1)
}

func (p *ipam) prefixLen() int {
	ones, _ := p.network.Mask.Size()
	return ones
}

func (p *ipam) ip(offset uint32) net.IP {
	out := make(net.IP, 4)
	binary.BigEndian.PutUint32(out, p.base+offset)
	return out
}

func (p *ipam) allocate(owner string) (net.IP, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	usable := p.size - 3                    // network, gateway, broadcast
	if uint32(len(p.allocated)) >= usable { // #nosec G115 -- map size is bounded by usable.
		return nil, ErrPoolExhausted
	}
	for {
		offset := p.next
		p.next++
		if p.next >= p.size-1 {
			p.next = 2
		}
		if _, taken := p.allocated[offset]; !taken {
			p.allocated[offset] = owner
			return p.ip(offset), nil
		}
	}
}

func (p *ipam) release(ip net.IP) {
	ip4 := ip.To4()
	if ip4 == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.allocated, binary.BigEndian.Uint32(ip4)-p.base)
}
