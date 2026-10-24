package platform

import (
	"net"
	"net/netip"
)

type Connection struct{ Destination, IP, State string }

func remoteConnection(destination, state string) (Connection, bool) {
	host, _, err := net.SplitHostPort(destination)
	if err != nil {
		return Connection{}, false
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || ip.Unmap().IsLoopback() || ip.IsUnspecified() {
		return Connection{}, false
	}
	return Connection{destination, ip.Unmap().String(), state}, true
}
