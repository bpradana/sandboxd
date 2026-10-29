//go:build linux

package netns

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

const (
	// TableName is the nftables table (family inet) owned by the engine.
	TableName = "sandboxd"
	// VethPrefix prefixes host-side veth names; reconcile deletes any it finds.
	VethPrefix = "sbx"
	ifNameSize = 16
)

// builtinDeny are destinations sandboxes may never reach: "this" network,
// RFC1918, CGNAT, loopback, link-local (cloud metadata), IETF protocol
// assignments, benchmarking, multicast, and reserved space.
var builtinDeny = []string{
	"0.0.0.0/8",
	"10.0.0.0/8",
	"100.64.0.0/10",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"172.16.0.0/12",
	"192.0.0.0/24",
	"192.168.0.0/16",
	"198.18.0.0/15",
	"224.0.0.0/4",
	"240.0.0.0/4",
}

type Config struct {
	Bridge     string
	CIDR       string
	DNS        []string
	EgressDeny []string
	IPForward  bool
}

// Attachment is a sandbox's egress interface.
type Attachment struct {
	IP        net.IP
	PrefixLen int
	Gateway   net.IP
	HostVeth  string
}

type Manager struct {
	cfg  Config
	pool *ipam
}

// Setup creates the bridge and installs the nftables policy. It is idempotent
// so it also repairs state left by a crashed process.
func Setup(cfg Config) (*Manager, error) {
	pool, err := newIPAM(cfg.CIDR)
	if err != nil {
		return nil, err
	}
	manager := &Manager{cfg: cfg, pool: pool}

	if err := manager.ensureBridge(); err != nil {
		return nil, err
	}
	if cfg.IPForward {
		if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0o644); err != nil { // #nosec G306 -- procfs sysctl.
			return nil, fmt.Errorf("enable ip_forward: %w", err)
		}
	}
	if err := manager.installRules(); err != nil {
		return nil, fmt.Errorf("install nftables rules: %w", err)
	}
	return manager, nil
}

func (m *Manager) ensureBridge() error {
	link, err := netlink.LinkByName(m.cfg.Bridge)
	if err != nil {
		var notFound netlink.LinkNotFoundError
		if !errors.As(err, &notFound) {
			return fmt.Errorf("look up bridge: %w", err)
		}
		bridge := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: m.cfg.Bridge}}
		if err := netlink.LinkAdd(bridge); err != nil {
			return fmt.Errorf("create bridge %s: %w", m.cfg.Bridge, err)
		}
		link = bridge
	}

	addr := &netlink.Addr{IPNet: &net.IPNet{IP: m.pool.gateway(), Mask: net.CIDRMask(m.pool.prefixLen(), 32)}}
	if err := netlink.AddrReplace(link, addr); err != nil {
		return fmt.Errorf("assign bridge address: %w", err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("bring bridge up: %w", err)
	}
	return nil
}

// installRules replaces the engine's nftables table atomically.
func (m *Manager) installRules() error {
	deny, err := m.denyNetworks()
	if err != nil {
		return err
	}

	conn, err := nftables.New()
	if err != nil {
		return err
	}
	table := &nftables.Table{Family: nftables.TableFamilyINet, Name: TableName}
	// Add-then-delete guarantees the delete succeeds even if the table is absent.
	conn.AddTable(table)
	conn.DelTable(table)
	conn.AddTable(table)

	accept := nftables.ChainPolicyAccept
	postrouting := conn.AddChain(&nftables.Chain{
		Name: "postrouting", Table: table, Type: nftables.ChainTypeNAT,
		Hooknum: nftables.ChainHookPostrouting, Priority: nftables.ChainPriorityNATSource,
	})
	forward := conn.AddChain(&nftables.Chain{
		Name: "forward", Table: table, Type: nftables.ChainTypeFilter,
		Hooknum: nftables.ChainHookForward, Priority: nftables.ChainPriorityFilter, Policy: &accept,
	})
	input := conn.AddChain(&nftables.Chain{
		Name: "input", Table: table, Type: nftables.ChainTypeFilter,
		Hooknum: nftables.ChainHookInput, Priority: nftables.ChainPriorityFilter, Policy: &accept,
	})

	bridge := ifname(m.cfg.Bridge)
	_, sandboxNet, _ := net.ParseCIDR(m.cfg.CIDR)

	// NAT: sandbox traffic leaving through any non-bridge interface.
	conn.AddRule(&nftables.Rule{Table: table, Chain: postrouting, Exprs: concat(
		matchIPv4(),
		matchNetwork(12, sandboxNet), // ip saddr
		matchIfname(expr.MetaKeyOIFNAME, bridge, expr.CmpOpNeq),
		[]expr.Any{&expr.Masq{}},
	)})

	// Forward: replies are allowed back in; new traffic from sandboxes may only
	// go to public IPv4 destinations; nothing may be initiated towards them,
	// and sandboxes cannot reach each other (bridge ports are isolated too).
	conn.AddRule(&nftables.Rule{Table: table, Chain: forward, Exprs: concat(
		matchEstablished(), verdict(expr.VerdictAccept),
	)})
	for _, network := range deny {
		conn.AddRule(&nftables.Rule{Table: table, Chain: forward, Exprs: concat(
			matchIfname(expr.MetaKeyIIFNAME, bridge, expr.CmpOpEq),
			matchIPv4(),
			matchNetwork(16, network), // ip daddr
			verdict(expr.VerdictDrop),
		)})
	}
	conn.AddRule(&nftables.Rule{Table: table, Chain: forward, Exprs: concat(
		matchIfname(expr.MetaKeyIIFNAME, bridge, expr.CmpOpEq),
		matchNotIPv4(),
		verdict(expr.VerdictDrop),
	)})
	conn.AddRule(&nftables.Rule{Table: table, Chain: forward, Exprs: concat(
		matchIfname(expr.MetaKeyOIFNAME, bridge, expr.CmpOpEq),
		verdict(expr.VerdictDrop),
	)})

	// Input: sandboxes cannot talk to the host itself (daemon API, metadata
	// proxies, local services). DNS uses public resolvers for that reason.
	conn.AddRule(&nftables.Rule{Table: table, Chain: input, Exprs: concat(
		matchIfname(expr.MetaKeyIIFNAME, bridge, expr.CmpOpEq),
		matchEstablished(),
		verdict(expr.VerdictAccept),
	)})
	conn.AddRule(&nftables.Rule{Table: table, Chain: input, Exprs: concat(
		matchIfname(expr.MetaKeyIIFNAME, bridge, expr.CmpOpEq),
		verdict(expr.VerdictDrop),
	)})

	return conn.Flush()
}

