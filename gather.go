// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/netip"
	"reflect"
	"sync"

	"github.com/pion/dtls/v3"
	"github.com/pion/ice/v4/internal/fakenet"
	stunx "github.com/pion/ice/v4/internal/stun"
	"github.com/pion/logging"
	"github.com/pion/stun/v3"
	"github.com/pion/turn/v4"
)

// Close a net.Conn and log if we have a failure.
func closeConnAndLog(c io.Closer, log logging.LeveledLogger, msg string, args ...any) {
	if c == nil || (reflect.ValueOf(c).Kind() == reflect.Ptr && reflect.ValueOf(c).IsNil()) {
		log.Warnf("Connection is not allocated: "+msg, args...)

		return
	}

	log.Warnf(msg, args...)
	if err := c.Close(); err != nil {
		log.Warnf("Failed to close connection: %v", err)
	}
}

// GatherCandidates initiates the trickle based gathering process.
func (a *Agent) GatherCandidates() error {
	var gatherErr error

	if runErr := a.loop.Run(a.loop, func(ctx context.Context) {
		if a.gatheringState != GatheringStateNew {
			gatherErr = ErrMultipleGatherAttempted

			return
		} else if a.onCandidateHdlr.Load() == nil {
			gatherErr = ErrNoOnCandidateHandler

			return
		}

		a.gatherCandidateCancel() // Cancel previous gathering routine
		ctx, cancel := context.WithCancel(ctx)
		a.gatherCandidateCancel = cancel
		done := make(chan struct{})
		a.gatherCandidateDone = done

		go a.gatherCandidates(ctx, done)
	}); runErr != nil {
		return runErr
	}

	return gatherErr
}

func (a *Agent) gatherCandidates(ctx context.Context, done chan struct{}) { //nolint:cyclop
	defer close(done)
	if err := a.setGatheringState(GatheringStateGathering); err != nil { //nolint:contextcheck
		a.log.Warnf("Failed to set gatheringState to GatheringStateGathering: %v", err)

		return
	}

	var wg sync.WaitGroup
	for _, t := range a.candidateTypes {
		switch t {
		case CandidateTypeHost:
			wg.Add(1)
			go func() {
				a.gatherCandidatesLocal(ctx, a.networkTypes)
				wg.Done()
			}()
		case CandidateTypeServerReflexive:
			wg.Add(1)
			go func() {
				if a.udpMuxSrflx != nil {
					a.gatherCandidatesSrflxUDPMux(ctx, a.urls, a.networkTypes)
				} else {
					a.gatherCandidatesSrflx(ctx, a.urls, a.networkTypes)
				}
				wg.Done()
			}()
			if a.extIPMapper != nil && a.extIPMapper.candidateType == CandidateTypeServerReflexive {
				wg.Add(1)
				go func() {
					a.gatherCandidatesSrflxMapped(ctx, a.networkTypes)
					wg.Done()
				}()
			}
		case CandidateTypeRelay:
			wg.Add(1)
			go func() {
				a.gatherCandidatesRelay(ctx, a.urls)
				wg.Done()
			}()
		case CandidateTypePeerReflexive, CandidateTypeUnspecified:
		}
	}

	// Block until all STUN and TURN URLs have been gathered (or timed out)
	wg.Wait()

	if err := a.setGatheringState(GatheringStateComplete); err != nil { //nolint:contextcheck
		a.log.Warnf("Failed to set gatheringState to GatheringStateComplete: %v", err)
	}
}

