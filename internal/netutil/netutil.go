// Package netutil finds the LAN addresses other devices can use to reach us.
package netutil

import (
	"net"
	"sort"
	"strings"
)

// Names of interfaces that are almost never the shared Wi-Fi / hotspot link.
var virtualPrefixes = []string{
	"docker", "br-", "veth", "virbr", "vmnet", "vboxnet", "utun", "tun", "tap",
	"zt", "tailscale", "wg", "vethernet", "lxc", "cni", "flannel", "podman",
}

// LANIPv4 returns the machine's private IPv4 addresses, most likely
// Wi-Fi/hotspot address first.
func LANIPv4() []net.IP {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var ips []net.IP
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || isVirtual(iface.Name) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ip4 := ipnet.IP.To4(); ip4 != nil && ip4.IsPrivate() {
				ips = append(ips, ip4)
			}
		}
	}
	sort.SliceStable(ips, func(i, j int) bool { return rank(ips[i]) < rank(ips[j]) })
	return ips
}

func isVirtual(name string) bool {
	n := strings.ToLower(name)
	for _, p := range virtualPrefixes {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

// rank prefers the ranges home routers and phone hotspots typically hand out.
func rank(ip net.IP) int {
	switch {
	case ip[0] == 192 && ip[1] == 168:
		return 0
	case ip[0] == 172 && ip[1] == 20 && ip[2] == 10: // iPhone personal hotspot
		return 0
	case ip[0] == 10:
		return 1
	default:
		return 2
	}
}