// denyNetworks combines the built-in list, configured extras, and every IPv4
// address currently assigned to the host.
func (m *Manager) denyNetworks() ([]*net.IPNet, error) {
	var result []*net.IPNet
	for _, cidr := range append(append([]string{}, builtinDeny...), m.cfg.EgressDeny...) {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("parse deny cidr %q: %w", cidr, err)
		}
		result = append(result, network)
	}
	addrs, err := netlink.AddrList(nil, netlink.FAMILY_V4)
	if err != nil {
		return nil, fmt.Errorf("list host addresses: %w", err)
	}
	for _, addr := range addrs {
		result = append(result, &net.IPNet{IP: addr.IP.To4(), Mask: net.CIDRMask(32, 32)})
	}
	return result, nil
}

// Attach creates a veth pair, plugs the host end into the bridge as an
// isolated port, moves the peer into the namespace of pid as eth0, and
// configures addressing and routes from the host side.
func (m *Manager) Attach(sandboxID string, pid int) (*Attachment, error) {
	ip, err := m.pool.allocate(sandboxID)
	if err != nil {
		return nil, err
	}
	attachment, err := m.attach(sandboxID, pid, ip)
	if err != nil {
		m.pool.release(ip)
		return nil, err
	}
	return attachment, nil
}

func (m *Manager) attach(sandboxID string, pid int, ip net.IP) (*Attachment, error) {
	hostName, peerName := vethNames(sandboxID)
	bridge, err := netlink.LinkByName(m.cfg.Bridge)
	if err != nil {
		return nil, fmt.Errorf("look up bridge: %w", err)
	}

	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: hostName, MasterIndex: bridge.Attrs().Index},
		PeerName:  peerName,
	}
	if err := netlink.LinkAdd(veth); err != nil {
		return nil, fmt.Errorf("create veth: %w", err)
	}
	cleanup := func() { _ = netlink.LinkDel(veth) }

	host, err := netlink.LinkByName(hostName)
	if err != nil {
		cleanup()
		return nil, err
	}
	if err := netlink.LinkSetIsolated(host, true); err != nil {
		cleanup()
		return nil, fmt.Errorf("isolate bridge port: %w", err)
	}
	if err := netlink.LinkSetUp(host); err != nil {
		cleanup()
		return nil, err
	}

	peer, err := netlink.LinkByName(peerName)
	if err != nil {
		cleanup()
		return nil, err
	}
	if err := netlink.LinkSetNsPid(peer, pid); err != nil {
		cleanup()
		return nil, fmt.Errorf("move veth into sandbox: %w", err)
	}

	attachment := &Attachment{IP: ip, PrefixLen: m.pool.prefixLen(), Gateway: m.pool.gateway(), HostVeth: hostName}
	if err := configureInside(pid, peerName, attachment); err != nil {
		cleanup()
		return nil, err
	}
	return attachment, nil
}