//nolint:gocognit,gocyclo,cyclop
func (a *Agent) gatherCandidatesLocal(ctx context.Context, networkTypes []NetworkType) {
	networks := map[string]struct{}{}
	for _, networkType := range networkTypes {
		if networkType.IsTCP() {
			networks[tcp] = struct{}{}
		} else {
			networks[udp] = struct{}{}
		}
	}

	// When UDPMux is enabled, skip other UDP candidates
	if a.udpMux != nil {
		if err := a.gatherCandidatesLocalUDPMux(ctx); err != nil {
			a.log.Warnf("Failed to create host candidate for UDPMux: %s", err)
		}
		delete(networks, udp)
	}

	_, localAddrs, err := localInterfaces(a.net, a.interfaceFilter, a.ipFilter, networkTypes, a.includeLoopback)
	if err != nil {
		a.log.Warnf("Failed to iterate local interfaces, host candidates will not be gathered %s", err)

		return
	}

	for _, addr := range localAddrs {
		mappedIP := addr
		if a.mDNSMode != MulticastDNSModeQueryAndGather &&
			a.extIPMapper != nil && a.extIPMapper.candidateType == CandidateTypeHost {
			if _mappedIP, innerErr := a.extIPMapper.findExternalIP(addr.String()); innerErr == nil {
				conv, ok := netip.AddrFromSlice(_mappedIP)
				if !ok {
					a.log.Warnf("failed to convert mapped external IP to netip.Addr'%s'", addr.String())

					continue
				}
				// we'd rather have an IPv4-mapped IPv6 become IPv4 so that it is usable
				mappedIP = conv.Unmap()
			} else {
				a.log.Warnf("1:1 NAT mapping is enabled but no external IP is found for %s", addr.String())
			}
		}

		address := mappedIP.String()
		var isLocationTracked bool
		if a.mDNSMode == MulticastDNSModeQueryAndGather {
			address = a.mDNSName
		} else {
			// Here, we are not doing multicast gathering, so we will need to skip this address so
			// that we don't accidentally reveal location tracking information. Otherwise, the
			// case above hides the IP behind an mDNS address.
			isLocationTracked = shouldFilterLocationTrackedIP(mappedIP)
		}

		for network := range networks {
			type connAndPort struct {
				conn net.PacketConn
				port int
			}
			var (
				conns   []connAndPort
				tcpType TCPType
			)

			switch network {
			case tcp:
				if a.tcpMux == nil {
					continue
				}

				// Handle ICE TCP passive mode
				var muxConns []net.PacketConn
				if multi, ok := a.tcpMux.(AllConnsGetter); ok {
					a.log.Debugf("GetAllConns by ufrag: %s", a.localUfrag)
					// Note: this is missing zone for IPv6 by just grabbing the IP slice
					muxConns, err = multi.GetAllConns(a.localUfrag, mappedIP.Is6(), addr.AsSlice())
					if err != nil {
						a.log.Warnf("Failed to get all TCP connections by ufrag: %s %s %s", network, addr, a.localUfrag)

						continue
					}
				} else {
					a.log.Debugf("GetConn by ufrag: %s", a.localUfrag)
					// Note: this is missing zone for IPv6 by just grabbing the IP slice
					conn, err := a.tcpMux.GetConnByUfrag(a.localUfrag, mappedIP.Is6(), addr.AsSlice())
					if err != nil {
						a.log.Warnf("Failed to get TCP connections by ufrag: %s %s %s", network, addr, a.localUfrag)

						continue
					}
					muxConns = []net.PacketConn{conn}
				}

				// Extract the port for each PacketConn we got.
				for _, conn := range muxConns {
					if tcpConn, ok := conn.LocalAddr().(*net.TCPAddr); ok {
						conns = append(conns, connAndPort{conn, tcpConn.Port})
					} else {
						a.log.Warnf("Failed to get port of connection from TCPMux: %s %s %s", network, addr, a.localUfrag)
					}
				}
				if len(conns) == 0 {
					// Didn't succeed with any, try the next network.
					continue
				}
				tcpType = TCPTypePassive
				// Is there a way to verify that the listen address is even
				// accessible from the current interface.
			case udp:
				conn, err := listenUDPInPortRange(a.net, a.log, int(a.portMax), int(a.portMin), network, &net.UDPAddr{
					IP:   addr.AsSlice(),
					Port: 0,
					Zone: addr.Zone(),
				})
				if err != nil {
					a.log.Warnf("Failed to listen %s %s", network, addr)

					continue
				}

				if udpConn, ok := conn.LocalAddr().(*net.UDPAddr); ok {
					conns = append(conns, connAndPort{conn, udpConn.Port})
				} else {
					a.log.Warnf("Failed to get port of UDPAddr from ListenUDPInPortRange: %s %s %s", network, addr, a.localUfrag)

					continue
				}
			}

			for _, connAndPort := range conns {
				var hostConfigs []CandidateHostConfig
				if network == udp && a.hostUDPAdvertisedAddrsMapper != nil && a.mDNSMode != MulticastDNSModeQueryAndGather {
					advertisedAddrs := a.hostUDPAdvertisedAddrsMapper(connAndPort.conn.LocalAddr().(*net.UDPAddr))
					for _, advertisedAddr := range advertisedAddrs {
						hostConfigs = append(hostConfigs, CandidateHostConfig{
							Network:           network,
							Address:           advertisedAddr.IP.String(),
							Port:              advertisedAddr.Port,
							Component:         ComponentRTP,
							TCPType:           tcpType,
							IsLocationTracked: shouldFilterLocationTracked(advertisedAddr.IP),
						})
					}
				} else if network == tcp && a.hostTCPAdvertisedAddrsMapper != nil && a.mDNSMode != MulticastDNSModeQueryAndGather {
					advertisedAddrs := a.hostTCPAdvertisedAddrsMapper(connAndPort.conn.LocalAddr().(*net.TCPAddr))
					for _, advertisedAddr := range advertisedAddrs {
						hostConfigs = append(hostConfigs, CandidateHostConfig{
							Network:           network,
							Address:           advertisedAddr.IP.String(),
							Port:              advertisedAddr.Port,
							Component:         ComponentRTP,
							TCPType:           tcpType,
							IsLocationTracked: shouldFilterLocationTracked(advertisedAddr.IP),
						})
					}
				} else {
					hostConfigs = append(hostConfigs, CandidateHostConfig{
						Network:   network,
						Address:   address,
						Port:      connAndPort.port,
						Component: ComponentRTP,
						TCPType:   tcpType,
						// we will still process this candidate so that we start up the right
						// listeners.
						IsLocationTracked: isLocationTracked,
					})
				}

				if len(hostConfigs) == 0 {
					closeConnAndLog(connAndPort.conn,
						a.log,
						"gatherCandidatesLocal: no host configs allowed for %s %s %d",
						network,
						mappedIP,
						connAndPort.port)

					continue
				}

				var shared *sharedPacketConn
				if len(hostConfigs) > 1 {
					shared = newSharedPacketConn(connAndPort.conn)
				}

				for i := range hostConfigs {
					useConn := connAndPort.conn
					if shared != nil {
						useConn = shared.Ref()
					}

					candidateHost, err := NewCandidateHost(&hostConfigs[i])
					if err != nil {
						failedAddr := hostConfigs[i].Address
						if a.mDNSMode == MulticastDNSModeQueryAndGather {
							failedAddr = mappedIP.String()
						}
						closeConnAndLog(
							useConn,
							a.log,
							"failed to create host candidate: %s %s %d: %v",
							network, failedAddr,
							hostConfigs[i].Port,
							err,
						)

						continue
					}

					if a.mDNSMode == MulticastDNSModeQueryAndGather {
						if err = candidateHost.setIPAddr(addr); err != nil {
							closeConnAndLog(
								useConn,
								a.log,
								"failed to create host candidate: %s %s %d: %v",
								network,
								mappedIP,
								connAndPort.port,
								err,
							)

							continue
						}
					}

					if err := a.addCandidate(ctx, candidateHost, useConn); err != nil {
						if closeErr := candidateHost.close(); closeErr != nil {
							a.log.Warnf("Failed to close candidate: %v", closeErr)
						}
						a.log.Warnf("Failed to append to localCandidates and run onCandidateHdlr: %v", err)
					}
				}

				if shared != nil {
					shared.Release()
				}
			}
		}
	}
}

