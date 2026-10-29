// Package netns wires sandbox network namespaces to the host: a bridge with
// NAT for egress, one isolated veth port per sandbox, and an nftables policy
// that blocks private, link-local, and host destinations.
package netns
