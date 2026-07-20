// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"fmt"
	"net"
	"net/netip"
)

// isIPv6LinkLocal reports whether addr needs a zone to identify the interface
// it belongs to.
func isIPv6LinkLocal(addr netip.Addr) bool {
	return addr.Is6() && (addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast())
}

func addrWithOptionalZone(addr netip.Addr, zone string) netip.Addr {
	if zone == "" {
		return addr
	}
	if isIPv6LinkLocal(addr) {
		return addr.WithZone(zone)
	}

	return addr
}

// parseAddrFromIface should only be used when it's known the address belongs to that interface.
// e.g. it's LocalAddress on a listener.
func parseAddrFromIface(in net.Addr, ifcName string) (netip.Addr, int, NetworkType, error) {
	addr, port, nt, err := parseAddr(in)
	if err != nil {
		return netip.Addr{}, 0, 0, err
	}
	if _, ok := in.(*net.IPNet); ok {
		// net.IPNet does not have a Zone but we provide it from the interface
		addr = addrWithOptionalZone(addr, ifcName)
	}

	return addr, port, nt, nil
}

func parseAddr(in net.Addr) (netip.Addr, int, NetworkType, error) {
	host := func(ip net.IP, zone string) (netip.Addr, int, NetworkType, error) {
		a, err := ipAddrToNetIP(ip, zone)
		if err != nil {
			return netip.Addr{}, 0, 0, err
		}

		return a, 0, 0, nil
	}

	sock := func(ip net.IP, zone string, port int, v4, v6 NetworkType) (netip.Addr, int, NetworkType, error) {
		a, err := ipAddrToNetIP(ip, zone)
		if err != nil {
			return netip.Addr{}, 0, 0, err
		}

		nt := v6
		if a.Is4() {
			nt = v4
		}

		return a, port, nt, nil
	}

	switch a := in.(type) {
	case *net.IPNet:
		return host(a.IP, "")
	case *net.IPAddr:
		return host(a.IP, a.Zone)
	case *net.UDPAddr:
		return sock(a.IP, a.Zone, a.Port, NetworkTypeUDP4, NetworkTypeUDP6)
	case *net.TCPAddr:
		return sock(a.IP, a.Zone, a.Port, NetworkTypeTCP4, NetworkTypeTCP6)
	default:
		return netip.Addr{}, 0, 0, addrParseError{in}
	}
}

type addrParseError struct {
	addr net.Addr
}

func (e addrParseError) Error() string {
	return fmt.Sprintf("do not know how to parse address type %T", e.addr)
}

type ipConvertError struct {
	ip []byte
}

func (e ipConvertError) Error() string {
	return fmt.Sprintf("failed to convert IP '%s' to netip.Addr", e.ip)
}

func ipAddrToNetIP(ip []byte, zone string) (netip.Addr, error) {
	netIPAddr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return netip.Addr{}, ipConvertError{ip}
	}
	// we'd rather have an IPv4-mapped IPv6 become IPv4 so that it is usable.
	netIPAddr = netIPAddr.Unmap()
	netIPAddr = addrWithOptionalZone(netIPAddr, zone)

	return netIPAddr, nil
}

func createAddr(network NetworkType, ip netip.Addr, port int) net.Addr {
	switch {
	case network.IsTCP():
		return &net.TCPAddr{IP: ip.AsSlice(), Port: port, Zone: ip.Zone()}
	default:
		return &net.UDPAddr{IP: ip.AsSlice(), Port: port, Zone: ip.Zone()}
	}
}

// netAddrToAddrPort converts an address to netip.AddrPort. UDP and TCP
// addresses use their allocation-free conversions; other address types are
// parsed from their string representation. Invalid addresses and ports return
// the zero value.
func netAddrToAddrPort(addr net.Addr) netip.AddrPort {
	if addr == nil {
		return netip.AddrPort{}
	}
	switch a := addr.(type) {
	case *net.UDPAddr:
		if a == nil || !portFitsInUint16(a.Port) {
			return netip.AddrPort{}
		}

		return a.AddrPort()
	case *net.TCPAddr:
		if a == nil || !portFitsInUint16(a.Port) {
			return netip.AddrPort{}
		}

		return a.AddrPort()
	default:
		addrPort, err := netip.ParseAddrPort(addr.String())
		if err != nil {
			return netip.AddrPort{}
		}

		return addrPort
	}
}