// shouldFilterLocationTrackedIP returns if this candidate IP should be filtered out from
// any candidate publishing/notification for location tracking reasons.
func shouldFilterLocationTrackedIP(candidateIP netip.Addr) bool {
	// https://tools.ietf.org/html/rfc8445#section-5.1.1.1
	// Similarly, when host candidates corresponding to
	// an IPv6 address generated using a mechanism that prevents location
	// tracking are gathered, then host candidates corresponding to IPv6
	// link-local addresses [RFC4291] MUST NOT be gathered.
	return candidateIP.Is6() && (candidateIP.IsLinkLocalUnicast() || candidateIP.IsLinkLocalMulticast())
}

// shouldFilterLocationTracked returns if this candidate IP should be filtered out from
// any candidate publishing/notification for location tracking reasons.
func shouldFilterLocationTracked(candidateIP net.IP) bool {
	addr, ok := netip.AddrFromSlice(candidateIP)
	if !ok {
		return false
	}

	return shouldFilterLocationTrackedIP(addr)
}

func (a *Agent) gatherCandidatesLocalUDPMux(ctx context.Context) error { //nolint:gocognit,cyclop
	if a.udpMux == nil {
		return errUDPMuxDisabled
	}

	localAddresses := a.udpMux.GetListenAddresses()
	existingConfigs := make(map[CandidateHostConfig]struct{})

	for _, addr := range localAddresses {
		udpAddr, ok := addr.(*net.UDPAddr)
		if !ok {
			return errInvalidAddress
		}
		candidateIP := udpAddr.IP

		if _, ok := a.udpMux.(*UDPMuxDefault); ok && !a.includeLoopback && candidateIP.IsLoopback() {
			// Unlike MultiUDPMux Default, UDPMuxDefault doesn't have
			// a separate param to include loopback, so we respect agent config
			continue
		}

		if a.mDNSMode != MulticastDNSModeQueryAndGather &&
			a.extIPMapper != nil &&
			a.extIPMapper.candidateType == CandidateTypeHost {
			mappedIP, err := a.extIPMapper.findExternalIP(candidateIP.String())
			if err != nil {
				a.log.Warnf("1:1 NAT mapping is enabled but no external IP is found for %s", candidateIP.String())

				continue
			}

			candidateIP = mappedIP
		}

		var hostConfigs []CandidateHostConfig

		if a.mDNSMode == MulticastDNSModeQueryAndGather {
			hostConfigs = append(hostConfigs, CandidateHostConfig{
				Network:   udp,
				Address:   a.mDNSName,
				Port:      udpAddr.Port,
				Component: ComponentRTP,
			})
		} else if a.hostUDPAdvertisedAddrsMapper != nil {
			adv := a.hostUDPAdvertisedAddrsMapper(udpAddr)
			for _, advertisedAddr := range adv {
				hostConfigs = append(hostConfigs, CandidateHostConfig{
					Network:           udp,
					Address:           advertisedAddr.IP.String(),
					Port:              advertisedAddr.Port,
					Component:         ComponentRTP,
					IsLocationTracked: shouldFilterLocationTracked(advertisedAddr.IP),
				})
			}
		} else {
			hostConfigs = append(hostConfigs, CandidateHostConfig{
				Network:           udp,
				Address:           candidateIP.String(),
				Port:              udpAddr.Port,
				Component:         ComponentRTP,
				IsLocationTracked: shouldFilterLocationTracked(candidateIP),
			})
		}

		if len(hostConfigs) == 0 {
			continue
		}

		conn, err := a.udpMux.GetConn(a.localUfrag, udpAddr)
		if err != nil {
			return err
		}

		var shared *sharedPacketConn
		if len(hostConfigs) > 1 {
			shared = newSharedPacketConn(conn)
		}

		for i := range hostConfigs {
			if _, ok := existingConfigs[hostConfigs[i]]; ok {
				continue
			}

			useConn := conn
			if shared != nil {
				useConn = shared.Ref()
			}

			candidateHost, err := NewCandidateHost(&hostConfigs[i])
			if err != nil {
				closeConnAndLog(useConn,
					a.log,
					"failed to create host mux candidate: %s %d: %v",
					hostConfigs[i].Address,
					hostConfigs[i].Port,
					err,
				)

				continue
			}

			if err := a.addCandidate(ctx, candidateHost, useConn); err != nil {
				if closeErr := candidateHost.close(); closeErr != nil {
					a.log.Warnf("Failed to close candidate: %v", closeErr)
				}
				closeConnAndLog(
					useConn,
					a.log,
					"failed to add candidate: %s %d: %v",
					hostConfigs[i].Address,
					hostConfigs[i].Port,
					err,
				)

				continue
			}

			existingConfigs[hostConfigs[i]] = struct{}{}
		}

		if shared != nil {
			shared.Release()
		}
	}

	return nil
}

