// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build linux || darwin || freebsd

package ice

import (
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"runtime"

	"github.com/pion/transport/v5/packetio"
	"golang.org/x/sys/unix"
)

func newECNPacketReader(conn net.PacketConn) (packetReader, error) {
	// Match only raw sockets so promoted UDP methods cannot bypass wrappers.
	udp, ok := conn.(*net.UDPConn)
	if !ok {
		return nil, nil //nolint:nilnil
	}
	raw, err := udp.SyscallConn()
	if err != nil {
		return nil, err
	}

	// Try both families so dual-stack sockets can report ECN for either.
	var ipv4Err, ipv6Err error
	if err = raw.Control(func(fd uintptr) {
		ipv4Err = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_RECVTOS, 1)        //nolint:gosec // Unix file descriptors fit in int.
		ipv6Err = unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_RECVTCLASS, 1) //nolint:gosec // Unix file descriptors fit in int.
	}); err != nil {
		return nil, err
	}
	if ipv4Err != nil && ipv6Err != nil {
		return nil, errors.Join(ipv4Err, ipv6Err)
	}

	var oob [128]byte

	return func(buf []byte, attrs packetio.Attributes) (int, netip.AddrPort, packetio.Attributes, error) {
		n, oobn, flags, addr, readErr := udp.ReadMsgUDPAddrPort(buf, oob[:])
		if readErr == nil && flags&unix.MSG_CTRUNC == 0 {
			if ecn, present := parseECN(oob[:oobn]); present {
				attrs.Set(ecnAttributeKey, ecn)
			}
		}

		return n, addr, attrs, readErr
	}, nil
}

func parseECN(oob []byte) (ECN, bool) {
	ipv4Type := int32(unix.IP_TOS)
	if runtime.GOOS != "linux" {
		ipv4Type = unix.IP_RECVTOS
	}
	// ParseOneSocketControlMessage requires a complete header.
	for len(oob) >= unix.CmsgLen(0) {
		header, data, rest, err := unix.ParseOneSocketControlMessage(oob)
		if err != nil {
			break
		}
		oob = rest
		switch {
		case header.Level == unix.IPPROTO_IP && header.Type == ipv4Type && len(data) == 1:
			return ECN(data[0] & 0x03), true
		case header.Level == unix.IPPROTO_IPV6 && header.Type == unix.IPV6_TCLASS && len(data) == 4:
			return ECN(binary.NativeEndian.Uint32(data) & 0x03), true
		}
	}

	return 0, false
}
