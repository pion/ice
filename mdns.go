// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"net"
	"reflect"
	"slices"

	"github.com/google/uuid"
	"github.com/pion/logging"
	"github.com/pion/mdns/v2"
	"github.com/pion/transport/v5"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// MulticastDNSMode represents the different Multicast modes ICE can run in.
type MulticastDNSMode byte

// MulticastDNSMode enum.
const (
	// MulticastDNSModeDisabled means remote mDNS candidates will be discarded, and local host candidates will use IPs.
	MulticastDNSModeDisabled MulticastDNSMode = iota + 1

	// MulticastDNSModeQueryOnly means remote mDNS candidates will be accepted, and local host candidates will use IPs.
	MulticastDNSModeQueryOnly

	// MulticastDNSModeQueryAndGather means remote mDNS candidates will be accepted,
	// and local host candidates will use mDNS.
	MulticastDNSModeQueryAndGather
)

// multicastDNSNetwork records the network used by the current mDNS connection.
type multicastDNSNetwork struct {
	useIPv4      bool
	useIPv6      bool
	interfaces   []net.Interface
	localAddrs   []ifaceAddr
	localAddress net.IP
}

func (a *Agent) updateMulticastDNS(
	networkTypes []NetworkType, interfaces []*transport.Interface, localAddrs []ifaceAddr,
) MulticastDNSMode {
	network := multicastDNSNetwork{
		localAddrs:   slices.Clone(localAddrs),
		localAddress: slices.Clone(mDNSLocalAddressFromTCPMux(a.tcpMux, networkTypes)),
	}
	// mDNS always uses UDP. changing ICE transports alone must not restart.
	network.useIPv4, network.useIPv6 = multicastDNSIPFamilies(networkTypes)
	for _, iface := range interfaces {
		ifc := iface.Interface
		ifc.HardwareAddr = slices.Clone(ifc.HardwareAddr)
		network.interfaces = append(network.interfaces, ifc)
	}
	if a.mDNSConn != nil && reflect.DeepEqual(a.mDNSNetwork, network) {
		return a.mDNSMode
	}

	a.closeMulticastConn()
	a.mDNSNetwork = network
	if a.mDNSMode == MulticastDNSModeDisabled || len(interfaces) == 0 {
		return MulticastDNSModeDisabled
	}

	var err error
	a.mDNSConn, _, err = createMulticastDNS(
		a.net, networkTypes, interfaces, a.includeLoopback, network.localAddress,
		a.mDNSMode, a.mDNSName, a.log, a.loggerFactory,
	)
	if err != nil {
		a.log.Warnf("Failed to initialize mDNS %s: %v", a.mDNSName, err)
		a.closeMulticastConn()
	}
	if a.mDNSConn == nil {
		return MulticastDNSModeDisabled
	}

	return a.mDNSMode
}

func generateMulticastDNSName() (string, error) {
	// https://tools.ietf.org/id/draft-ietf-rtcweb-mdns-ice-candidates-02.html#gathering
	// The unique name MUST consist of a version 4 UUID as defined in [RFC4122], followed by “.local”.
	u, err := uuid.NewRandom()

	return u.String() + ".local", err
}

func multicastDNSIPFamilies(networkTypes []NetworkType) (useIPv4, useIPv6 bool) {
	for _, networkType := range configuredNetworkTypes(networkTypes) {
		useIPv4 = useIPv4 || networkType.IsIPv4()
		useIPv6 = useIPv6 || networkType.IsIPv6()
	}

	return useIPv4, useIPv6
}

//nolint:cyclop
func createMulticastDNS(
	netTransport transport.Net,
	networkTypes []NetworkType,
	interfaces []*transport.Interface,
	includeLoopback bool,
	localAddress net.IP,
	mDNSMode MulticastDNSMode,
	mDNSName string,
	log logging.LeveledLogger,
	loggerFactory logging.LoggerFactory,
) (*mdns.Conn, MulticastDNSMode, error) {
	if mDNSMode == MulticastDNSModeDisabled {
		return nil, mDNSMode, nil
	}

	useV4, useV6 := multicastDNSIPFamilies(networkTypes)

	addr4, mdnsErr := netTransport.ResolveUDPAddr("udp4", mdns.DefaultAddressIPv4)
	if mdnsErr != nil {
		return nil, mDNSMode, mdnsErr
	}
	addr6, mdnsErr := netTransport.ResolveUDPAddr("udp6", mdns.DefaultAddressIPv6)
	if mdnsErr != nil {
		return nil, mDNSMode, mdnsErr
	}

	var pktConnV4 *ipv4.PacketConn
	var mdns4Err error
	if useV4 {
		var l transport.UDPConn
		l, mdns4Err = netTransport.ListenUDP("udp4", addr4)
		if mdns4Err != nil {
			// If ICE fails to start MulticastDNS server just warn the user and continue
			log.Errorf("Failed to enable mDNS over IPv4: (%s)", mdns4Err)

			return nil, MulticastDNSModeDisabled, nil
		}
		pktConnV4 = ipv4.NewPacketConn(l)
	}

	started := false
	defer func() {
		if !started && pktConnV4 != nil {
			_ = pktConnV4.Close()
		}
	}()
	var pktConnV6 *ipv6.PacketConn
	defer func() {
		if !started && pktConnV6 != nil {
			_ = pktConnV6.Close()
		}
	}()
	var mdns6Err error
	if useV6 {
		var l transport.UDPConn
		l, mdns6Err = netTransport.ListenUDP("udp6", addr6)
		if mdns6Err != nil {
			log.Errorf("Failed to enable mDNS over IPv6: (%s)", mdns6Err)

			return nil, MulticastDNSModeDisabled, nil
		}
		pktConnV6 = ipv6.NewPacketConn(l)
	}

	if mdns4Err != nil && mdns6Err != nil {
		// If ICE fails to start MulticastDNS server just warn the user and continue
		log.Errorf("Failed to enable mDNS, continuing in mDNS disabled mode")
		//nolint:nilerr
		return nil, MulticastDNSModeDisabled, nil
	}
	var ifcs []net.Interface
	if interfaces != nil {
		ifcs = make([]net.Interface, 0, len(ifcs))
		for _, ifc := range interfaces {
			ifcs = append(ifcs, ifc.Interface)
		}
	}

	switch mDNSMode {
	case MulticastDNSModeQueryOnly:
		//nolint:staticcheck // NewServer is unavailable in the pinned mDNS version.
		conn, err := mdns.Server(pktConnV4, pktConnV6, &mdns.Config{
			Interfaces:      ifcs,
			IncludeLoopback: includeLoopback,
			LocalAddress:    localAddress,
			LoggerFactory:   loggerFactory,
		})

		started = err == nil

		return conn, mDNSMode, err
	case MulticastDNSModeQueryAndGather:
		//nolint:staticcheck // NewServer is unavailable in the pinned mDNS version.
		conn, err := mdns.Server(pktConnV4, pktConnV6, &mdns.Config{
			Interfaces:      ifcs,
			IncludeLoopback: includeLoopback,
			LocalAddress:    localAddress,
			LocalNames:      []string{mDNSName},
			LoggerFactory:   loggerFactory,
		})

		started = err == nil

		return conn, mDNSMode, err
	default:
		return nil, mDNSMode, nil
	}
}