func (a *Agent) gatherCandidatesSrflxMapped(ctx context.Context, networkTypes []NetworkType) {
	var wg sync.WaitGroup
	defer wg.Wait()

	for _, networkType := range networkTypes {
		if networkType.IsTCP() {
			continue
		}

		network := networkType.String()
		wg.Add(1)
		go func() {
			defer wg.Done()

			conn, err := listenUDPInPortRange(
				a.net,
				a.log,
				int(a.portMax),
				int(a.portMin),
				network,
				&net.UDPAddr{IP: nil, Port: 0},
			)
			if err != nil {
				a.log.Warnf("Failed to listen %s: %v", network, err)

				return
			}

			lAddr, ok := conn.LocalAddr().(*net.UDPAddr)
			if !ok {
				closeConnAndLog(conn, a.log, "1:1 NAT mapping is enabled but LocalAddr is not a UDPAddr")

				return
			}

			mappedIP, err := a.extIPMapper.findExternalIP(lAddr.IP.String())
			if err != nil {
				closeConnAndLog(conn, a.log, "1:1 NAT mapping is enabled but no external IP is found for %s", lAddr.IP.String())

				return
			}

			if shouldFilterLocationTracked(mappedIP) {
				closeConnAndLog(conn, a.log, "external IP is somehow filtered for location tracking reasons %s", mappedIP)

				return
			}

			srflxConfig := CandidateServerReflexiveConfig{
				Network:   network,
				Address:   mappedIP.String(),
				Port:      lAddr.Port,
				Component: ComponentRTP,
				RelAddr:   lAddr.IP.String(),
				RelPort:   lAddr.Port,
			}
			c, err := NewCandidateServerReflexive(&srflxConfig)
			if err != nil {
				closeConnAndLog(conn, a.log, "failed to create server reflexive candidate: %s %s %d: %v",
					network,
					mappedIP.String(),
					lAddr.Port,
					err)

				return
			}

			if err := a.addCandidate(ctx, c, conn); err != nil {
				if closeErr := c.close(); closeErr != nil {
					a.log.Warnf("Failed to close candidate: %v", closeErr)
				}
				a.log.Warnf("Failed to append to localCandidates and run onCandidateHdlr: %v", err)
			}
		}()
	}
}

