// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"net"

	"github.com/google/uuid"
	"github.com/pion/logging"
	"github.com/pion/mdns/v2"
	"github.com/pion/transport/v3"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// MulticastDNSMode represents the different Multicast modes ICE can run in
type MulticastDNSMode byte

// MulticastDNSMode enum
const (
	// MulticastDNSModeDisabled means remote mDNS candidates will be discarded, and local host candidates will use IPs
	MulticastDNSModeDisabled MulticastDNSMode = iota + 1

	// MulticastDNSModeQueryOnly means remote mDNS candidates will be accepted, and local host candidates will use IPs
	MulticastDNSModeQueryOnly

	// MulticastDNSModeQueryAndGather means remote mDNS candidates will be accepted, and local host candidates will use mDNS
	MulticastDNSModeQueryAndGather
)

func generateMulticastDNSName() (string, error) {
	// https://tools.ietf.org/id/draft-ietf-rtcweb-mdns-ice-candidates-02.html#gathering
	// The unique name MUST consist of a version 4 UUID as defined in [RFC4122], followed by “.local”.
	u, err := uuid.NewRandom()
	return u.String() + ".local", err
}

func createMulticastListener(n transport.Net, network, address string) (net.PacketConn, error) {
	addr, err := n.ResolveUDPAddr(network, address)
	if err != nil {
		return nil, err
	}

	return n.ListenUDP(network, addr)
}

func createMulticastDNS(n transport.Net, mDNSMode MulticastDNSMode, mDNSName string, networkTypes []NetworkType, log logging.LeveledLogger) (*mdns.Conn, MulticastDNSMode, error) {
	if mDNSMode == MulticastDNSModeDisabled {
		return nil, mDNSMode, nil
	}

	var (
		pktConnIPV4              *ipv4.PacketConn
		pktConnIPV6              *ipv6.PacketConn
		ipV4Enabled, ipV6Enabled bool
	)

	for _, n := range networkTypes {
		if n == NetworkTypeUDP4 {
			ipV4Enabled = true
		} else if n == NetworkTypeUDP6 {
			ipV6Enabled = true
		}
	}

	if ipV4Enabled {
		l, err := createMulticastListener(n, "udp4", mdns.DefaultAddressIPv4)
		if err != nil {
			log.Errorf("Failed to enable IPv4 mDNS (%s)", err)
		} else {
			pktConnIPV4 = ipv4.NewPacketConn(l)
		}
	}

	if ipV6Enabled {
		l, err := createMulticastListener(n, "udp6", mdns.DefaultAddressIPv6)
		if err != nil {
			log.Errorf("Failed to enable IPv6 mDNS (%s)", err)
		} else {
			pktConnIPV6 = ipv6.NewPacketConn(l)
		}
	}

	if pktConnIPV4 == nil && pktConnIPV6 == nil {
		log.Errorf("Failed to enable IPv4 or IPv6 mDNS, continuing in mDNS disabled mode")
		return nil, MulticastDNSModeDisabled, nil
	}

	switch mDNSMode {
	case MulticastDNSModeQueryOnly:
		conn, err := mdns.Server(pktConnIPV4, pktConnIPV6, &mdns.Config{})
		return conn, mDNSMode, err
	case MulticastDNSModeQueryAndGather:
		conn, err := mdns.Server(pktConnIPV4, pktConnIPV6, &mdns.Config{
			LocalNames: []string{mDNSName},
		})
		return conn, mDNSMode, err
	default:
		return nil, mDNSMode, nil
	}
}