func portFitsInUint16(port int) bool {
	return port >= 0 && port <= 0xFFFF
}

// AddrPortReaderWriter is an optional net.PacketConn capability that avoids
// allocating net.Addr values. Its methods must have the same packet-oriented
// semantics as net.PacketConn.ReadFrom and net.PacketConn.WriteTo, except that
// addresses are represented by netip.AddrPort. Custom UDP and framed TCP
// connections may implement it to opt in to the allocation-free path.
type AddrPortReaderWriter interface {
	ReadFromAddrPort(b []byte) (int, netip.AddrPort, error)
	WriteToAddrPort(b []byte, addr netip.AddrPort) (int, error)
}

// udpAddrPortReaderWriter adapts the allocation-free methods provided by a
// raw *net.UDPConn to the transport-neutral AddrPortReaderWriter interface.
type udpAddrPortReaderWriter struct {
	conn *net.UDPConn
}

func (c *udpAddrPortReaderWriter) ReadFromAddrPort(b []byte) (int, netip.AddrPort, error) {
	return c.conn.ReadFromUDPAddrPort(b)
}

func (c *udpAddrPortReaderWriter) WriteToAddrPort(b []byte, addr netip.AddrPort) (int, error) {
	return c.conn.WriteToUDPAddrPort(b, addr)
}

// asAddrPortReaderWriter checks whether conn implements the
// AddrPortReaderWriter interface and, if so, returns a reference to
// the AddrPort-capable connection. *net.UDPConn is automatically adapted
// to AddrPortReaderWriter. If conn does not support AddrPort
// reads and writes, nil is returned.
func asAddrPortReaderWriter(conn net.PacketConn) AddrPortReaderWriter {
	// Match only the concrete type so wrappers embedding *net.UDPConn do not
	// accidentally bypass their ReadFrom and WriteTo implementations.
	if udpConn, ok := conn.(*net.UDPConn); ok {
		return &udpAddrPortReaderWriter{conn: udpConn}
	}

	addrPortConn, _ := conn.(AddrPortReaderWriter)

	return addrPortConn
}

func addrEqual(a, b net.Addr) bool {
	aIP, aPort, aType, aErr := parseAddr(a)
	if aErr != nil {
		return false
	}

	bIP, bPort, bType, bErr := parseAddr(b)
	if bErr != nil {
		return false
	}

	return aType == bType && aIP.Compare(bIP) == 0 && aPort == bPort
}

// addrPortEqual compares IP and port using the same normalization as addrEqual.
func addrPortEqual(a, b netip.AddrPort) bool {
	return a.IsValid() && b.IsValid() && canonicalAddrPort(a) == canonicalAddrPort(b)
}

// canonicalAddr maps an address to a single representation for use as a map key
// or in equality comparisons: IPv4-in-IPv6 is unmapped to IPv4, and a zone is
// kept only where it identifies an interface (link-local IPv6).
func canonicalAddr(addr netip.Addr) netip.Addr {
	addr = addr.Unmap()
	if isIPv6LinkLocal(addr) {
		return addr
	}

	return addr.WithZone("")
}

// canonicalAddrPort applies canonicalAddr to the address so that the same
// transport address produces one key regardless of the IPv4/IPv4-in-IPv6 form
// it arrives in.
func canonicalAddrPort(ap netip.AddrPort) netip.AddrPort {
	return netip.AddrPortFrom(canonicalAddr(ap.Addr()), ap.Port())
}

// AddrPort is  an IP and a port number.
type AddrPort [18]byte

func toAddrPortKey(addr netip.AddrPort) AddrPort {
	var ap AddrPort
	if !addr.IsValid() {
		return ap
	}

	addr16 := addr.Addr().As16()
	copy(ap[:16], addr16[:])
	ap[16] = uint8(addr.Port() >> 8)
	ap[17] = uint8(addr.Port() & 0xFF)

	return ap
}