//nolint:gocognit,cyclop
func (a *Agent) gatherCandidatesSrflxUDPMux(ctx context.Context, urls []*stun.URI, networkTypes []NetworkType) {
	var wg sync.WaitGroup
	defer wg.Wait()

	for _, networkType := range networkTypes {
		if networkType.IsTCP() {
			continue
		}

		for i := range urls {
			for _, listenAddr := range a.udpMuxSrflx.GetListenAddresses() {
				udpAddr, ok := listenAddr.(*net.UDPAddr)
				if !ok {
					a.log.Warn("Failed to cast udpMuxSrflx listen address to UDPAddr")

					continue
				}
				wg.Add(1)
				go func(url stun.URI, network string, localAddr *net.UDPAddr) {
					defer wg.Done()

					hostPort := fmt.Sprintf("%s:%d", url.Host, url.Port)
					serverAddr, err := a.net.ResolveUDPAddr(network, hostPort)
					if err != nil {
						a.log.Debugf("Failed to resolve STUN host: %s %s: %v", network, hostPort, err)

						return
					}

					if shouldFilterLocationTracked(serverAddr.IP) {
						a.log.Warnf("STUN host %s is somehow filtered for location tracking reasons", hostPort)

						return
					}

					xorAddr, err := a.udpMuxSrflx.GetXORMappedAddr(serverAddr, a.stunGatherTimeout)
					if err != nil {
						a.log.Warnf("Failed get server reflexive address %s %s: %v", network, url, err)

						return
					}

					conn, err := a.udpMuxSrflx.GetConnForURL(a.localUfrag, url.String(), localAddr)
					if err != nil {
						a.log.Warnf("Failed to find connection in UDPMuxSrflx %s %s: %v", network, url, err)

						return
					}

					ip := xorAddr.IP
					port := xorAddr.Port

					srflxConfig := CandidateServerReflexiveConfig{
						Network:   network,
						Address:   ip.String(),
						Port:      port,
						Component: ComponentRTP,
						RelAddr:   localAddr.IP.String(),
						RelPort:   localAddr.Port,
					}
					c, err := NewCandidateServerReflexive(&srflxConfig)
					if err != nil {
						closeConnAndLog(conn, a.log, "failed to create server reflexive candidate: %s %s %d: %v", network, ip, port, err)

						return
					}

					if err := a.addCandidate(ctx, c, conn); err != nil {
						if closeErr := c.close(); closeErr != nil {
							a.log.Warnf("Failed to close candidate: %v", closeErr)
						}
						a.log.Warnf("Failed to append to localCandidates and run onCandidateHdlr: %v", err)
					}
				}(*urls[i], networkType.String(), udpAddr)
			}
		}
	}
}