// configureInside sets up eth0 and lo inside the sandbox netns through a
// netlink handle bound to that namespace; the daemon never joins it.
func configureInside(pid int, peerName string, attachment *Attachment) error {
	ns, err := netns.GetFromPid(pid)
	if err != nil {
		return fmt.Errorf("open sandbox netns: %w", err)
	}
	defer func() { _ = ns.Close() }()

	handle, err := netlink.NewHandleAt(ns)
	if err != nil {
		return fmt.Errorf("netlink handle in sandbox netns: %w", err)
	}
	defer handle.Close()

	if err := linkUp(handle, "lo"); err != nil {
		return err
	}
	if attachment == nil {
		return nil
	}

	link, err := handle.LinkByName(peerName)
	if err != nil {
		return err
	}
	if err := handle.LinkSetName(link, "eth0"); err != nil {
		return fmt.Errorf("rename sandbox interface: %w", err)
	}
	addr := &netlink.Addr{IPNet: &net.IPNet{IP: attachment.IP, Mask: net.CIDRMask(attachment.PrefixLen, 32)}}
	if err := handle.AddrAdd(link, addr); err != nil {
		return fmt.Errorf("assign sandbox address: %w", err)
	}
	if err := handle.LinkSetUp(link); err != nil {
		return err
	}
	route := &netlink.Route{LinkIndex: link.Attrs().Index, Gw: attachment.Gateway}
	if err := handle.RouteAdd(route); err != nil {
		return fmt.Errorf("add default route: %w", err)
	}
	return nil
}

// LoopbackOnly brings up lo in a sandbox without egress.
func LoopbackOnly(pid int) error {
	return configureInside(pid, "", nil)
}

func linkUp(handle *netlink.Handle, name string) error {
	link, err := handle.LinkByName(name)
	if err != nil {
		return err
	}
	return handle.LinkSetUp(link)
}

// Detach removes the host veth (its peer dies with it) and frees the address.
func (m *Manager) Detach(attachment *Attachment) error {
	if attachment == nil {
		return nil
	}
	defer m.pool.release(attachment.IP)
	link, err := netlink.LinkByName(attachment.HostVeth)
	if err != nil {
		var notFound netlink.LinkNotFoundError
		if errors.As(err, &notFound) {
			return nil
		}
		return err
	}
	return netlink.LinkDel(link)
}

// DNS returns the resolvers written into sandbox resolv.conf.
func (m *Manager) DNS() []string {
	return m.cfg.DNS
}

// Teardown removes the nftables table and the bridge.
func (m *Manager) Teardown() error {
	return Cleanup(m.cfg.Bridge)
}

// Cleanup deletes leftover sandbox veths, the bridge, and the nftables table.
func Cleanup(bridgeName string) error {
	var errs []error
	links, err := netlink.LinkList()
	if err != nil {
		return err
	}
	for _, link := range links {
		if link.Type() == "veth" && strings.HasPrefix(link.Attrs().Name, VethPrefix) {
			if err := netlink.LinkDel(link); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if link, err := netlink.LinkByName(bridgeName); err == nil {
		if err := netlink.LinkDel(link); err != nil {
			errs = append(errs, err)
		}
	}

	conn, err := nftables.New()
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	table := &nftables.Table{Family: nftables.TableFamilyINet, Name: TableName}
	conn.AddTable(table)
	conn.DelTable(table)
	if err := conn.Flush(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// vethNames derives interface names (max 15 bytes) from the sandbox id.
func vethNames(sandboxID string) (string, string) {
	suffix := strings.TrimPrefix(sandboxID, "sbx-")
	if len(suffix) > 11 {
		suffix = suffix[:11]
	}
	return VethPrefix + "h" + suffix, VethPrefix + "p" + suffix
}

func ifname(name string) []byte {
	buf := make([]byte, ifNameSize)
	copy(buf, name)
	return buf
}

func concat(groups ...[]expr.Any) []expr.Any {
	var out []expr.Any
	for _, group := range groups {
		out = append(out, group...)
	}
	return out
}

func matchIfname(key expr.MetaKey, name []byte, op expr.CmpOp) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: key, Register: 1},
		&expr.Cmp{Op: op, Register: 1, Data: name},
	}
}

func matchIPv4() []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.NFPROTO_IPV4}},
	}
}

func matchNotIPv4() []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: []byte{unix.NFPROTO_IPV4}},
	}
}

// matchNetwork compares the IPv4 header field at offset (12 saddr, 16 daddr)
// against network.
func matchNetwork(offset uint32, network *net.IPNet) []expr.Any {
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: 4},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: []byte(network.Mask), Xor: []byte{0, 0, 0, 0}},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte(network.IP.To4())},
	}
}

func matchEstablished() []expr.Any {
	mask := make([]byte, 4)
	// Conntrack state bits are host-endian in the register.
	nativePutUint32(mask, expr.CtStateBitESTABLISHED|expr.CtStateBitRELATED)
	return []expr.Any{
		&expr.Ct{Register: 1, Key: expr.CtKeySTATE},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: mask, Xor: []byte{0, 0, 0, 0}},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: []byte{0, 0, 0, 0}},
	}
}

func verdict(kind expr.VerdictKind) []expr.Any {
	return []expr.Any{&expr.Verdict{Kind: kind}}
}