//nolint:cyclop,gocognit
func (a *Agent) gatherCandidatesSrflx(ctx context.Context, urls []*stun.URI, networkTypes []NetworkType) {
	var wg sync.WaitGroup
	defer wg.Wait()

	for _, networkType := range networkTypes {
		if networkType.IsTCP() {
			continue
		}

		for i := range urls {
			wg.Add(1)
			go func(url stun.URI, network string) {
				defer wg.Done()

				hostPort := fmt.Sprintf("%s:%d", url.Host, url.Port)
				serverAddr, err := a.net.ResolveUDPAddr(network, hostPort)
				if err != nil {
					a.log.Debugf("Failed to resolve STUN host: %s %s: %v", network, hostPort, err)

					return
				}

				if shouldFilterLocationTracked(serverAddr.IP) {
					a.log.Warnf("STUN host %s is somehow filtered for location tracking reasons", hostPort)

					return
				}

				conn, err := listenUDPInPortRange(
					a.net,
					a.log,
					int(a.portMax),
					int(a.portMin),
					network,
					&net.UDPAddr{IP: nil, Port: 0},
				)
				if err != nil {
					closeConnAndLog(conn, a.log, "failed to listen for %s: %v", serverAddr.String(), err)

					return
				}
				// If the agent closes midway through the connection
				// we end it early to prevent close delay.
				cancelCtx, cancelFunc := context.WithCancel(ctx)
				defer cancelFunc()
				go func() {
					select {
					case <-cancelCtx.Done():
						return
					case <-a.loop.Done():
						_ = conn.Close()
					}
				}()

				xorAddr, err := stunx.GetXORMappedAddr(conn, serverAddr, a.stunGatherTimeout)
				if err != nil {
					closeConnAndLog(conn, a.log, "failed to get server reflexive address %s %s: %v", network, url, err)

					return
				}

				ip := xorAddr.IP
				port := xorAddr.Port

				lAddr := conn.LocalAddr().(*net.UDPAddr) //nolint:forcetypeassert
				srflxConfig := CandidateServerReflexiveConfig{
					Network:   network,
					Address:   ip.String(),
					Port:      port,
					Component: ComponentRTP,
					RelAddr:   lAddr.IP.String(),
					RelPort:   lAddr.Port,
				}
				c, err := NewCandidateServerReflexive(&srflxConfig)
				if err != nil {
					closeConnAndLog(conn, a.log, "failed to create server reflexive candidate: %s %s %d: %v", network, ip, port, err)

					return
				}

				if err := a.addCandidate(ctx, c, conn); err != nil {
					if closeErr := c.close(); closeErr != nil {
						a.log.Warnf("Failed to close candidate: %v", closeErr)
					}
					a.log.Warnf("Failed to append to localCandidates and run onCandidateHdlr: %v", err)
				}
			}(*urls[i], networkType.String())
		}
	}
}

//nolint:maintidx,gocognit,gocyclo,cyclop
func (a *Agent) gatherCandidatesRelay(ctx context.Context, urls []*stun.URI) {
	var wg sync.WaitGroup
	defer wg.Wait()

	network := NetworkTypeUDP4.String()
	for i := range urls {
		switch {
		case urls[i].Scheme != stun.SchemeTypeTURN && urls[i].Scheme != stun.SchemeTypeTURNS:
			continue
		case urls[i].Username == "":
			a.log.Errorf("Failed to gather relay candidates: %v", ErrUsernameEmpty)

			return
		case urls[i].Password == "":
			a.log.Errorf("Failed to gather relay candidates: %v", ErrPasswordEmpty)

			return
		}

		wg.Add(1)
		go func(url stun.URI) {
			defer wg.Done()
			turnServerAddr := fmt.Sprintf("%s:%d", url.Host, url.Port)
			var (
				locConn       net.PacketConn
				err           error
				relAddr       string
				relPort       int
				relayProtocol string
			)

			switch {
			case url.Proto == stun.ProtoTypeUDP && url.Scheme == stun.SchemeTypeTURN:
				if locConn, err = a.net.ListenPacket(network, "0.0.0.0:0"); err != nil {
					a.log.Warnf("Failed to listen %s: %v", network, err)

					return
				}

				relAddr = locConn.LocalAddr().(*net.UDPAddr).IP.String() //nolint:forcetypeassert
				relPort = locConn.LocalAddr().(*net.UDPAddr).Port        //nolint:forcetypeassert
				relayProtocol = udp
			case a.proxyDialer != nil && url.Proto == stun.ProtoTypeTCP &&
				(url.Scheme == stun.SchemeTypeTURN || url.Scheme == stun.SchemeTypeTURNS):
				conn, connectErr := a.proxyDialer.Dial(NetworkTypeTCP4.String(), turnServerAddr)
				if connectErr != nil {
					a.log.Warnf("Failed to dial TCP address %s via proxy dialer: %v", turnServerAddr, connectErr)

					return
				}

				relAddr = conn.LocalAddr().(*net.TCPAddr).IP.String() //nolint:forcetypeassert
				relPort = conn.LocalAddr().(*net.TCPAddr).Port        //nolint:forcetypeassert
				if url.Scheme == stun.SchemeTypeTURN {
					relayProtocol = tcp
				} else if url.Scheme == stun.SchemeTypeTURNS {
					relayProtocol = "tls"
				}
				locConn = turn.NewSTUNConn(conn)

			case url.Proto == stun.ProtoTypeTCP && url.Scheme == stun.SchemeTypeTURN:
				tcpAddr, connectErr := a.net.ResolveTCPAddr(NetworkTypeTCP4.String(), turnServerAddr)
				if connectErr != nil {
					a.log.Warnf("Failed to resolve TCP address %s: %v", turnServerAddr, connectErr)

					return
				}

				conn, connectErr := a.net.DialTCP(NetworkTypeTCP4.String(), nil, tcpAddr)
				if connectErr != nil {
					a.log.Warnf("Failed to dial TCP address %s: %v", turnServerAddr, connectErr)

					return
				}

				relAddr = conn.LocalAddr().(*net.TCPAddr).IP.String() //nolint:forcetypeassert
				relPort = conn.LocalAddr().(*net.TCPAddr).Port        //nolint:forcetypeassert
				relayProtocol = tcp
				locConn = turn.NewSTUNConn(conn)
			case url.Proto == stun.ProtoTypeUDP && url.Scheme == stun.SchemeTypeTURNS:
				udpAddr, connectErr := a.net.ResolveUDPAddr(network, turnServerAddr)
				if connectErr != nil {
					a.log.Warnf("Failed to resolve UDP address %s: %v", turnServerAddr, connectErr)

					return
				}

				udpConn, dialErr := a.net.DialUDP("udp", nil, udpAddr)
				if dialErr != nil {
					a.log.Warnf("Failed to dial DTLS address %s: %v", turnServerAddr, dialErr)

					return
				}

				conn, connectErr := dtls.Client(&fakenet.PacketConn{Conn: udpConn}, udpConn.RemoteAddr(), &dtls.Config{
					ServerName:         url.Host,
					InsecureSkipVerify: a.insecureSkipVerify, //nolint:gosec
					LoggerFactory:      a.loggerFactory,
				})
				if connectErr != nil {
					a.log.Warnf("Failed to create DTLS client: %v", turnServerAddr, connectErr)

					return
				}

				if connectErr = conn.HandshakeContext(ctx); connectErr != nil {
					a.log.Warnf("Failed to create DTLS client: %v", turnServerAddr, connectErr)

					return
				}

				relAddr = conn.LocalAddr().(*net.UDPAddr).IP.String() //nolint:forcetypeassert
				relPort = conn.LocalAddr().(*net.UDPAddr).Port        //nolint:forcetypeassert
				relayProtocol = relayProtocolDTLS
				locConn = &fakenet.PacketConn{Conn: conn}
			case url.Proto == stun.ProtoTypeTCP && url.Scheme == stun.SchemeTypeTURNS:
				tcpAddr, resolvErr := a.net.ResolveTCPAddr(NetworkTypeTCP4.String(), turnServerAddr)
				if resolvErr != nil {
					a.log.Warnf("Failed to resolve relay address %s: %v", turnServerAddr, resolvErr)

					return
				}

				tcpConn, dialErr := a.net.DialTCP(NetworkTypeTCP4.String(), nil, tcpAddr)
				if dialErr != nil {
					a.log.Warnf("Failed to connect to relay: %v", dialErr)

					return
				}

				conn := tls.Client(tcpConn, &tls.Config{
					ServerName:         url.Host,
					InsecureSkipVerify: a.insecureSkipVerify, //nolint:gosec
				})

				if hsErr := conn.HandshakeContext(ctx); hsErr != nil {
					if closeErr := tcpConn.Close(); closeErr != nil {
						a.log.Errorf("Failed to close relay connection: %v", closeErr)
					}
					a.log.Warnf("Failed to connect to relay: %v", hsErr)

					return
				}

				relAddr = conn.LocalAddr().(*net.TCPAddr).IP.String() //nolint:forcetypeassert
				relPort = conn.LocalAddr().(*net.TCPAddr).Port        //nolint:forcetypeassert
				relayProtocol = relayProtocolTLS
				locConn = turn.NewSTUNConn(conn)
			default:
				a.log.Warnf("Unable to handle URL in gatherCandidatesRelay %v", url)

				return
			}

			client, err := turn.NewClient(&turn.ClientConfig{
				TURNServerAddr: turnServerAddr,
				Conn:           locConn,
				Username:       url.Username,
				Password:       url.Password,
				LoggerFactory:  a.loggerFactory,
				Net:            a.net,
			})
			if err != nil {
				closeConnAndLog(locConn, a.log, "failed to create new TURN client %s %s", turnServerAddr, err)

				return
			}

			if err = client.Listen(); err != nil {
				client.Close()
				closeConnAndLog(locConn, a.log, "failed to listen on TURN client %s %s", turnServerAddr, err)

				return
			}

			relayConn, err := client.Allocate()
			if err != nil {
				client.Close()
				closeConnAndLog(locConn, a.log, "failed to allocate on TURN client %s %s", turnServerAddr, err)

				return
			}

			rAddr := relayConn.LocalAddr().(*net.UDPAddr) //nolint:forcetypeassert

			if shouldFilterLocationTracked(rAddr.IP) {
				a.log.Warnf("TURN address %s is somehow filtered for location tracking reasons", rAddr.IP)

				return
			}

			relayConfig := CandidateRelayConfig{
				Network:       network,
				Component:     ComponentRTP,
				Address:       rAddr.IP.String(),
				Port:          rAddr.Port,
				RelAddr:       relAddr,
				RelPort:       relPort,
				RelayProtocol: relayProtocol,
				OnClose: func() error {
					client.Close()

					return locConn.Close()
				},
			}
			relayConnClose := func() {
				if relayConErr := relayConn.Close(); relayConErr != nil {
					a.log.Warnf("Failed to close relay %v", relayConErr)
				}
			}
			candidate, err := NewCandidateRelay(&relayConfig)
			if err != nil {
				relayConnClose()

				client.Close()
				closeConnAndLog(locConn, a.log, "failed to create relay candidate: %s %s: %v", network, rAddr.String(), err)

				return
			}

			if err := a.addCandidate(ctx, candidate, relayConn); err != nil {
				relayConnClose()

				if closeErr := candidate.close(); closeErr != nil {
					a.log.Warnf("Failed to close candidate: %v", closeErr)
				}
				a.log.Warnf("Failed to append to localCandidates and run onCandidateHdlr: %v", err)
			}
		}(*urls[i])
	}
}
