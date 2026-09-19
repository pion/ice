// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
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
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/pion/dtls/v3"
	"github.com/pion/ice/v4/internal/fakenet"
	stunx "github.com/pion/ice/v4/internal/stun"
	"github.com/pion/logging"
	"github.com/pion/stun/v4"
	"github.com/pion/turn/v5"
)

// GatherOption configures a gathering pass.
type GatherOption func(*gatherConfig) error

type gatherConfig struct {
	mDNSMode               MulticastDNSMode
	urls                   []*stun.URI
	candidateTypes         []CandidateType
	networkTypes           []NetworkType
	turnTransportProtocols []NetworkType
	localUfrag             string
	localPwd               string
	localCredentialsSet    bool
}

func newGatherConfig(opts ...GatherOption) (*gatherConfig, error) {
	config := &gatherConfig{candidateTypes: defaultCandidateTypes()}
	for _, opt := range opts {
		if opt != nil {
			if err := opt(config); err != nil {
				return nil, err
			}
		}
	}
	config.networkTypes = configuredNetworkTypes(config.networkTypes)

	return config, nil
}

func (config *gatherConfig) resolveLocalCredentials(ufrag, pwd string) error {
	if !config.localCredentialsSet {
		config.localUfrag, config.localPwd = ufrag, pwd
	}
	var err error
	if config.localUfrag == "" {
		config.localUfrag, err = generateUFrag()
		if err != nil {
			return err
		}
	}
	if config.localPwd == "" {
		config.localPwd, err = generatePwd()
		if err != nil {
			return err
		}
	}

	return nil
}

// WithURLs sets the STUN/TURN server URLs used for this gathering pass.
func WithURLs(urls []*stun.URI) GatherOption {
	return func(config *gatherConfig) error {
		if len(urls) == 0 {
			config.urls = nil

			return nil
		}

		cloned := make([]*stun.URI, len(urls))
		for i, url := range urls {
			if url == nil {
				return ErrInvalidURL
			}
			value := *url
			cloned[i] = &value
		}
		config.urls = cloned

		return nil
	}
}

// WithLocalCredentials sets the local ICE username fragment and password.
// If this option is omitted on the first Gather call, both credentials are generated
// randomly. They are available through GetLocalUserCredentials when Gather returns.
// Subsequent Gather calls without this option reuse the existing credentials and do
// not restart ICE.
//
// Changing either credential starts a new ICE generation and restarts ICE. Each empty
// string supplied to this option generates a fresh value: WithLocalCredentials("", "").
func WithLocalCredentials(ufrag, pwd string) GatherOption {
	return func(config *gatherConfig) error {
		if err := validateLocalCredentials(ufrag, pwd); err != nil {
			return err
		}

		config.localUfrag = ufrag
		config.localPwd = pwd
		config.localCredentialsSet = true

		return nil
	}
}

// WithNetworkTypes sets the enabled candidate network types for candidate gathering.
// This controls the network types exposed in ICE candidates and used for pairing.
// Use WithTURNTransportProtocols to control the local TURN client-to-server transport.
// By default, all network types are enabled.
//
// Example:
//
//	err := agent.Gather(
//		WithNetworkTypes([]NetworkType{NetworkTypeUDP4, NetworkTypeUDP6}),
//	)
func WithNetworkTypes(networkTypes []NetworkType) GatherOption {
	return func(config *gatherConfig) error {
		normalized, err := sanitizeTransportNetworkTypes(networkTypes)
		if err != nil {
			return err
		}

		config.networkTypes = normalized

		return nil
	}
}

// WithTURNTransportProtocols restricts protocols used for this gathering pass when
// connecting to TURN servers (TURN client <-> TURN server transport).
//
// This is independent from WithNetworkTypes, which controls ICE candidate
// network types announced to the peer. Supported values are
// NetworkTypeUDP4/UDP6 and NetworkTypeTCP4/TCP6.
func WithTURNTransportProtocols(protocols []NetworkType) GatherOption {
	return func(config *gatherConfig) error {
		normalized, err := sanitizeTransportNetworkTypes(protocols)
		if err != nil {
			return err
		}

		config.turnTransportProtocols = normalized

		return nil
	}
}

// WithCandidateTypes sets the enabled candidate types for gathering.
// By default, host, server reflexive, and relay candidates are enabled.
//
// Example:
//
//	err := agent.Gather(
//		WithCandidateTypes([]CandidateType{CandidateTypeHost, CandidateTypeServerReflexive}),
//	)
func WithCandidateTypes(candidateTypes []CandidateType) GatherOption {
	return func(config *gatherConfig) error {
		config.candidateTypes = append([]CandidateType(nil), candidateTypes...)

		return nil
	}
}

func sanitizeTransportNetworkTypes(types []NetworkType) ([]NetworkType, error) {
	if len(types) == 0 {
		return nil, nil
	}

	seen := map[NetworkType]struct{}{}
	out := make([]NetworkType, 0, len(types))
	for _, networkType := range types {
		if !networkType.IsUDP() && !networkType.IsTCP() {
			return nil, ErrProtoType
		}

		if _, ok := seen[networkType]; ok {
			continue
		}

		seen[networkType] = struct{}{}
		out = append(out, networkType)
	}

	return out, nil
}

type turnClient interface {
	Listen() error
	AllocateWithContext(context.Context) (net.PacketConn, error)
	Close()
}

func defaultTurnClient(cfg *turn.ClientConfig) (turnClient, error) {
	return turn.NewClient(cfg)
}

func configuredNetworkTypes(networkTypes []NetworkType) []NetworkType {
	if len(networkTypes) == 0 {
		return supportedNetworkTypes()
	}

	return networkTypes
}

func effectiveURLProtoType(url stun.URI) stun.ProtoType {
	if url.Proto != stun.ProtoTypeUnknown {
		return url.Proto
	}

	switch url.Scheme {
	case stun.SchemeTypeSTUN, stun.SchemeTypeTURN:
		return stun.ProtoTypeUDP
	case stun.SchemeTypeSTUNS, stun.SchemeTypeTURNS:
		return stun.ProtoTypeTCP
	default:
		return stun.ProtoTypeUnknown
	}
}

func urlSupportsSrflxGathering(url stun.URI) bool {
	if effectiveURLProtoType(url) != stun.ProtoTypeUDP {
		return false
	}

	return url.Scheme == stun.SchemeTypeSTUN || url.Scheme == stun.SchemeTypeTURN
}

func relayNetworkTypesForConfiguredCandidates(networkTypes []NetworkType) []NetworkType {
	// Relay allocations currently produce UDP relay endpoints, so relay candidate
	// publication must be gated by configured UDP candidate network types.
	res := []NetworkType{}
	for _, networkType := range configuredNetworkTypes(networkTypes) {
		if networkType.IsUDP() {
			res = append(res, networkType)
		}
	}

	return res
}

func turnNetworkTypesForURL(url stun.URI, networkTypes []NetworkType) []NetworkType {
	proto := effectiveURLProtoType(url)
	res := []NetworkType{}

	for _, networkType := range configuredNetworkTypes(networkTypes) {
		switch proto {
		case stun.ProtoTypeUDP:
			if networkType.IsUDP() {
				res = append(res, networkType)
			}
		case stun.ProtoTypeTCP:
			if networkType.IsTCP() {
				res = append(res, networkType)
			}
		default:
		}
	}

	return res
}

// Close a net.Conn and log if we have a failure.
func closeConnAndLog(c io.Closer, log logging.LeveledLogger, msg string, args ...any) {
	if c == nil || (reflect.ValueOf(c).Kind() == reflect.Pointer && reflect.ValueOf(c).IsNil()) {
		log.Warnf("Connection is not allocated: "+msg, args...)

		return
	}

	log.Warnf(msg, args...)
	if err := c.Close(); err != nil {
		log.Warnf("Failed to close connection: %v", err)
	}
}

// Gather asynchronously gathers candidates, restarting ICE only when the local credentials change.
// Call OnCandidate before Gather. Local credentials are available when Gather returns.
// Calling Gather again cancels the previous gather. If the
// credentials are unchanged, existing candidates and connectivity are preserved.
// Changed credentials clear candidates and remote credentials and restart connectivity checks.
// Each pass signals completion with a nil candidate. Call Gather again to gather more candidates.
//
//nolint:cyclop
func (a *Agent) Gather(opts ...GatherOption) error {
	config, err := newGatherConfig(opts...)
	if err != nil {
		return err
	}
	var gatherErr error
	if err = a.loop.Run(a.loop, func(ctx context.Context) {
		if gatherErr = a.validateGatherConfig(config); gatherErr != nil {
			return
		}
		if a.onCandidateHdlr.Load() == nil {
			gatherErr = ErrNoOnCandidateHandler

			return
		}
		if gatherErr = config.resolveLocalCredentials(a.localUfrag, a.localPwd); gatherErr != nil {
			return
		}

		interfaces, _, interfaceErr := localInterfaces(a.net, a.interfaceFilter, a.ipFilter, config.networkTypes, a.includeLoopback)
		if interfaceErr != nil {
			gatherErr = fmt.Errorf("error getting local interfaces: %w", interfaceErr)

			return
		}

		a.gatherCandidateCancel()
		if a.localUfrag != config.localUfrag || a.localPwd != config.localPwd {
			a.startGatherGeneration(config)
		}
		config.mDNSMode = a.mDNSMode
		if a.mDNSConn == nil || !slices.Equal(a.networkTypes, config.networkTypes) {
			a.closeMulticastConn()
			var mdnsErr error
			a.mDNSConn, config.mDNSMode, mdnsErr = createMulticastDNS(
				a.net, config.networkTypes, interfaces, a.includeLoopback,
				mDNSLocalAddressFromTCPMux(a.tcpMux, config.networkTypes), a.mDNSMode, a.mDNSName, a.log, a.loggerFactory,
			)
			if mdnsErr != nil {
				a.log.Warnf("Failed to initialize mDNS %s: %v", a.mDNSName, mdnsErr)
			}
		}

		a.networkTypes = config.networkTypes
		if !a.relayAcceptanceMinWaitExplicit {
			a.relayAcceptanceMinWait = defaultRelayAcceptanceMinWaitFor(config.candidateTypes)
		}

		ctx, cancel := context.WithCancel(ctx)
		a.gatherCandidateCancel = cancel
		done := make(chan struct{})
		previousDone := a.gatherCandidateDone
		a.gatherCandidateDone = done
		generation := a.gatherGeneration
		a.gatheringState = GatheringStateGathering
		go func() {
			// Join previous workers before reusing muxes.
			if previousDone != nil {
				<-previousDone
			}
			a.gatherCandidates(ctx, done, generation, config.localUfrag, config)
		}()
	}); err != nil {
		return err
	}

	return gatherErr
}

// startGatherGeneration runs on the agent loop when the local credentials change.
func (a *Agent) startGatherGeneration(config *gatherConfig) {
	if a.gatheringState != GatheringStateNew {
		a.gatherGeneration++
	}
	a.removeUfragFromMux()
	a.localUfrag, a.localPwd = config.localUfrag, config.localPwd
	a.remoteUfrag, a.remotePwd = "", ""
	a.remoteCandidateGeneration.Add(1)
	a.checklist = nil
	a.pairsByID = make(map[uint64]*CandidatePair)
	a.pendingBindingRequests = nil
	a.setSelectedPair(nil)
	a.deleteAllCandidates()
	a.setSelector()
	if a.connectionState != ConnectionStateNew {
		a.updateConnectionState(ConnectionStateChecking)
	}
}

func (a *Agent) validateGatherConfig(config *gatherConfig) error { //nolint:cyclop
	if a.lite && (len(config.candidateTypes) != 1 || config.candidateTypes[0] != CandidateTypeHost) {
		return ErrLiteUsingNonHostCandidates
	}
	if len(config.urls) > 0 && !slices.Contains(config.candidateTypes, CandidateTypeServerReflexive) && !slices.Contains(config.candidateTypes, CandidateTypeRelay) {
		return ErrUselessURLsProvided
	}
	if a.addressRewriteMapper != nil {
		if a.addressRewriteMapper.hasCandidateType(CandidateTypeHost) && !slices.Contains(config.candidateTypes, CandidateTypeHost) {
			return ErrIneffectiveAddressRewriteHost
		}
		if a.addressRewriteMapper.hasCandidateType(CandidateTypeServerReflexive) && !slices.Contains(config.candidateTypes, CandidateTypeServerReflexive) {
			return ErrIneffectiveAddressRewriteSrflx
		}
	}

	return nil
}

func (a *Agent) gatherCandidates(
	ctx context.Context,
	done chan struct{},
	generation uint64,
	localUfrag string,
	config *gatherConfig,
) {
	defer close(done)
	if ctx.Err() != nil {
		return
	}

	a.gatherCandidatesInternal(ctx, generation, localUfrag, config)

	if err := a.completeGathering(ctx, generation); err != nil && ctx.Err() == nil {
		a.log.Warnf("Failed to set gatheringState to GatheringStateComplete: %v", err)
	}
}

func (a *Agent) shouldRewriteCandidateType(candidateType CandidateType) bool {
	return a.addressRewriteMapper != nil && a.addressRewriteMapper.hasCandidateType(candidateType)
}

func (a *Agent) rewriteCandidatePort(candidate *candidateBase, localIP, iface string) {
	if a.addressRewriteMapper != nil {
		candidate.port = a.addressRewriteMapper.findExternalPort(
			candidate.candidateType,
			localIP,
			iface,
			candidate.port,
		)
	}
}

func (a *Agent) rewriteCandidateAddresses(
	candidateType CandidateType,
	address, localIP, iface string,
) ([]string, bool) {
	original := []string{address}
	if !a.shouldRewriteCandidateType(candidateType) ||
		(candidateType == CandidateTypeHost && a.mDNSMode == MulticastDNSModeQueryAndGather) {
		return original, true
	}

	mapped, matched, mode, err := a.addressRewriteMapper.findExternalIPs(candidateType, localIP, iface)
	if err != nil {
		a.log.Warnf("Address rewrite mapping failed for %s: %v", localIP, err)

		return original, candidateType == CandidateTypeHost
	}
	if !matched {
		return original, true
	}
	if len(mapped) == 0 {
		return original, mode != AddressRewriteReplace
	}
	// Mapped srflx candidates supplement the separately gathered STUN candidates.
	if mode == AddressRewriteReplace || candidateType == CandidateTypeServerReflexive {
		return mapped, true
	}

	return append(original, mapped...), true
}

// rewrittenCandidateIP preserves the transport IP when advertising an FQDN.
func rewrittenCandidateIP(address string, fallback net.IP) net.IP {
	if ip := net.ParseIP(address); ip != nil {
		return ip
	}

	return fallback
}

// gatherCandidatesInternal performs the actual candidate gathering for all configured types.
func (a *Agent) gatherCandidatesInternal(ctx context.Context, generation uint64, localUfrag string, config *gatherConfig) {
	var wg sync.WaitGroup
	for _, t := range config.candidateTypes {
		switch t {
		case CandidateTypeHost:
			wg.Add(1)
			go func() {
				a.gatherCandidatesLocal(ctx, config.networkTypes, generation, localUfrag, config.mDNSMode)
				wg.Done()
			}()
		case CandidateTypeServerReflexive:
			a.gatherServerReflexiveCandidates(ctx, &wg, generation, localUfrag, config)
		case CandidateTypeRelay:
			wg.Add(1)
			go func() {
				a.gatherCandidatesRelay(ctx, config, generation)
				wg.Done()
			}()
		case CandidateTypePeerReflexive, CandidateTypeUnspecified:
		}
	}

	// Block until all STUN and TURN URLs have been gathered (or timed out)
	wg.Wait()
}

func (a *Agent) gatherServerReflexiveCandidates(
	ctx context.Context,
	wg *sync.WaitGroup,
	generation uint64,
	localUfrag string,
	config *gatherConfig,
) {
	replaceSrflx := a.addressRewriteMapper != nil && a.addressRewriteMapper.shouldReplace(CandidateTypeServerReflexive)
	if !replaceSrflx {
		wg.Add(1)
		go func() {
			if a.udpMuxSrflx != nil {
				a.gatherCandidatesSrflxUDPMux(ctx, config.urls, config.networkTypes, generation, localUfrag)
			} else {
				a.gatherCandidatesSrflx(ctx, config.urls, config.networkTypes, generation)
			}
			wg.Done()
		}()
	}
	if a.addressRewriteMapper != nil && a.addressRewriteMapper.hasCandidateType(CandidateTypeServerReflexive) {
		wg.Add(1)
		go func() {
			a.gatherCandidatesSrflxMapped(ctx, config.networkTypes, generation)
			wg.Done()
		}()
	}
}

//nolint:gocognit,gocyclo,cyclop,maintidx
func (a *Agent) gatherCandidatesLocal(
	ctx context.Context,
	networkTypes []NetworkType,
	generation uint64,
	localUfrag string,
	mdnsMode MulticastDNSMode,
) {
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
		if err := a.gatherCandidatesLocalUDPMux(ctx, generation, localUfrag, mdnsMode); err != nil {
			a.log.Warnf("Failed to create host candidate for UDPMux: %s", err)
		}
		delete(networks, udp)
	}

	_, localAddrs, err := localInterfaces(a.net, a.interfaceFilter, a.ipFilter, networkTypes, a.includeLoopback)
	if err != nil {
		a.log.Warnf("Failed to iterate local interfaces, host candidates will not be gathered %s", err)

		return
	}

	for _, info := range localAddrs {
		addr := info.addr
		ifaceName := info.iface
		mappedAddrs, ok := a.rewriteCandidateAddresses(CandidateTypeHost, addr.String(), addr.String(), ifaceName)
		if !ok {
			continue
		}

		for _, address := range mappedAddrs {
			mappedIP, parseErr := netip.ParseAddr(address)
			if parseErr != nil {
				mappedIP = addr
			}
			mappedIP = mappedIP.Unmap()
			var isLocationTracked bool
			if mdnsMode == MulticastDNSModeQueryAndGather {
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

					// Only advertise TCP candidates for addresses that the mux listener is actually
					// bound to. When the listener is bound to a specific IP, exposing other interface
					// addresses would generate unreachable passive candidates and can stall active
					// TCP connect attempts.
					if addrProvider, ok := a.tcpMux.(interface{ LocalAddr() net.Addr }); ok {
						if muxAddr, ok := addrProvider.LocalAddr().(*net.TCPAddr); ok {
							if ip := muxAddr.IP; ip != nil && !ip.IsUnspecified() && !ip.Equal(addr.AsSlice()) {
								continue
							}
						}
					}

					// Handle ICE TCP passive mode
					var muxConns []net.PacketConn
					if multi, ok := a.tcpMux.(AllConnsGetter); ok {
						a.log.Debugf("GetAllConns by ufrag: %s", localUfrag)
						// Note: this is missing zone for IPv6 by just grabbing the IP slice
						muxConns, err = multi.GetAllConns(localUfrag, mappedIP.Is6(), addr.AsSlice())
						if err != nil {
							a.log.Warnf("Failed to get all TCP connections by ufrag: %s %s %s", network, addr, localUfrag)

							continue
						}
					} else {
						a.log.Debugf("GetConn by ufrag: %s", localUfrag)
						// Note: this is missing zone for IPv6 by just grabbing the IP slice
						conn, err := a.tcpMux.GetConnByUfrag(localUfrag, mappedIP.Is6(), addr.AsSlice())
						if err != nil {
							a.log.Warnf("Failed to get TCP connections by ufrag: %s %s %s", network, addr, localUfrag)

							continue
						}
						muxConns = []net.PacketConn{conn}
					}

					// Extract the port for each PacketConn we got.
					for _, conn := range muxConns {
						if tcpConn, ok := conn.LocalAddr().(*net.TCPAddr); ok {
							conns = append(conns, connAndPort{conn, tcpConn.Port})
						} else {
							closeConnAndLog(
								conn,
								a.log,
								"Failed to get port of connection from TCPMux: %s %s %s",
								network, addr, localUfrag,
							)
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
						a.log.Warnf("Failed to get port of UDPAddr from ListenUDPInPortRange: %s %s %s", network, addr, localUfrag)

						continue
					}
				}

				for _, connAndPort := range conns {
					hostConfig := CandidateHostConfig{
						Network:   network,
						Address:   mappedIP.String(),
						Port:      connAndPort.port,
						Component: ComponentRTP,
						TCPType:   tcpType,
						// we will still process this candidate so that we start up the right
						// listeners.
						IsLocationTracked: isLocationTracked,
					}
					if mdnsMode == MulticastDNSModeQueryAndGather {
						hostConfig.Address = address
					}

					candidateHost, err := NewCandidateHost(&hostConfig)

					if err == nil && mdnsMode == MulticastDNSModeQueryAndGather {
						err = candidateHost.setIPAddr(addr)
					}

					if err != nil {
						closeConnAndLog(
							connAndPort.conn,
							a.log,
							"failed to create host candidate: %s %s %d: %v",
							network, mappedIP,
							connAndPort.port,
							err,
						)

						continue
					}
					candidateHost.address = address
					a.rewriteCandidatePort(&candidateHost.candidateBase, addr.String(), ifaceName)

					if err := a.addCandidate(ctx, candidateHost, connAndPort.conn, &generation, false); err != nil {
						a.log.Warnf("Failed to append to localCandidates and run onCandidateHdlr: %v", err)
						a.cleanupCandidate(candidateHost, connAndPort.conn, "failed")
					}
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

//nolint:gocognit,cyclop
func (a *Agent) gatherCandidatesLocalUDPMux(
	ctx context.Context,
	generation uint64,
	localUfrag string,
	mdnsMode MulticastDNSMode,
) error {
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
		if _, isDefault := a.udpMux.(*UDPMuxDefault); isDefault && !a.includeLoopback && udpAddr.IP.IsLoopback() {
			// Unlike MultiUDPMux Default, UDPMuxDefault doesn't have
			// a separate param to include loopback, so we respect agent config
			continue
		}

		candidateIPs, ok := a.rewriteCandidateAddresses(CandidateTypeHost, udpAddr.IP.String(), udpAddr.IP.String(), "")
		if !ok {
			continue
		}

		for _, candidateIP := range candidateIPs {
			var address string
			var isLocationTracked bool
			if mdnsMode == MulticastDNSModeQueryAndGather {
				address = a.mDNSName
			} else {
				address = candidateIP
				// Here, we are not doing multicast gathering, so we will need to skip this address so
				// that we don't accidentally reveal location tracking information. Otherwise, the
				// case above hides the IP behind an mDNS address.
				isLocationTracked = shouldFilterLocationTracked(rewrittenCandidateIP(candidateIP, udpAddr.IP))
			}

			hostConfig := CandidateHostConfig{
				Network:           udp,
				Address:           address,
				Port:              udpAddr.Port,
				Component:         ComponentRTP,
				IsLocationTracked: isLocationTracked,
			}

			// Detect a duplicate candidate before calling addCandidate().
			// otherwise, addCandidate() detects the duplicate candidate
			// and close its connection, invalidating all candidates
			// that share the same connection.
			if _, ok := existingConfigs[hostConfig]; ok {
				continue
			}

			conn, err := a.udpMux.GetConn(localUfrag, udpAddr)
			if err != nil {
				return err
			}

			transportConfig := hostConfig
			if mdnsMode != MulticastDNSModeQueryAndGather {
				transportConfig.Address = rewrittenCandidateIP(address, udpAddr.IP).String()
			}
			cand, err := NewCandidateHost(&transportConfig)
			if err != nil {
				closeConnAndLog(conn, a.log, "failed to create host mux candidate: %s %d: %v", candidateIP, udpAddr.Port, err)

				continue
			}

			cand.address = address
			a.rewriteCandidatePort(&cand.candidateBase, udpAddr.IP.String(), "")

			if err := a.addCandidate(ctx, cand, conn, &generation, false); err != nil {
				a.log.Warnf("failed to add candidate: %s %d: %v", candidateIP, udpAddr.Port, err)
				a.cleanupCandidate(cand, conn, "failed")

				continue
			}

			existingConfigs[hostConfig] = struct{}{}
		}
	}

	return nil
}

//nolint:gocognit,cyclop
func (a *Agent) gatherCandidatesSrflxMapped(ctx context.Context, networkTypes []NetworkType, generation uint64) {
	var wg sync.WaitGroup
	defer wg.Wait()

	_, ifaces, _ := localInterfaces(a.net, a.interfaceFilter, a.ipFilter, networkTypes, a.includeLoopback)

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
				closeConnAndLog(conn, a.log, "Address rewrite mapping is enabled but LocalAddr is not a UDPAddr")

				return
			}

			iface := findIfaceForIP(ifaces, lAddr.IP)
			addresses, ok := a.rewriteCandidateAddresses(CandidateTypeServerReflexive, lAddr.IP.String(), lAddr.IP.String(), iface)
			if !ok {
				closeConnAndLog(
					conn, a.log, "Address rewrite mapping did not provide usable external IPs for %s", lAddr.IP.String(),
				)

				return
			}

			for idx, mappedIP := range addresses {
				currentConn := conn
				currentAddr := lAddr
				if idx > 0 {
					newConn, listenErr := listenUDPInPortRange(
						a.net,
						a.log,
						int(a.portMax),
						int(a.portMin),
						network,
						&net.UDPAddr{IP: lAddr.IP, Port: 0},
					)
					if listenErr != nil {
						closeConnAndLog(newConn, a.log, "Failed to listen %s for additional srflx mapping: %v", network, listenErr)

						return
					}
					currentConn = newConn
					var ok bool
					currentAddr, ok = currentConn.LocalAddr().(*net.UDPAddr)
					if !ok {
						closeConnAndLog(currentConn, a.log, "Address rewrite mapping is enabled but LocalAddr is not a UDPAddr")

						return
					}
				}

				if shouldFilterLocationTracked(rewrittenCandidateIP(mappedIP, currentAddr.IP)) {
					closeConnAndLog(currentConn, a.log, "external IP is somehow filtered for location tracking reasons %s", mappedIP)

					continue
				}

				srflxConfig := CandidateServerReflexiveConfig{
					Network:   network,
					Address:   rewrittenCandidateIP(mappedIP, currentAddr.IP).String(),
					Port:      currentAddr.Port,
					Component: ComponentRTP,
					RelAddr:   currentAddr.IP.String(),
					RelPort:   currentAddr.Port,
				}
				candidate, err := NewCandidateServerReflexive(&srflxConfig)
				if err != nil {
					closeConnAndLog(currentConn, a.log, "failed to create server reflexive candidate: %s %s %d: %v",
						network,
						mappedIP,
						currentAddr.Port,
						err)

					continue
				}
				candidate.address = mappedIP
				a.rewriteCandidatePort(&candidate.candidateBase, lAddr.IP.String(), iface)

				if err := a.addCandidate(ctx, candidate, currentConn, &generation, false); err != nil {
					a.log.Warnf("Failed to append to localCandidates and run onCandidateHdlr: %v", err)
					a.cleanupCandidate(candidate, currentConn, "failed")
				}
			}
		}()
	}
}

//nolint:gocognit,cyclop
func (a *Agent) gatherCandidatesSrflxUDPMux(
	ctx context.Context,
	urls []*stun.URI,
	networkTypes []NetworkType,
	generation uint64,
	localUfrag string,
) {
	var wg sync.WaitGroup
	defer wg.Wait()

	for _, networkType := range networkTypes {
		if networkType.IsTCP() {
			continue
		}

		for i := range urls {
			if !urlSupportsSrflxGathering(*urls[i]) {
				continue
			}

			for _, listenAddr := range a.udpMuxSrflx.GetListenAddresses() {
				udpAddr, ok := listenAddr.(*net.UDPAddr)
				if !ok {
					a.log.Warn("Failed to cast udpMuxSrflx listen address to UDPAddr")

					continue
				}
				wg.Add(1)
				go func(url stun.URI, network string, localAddr *net.UDPAddr) {
					defer wg.Done()

					hostPort := net.JoinHostPort(url.Host, strconv.Itoa(url.Port))
					serverAddr, err := a.net.ResolveUDPAddr(network, hostPort)
					if err != nil {
						a.log.Debugf("Failed to resolve STUN host: %s %s: %v", network, hostPort, err)

						return
					}

					if shouldFilterLocationTracked(serverAddr.IP) {
						a.log.Warnf("STUN host %s is somehow filtered for location tracking reasons", hostPort)

						return
					}

					xorAddr, err := getXORMappedAddr(ctx, a.udpMuxSrflx, serverAddr, a.stunGatherTimeout)
					if err != nil {
						a.log.Warnf("Failed get server reflexive address %s %s: %v", network, url, err)

						return
					}

					conn, err := a.udpMuxSrflx.GetConnForURL(localUfrag, url.String(), localAddr)
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
					cand, err := NewCandidateServerReflexive(&srflxConfig)
					if err != nil {
						closeConnAndLog(conn, a.log, "failed to create server reflexive candidate: %s %s %d: %v", network, ip, port, err)

						return
					}
					a.rewriteCandidatePort(&cand.candidateBase, localAddr.IP.String(), "")

					if err := a.addCandidate(ctx, cand, conn, &generation, false); err != nil {
						a.log.Warnf("Failed to append srflx mux candidate to localCandidates: %v", err)
						a.cleanupCandidate(cand, conn, "failed")
					}
				}(*urls[i], networkType.String(), udpAddr)
			}
		}
	}
}

type contextXORMappedAddrGetter interface {
	GetXORMappedAddrContext(context.Context, net.Addr, time.Duration) (*stun.XORMappedAddress, error)
}

func getXORMappedAddr(
	ctx context.Context,
	mux UniversalUDPMux,
	serverAddr net.Addr,
	deadline time.Duration,
) (*stun.XORMappedAddress, error) {
	if muxWithContext, ok := mux.(contextXORMappedAddrGetter); ok {
		return muxWithContext.GetXORMappedAddrContext(ctx, serverAddr, deadline)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return mux.GetXORMappedAddr(serverAddr, deadline)
}

//nolint:cyclop,gocognit
func (a *Agent) gatherCandidatesSrflx(
	ctx context.Context, urls []*stun.URI, networkTypes []NetworkType, generation uint64,
) {
	var wg sync.WaitGroup
	defer wg.Wait()

	useFilteredLocalAddrs := a.interfaceFilter != nil || a.ipFilter != nil
	localAddrs := []ifaceAddr{}
	if useFilteredLocalAddrs {
		_, addrs, err := localInterfaces(a.net, a.interfaceFilter, a.ipFilter, networkTypes, a.includeLoopback)
		if err != nil {
			a.log.Warnf("Failed to iterate local interfaces, srflx candidates will not be gathered %s", err)

			return
		}
		localAddrs = addrs
	}

	gatherForURL := func(url stun.URI, network string, listenAddr *net.UDPAddr) {
		defer wg.Done()

		hostPort := net.JoinHostPort(url.Host, strconv.Itoa(url.Port))
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
			listenAddr,
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

		transaction, err := stunx.NewXORMappedAddrTransaction()
		if err != nil {
			closeConnAndLog(conn, a.log, "failed to create STUN transaction for %s %s: %v", network, url, err)

			return
		}

		xorAddr, err := transaction.RunPacketConn(ctx, conn, serverAddr, a.stunGatherTimeout)
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
		candidate, err := NewCandidateServerReflexive(&srflxConfig)
		if err != nil {
			closeConnAndLog(conn, a.log, "failed to create server reflexive candidate: %s %s %d: %v", network, ip, port, err)

			return
		}

		a.rewriteCandidatePort(&candidate.candidateBase, lAddr.IP.String(), findIfaceForIP(localAddrs, lAddr.IP))

		if err := a.addCandidate(ctx, candidate, conn, &generation, false); err != nil {
			a.log.Warnf("Failed to append to localCandidates and run onCandidateHdlr: %v", err)
			a.cleanupCandidate(candidate, conn, "failed")
		}
	}

	for _, networkType := range networkTypes {
		if networkType.IsTCP() {
			continue
		}

		for i := range urls {
			if !urlSupportsSrflxGathering(*urls[i]) {
				continue
			}

			if !useFilteredLocalAddrs {
				wg.Add(1)
				go gatherForURL(*urls[i], networkType.String(), &net.UDPAddr{IP: nil, Port: 0})

				continue
			}

			for j := range localAddrs {
				if networkType.IsIPv4() && localAddrs[j].addr.Is6() {
					continue
				}
				if networkType.IsIPv6() && !localAddrs[j].addr.Is6() {
					continue
				}

				wg.Add(1)
				go gatherForURL(
					*urls[i],
					networkType.String(),
					&net.UDPAddr{IP: localAddrs[j].addr.AsSlice(), Zone: localAddrs[j].addr.Zone(), Port: 0},
				)
			}
		}
	}
}

//nolint:maintidx,gocognit,gocyclo,cyclop
func (a *Agent) gatherCandidatesRelay(ctx context.Context, config *gatherConfig, generation uint64) {
	var wg sync.WaitGroup
	defer wg.Wait()
	_, ifaces, _ := localInterfaces(a.net, a.interfaceFilter, a.ipFilter, config.networkTypes, a.includeLoopback)

	useFilteredLocalAddrs := a.interfaceFilter != nil || a.ipFilter != nil
	localAddrs := []ifaceAddr{}
	if useFilteredLocalAddrs {
		localAddrs = append(localAddrs, ifaces...)
		if len(localAddrs) == 0 {
			return
		}
	}

	if len(relayNetworkTypesForConfiguredCandidates(config.networkTypes)) == 0 {
		return
	}

	for _, url := range config.urls {
		switch {
		case url.Scheme != stun.SchemeTypeTURN && url.Scheme != stun.SchemeTypeTURNS:
			continue
		case url.Username == "":
			a.log.Errorf("Failed to gather relay candidates: %v", ErrUsernameEmpty)

			return
		case url.Password == "":
			a.log.Errorf("Failed to gather relay candidates: %v", ErrPasswordEmpty)

			return
		}

		urlProto := effectiveURLProtoType(*url)

		networkTypes := turnNetworkTypesForURL(*url, config.turnTransportProtocols)
		if len(networkTypes) == 0 {
			continue
		}

		for _, networkType := range networkTypes {
			network := networkType.String()
			bindAddrs := []string{}
			if !useFilteredLocalAddrs { // nolint:nestif
				if networkType.IsIPv6() {
					bindAddrs = append(bindAddrs, "[::]:0")
				} else {
					bindAddrs = append(bindAddrs, "0.0.0.0:0")
				}
			} else {
				for i := range localAddrs {
					if networkType.IsIPv4() && localAddrs[i].addr.Is6() {
						continue
					}
					if networkType.IsIPv6() && !localAddrs[i].addr.Is6() {
						continue
					}

					bindAddrs = append(bindAddrs, net.JoinHostPort(localAddrs[i].addr.String(), "0"))
				}
			}

			for _, localBindAddr := range bindAddrs {
				wg.Add(1)
				go func(url stun.URI, network string, urlProto stun.ProtoType, localBindAddr string) {
					defer wg.Done()

					turnServerAddr := net.JoinHostPort(url.Host, strconv.Itoa(url.Port))
					var (
						locConn             net.PacketConn
						err                 error
						relAddr             string
						relPort             int
						relayProtocol       string
						needsTURNServerAddr bool
					)

					switch {
					case urlProto == stun.ProtoTypeUDP && url.Scheme == stun.SchemeTypeTURN:
						serverAddr, resolveErr := a.net.ResolveUDPAddr(network, turnServerAddr)
						if resolveErr != nil {
							a.log.Debugf("Failed to resolve TURN host: %s %s: %v", network, turnServerAddr, resolveErr)

							return
						}
						turnServerAddr = serverAddr.String()

						if locConn, err = a.net.ListenPacket(network, localBindAddr); err != nil {
							a.log.Warnf("Failed to listen %s: %v", network, err)

							return
						}

						relAddr = locConn.LocalAddr().(*net.UDPAddr).IP.String() //nolint:forcetypeassert
						relPort = locConn.LocalAddr().(*net.UDPAddr).Port        //nolint:forcetypeassert
						relayProtocol = udp
						needsTURNServerAddr = true
					case a.proxyDialer != nil && urlProto == stun.ProtoTypeTCP &&
						(url.Scheme == stun.SchemeTypeTURN || url.Scheme == stun.SchemeTypeTURNS):
						conn, connectErr := a.proxyDialer.Dial(network, turnServerAddr)
						if connectErr != nil {
							a.log.Warnf("Failed to dial TCP address %s via proxy dialer: %v", turnServerAddr, connectErr)

							return
						}

						relAddr = conn.LocalAddr().(*net.TCPAddr).IP.String() //nolint:forcetypeassert
						relPort = conn.LocalAddr().(*net.TCPAddr).Port        //nolint:forcetypeassert
						switch url.Scheme {
						case stun.SchemeTypeTURN:
							relayProtocol = tcp
						case stun.SchemeTypeTURNS:
							relayProtocol = "tls"
						default:
						}
						locConn = turn.NewSTUNConn(conn)

					case urlProto == stun.ProtoTypeTCP && url.Scheme == stun.SchemeTypeTURN:
						tcpAddr, connectErr := a.net.ResolveTCPAddr(network, turnServerAddr)
						if connectErr != nil {
							a.log.Warnf("Failed to resolve TCP address %s: %v", turnServerAddr, connectErr)

							return
						}

						conn, connectErr := a.net.DialTCP(network, nil, tcpAddr)
						if connectErr != nil {
							a.log.Warnf("Failed to dial TCP address %s: %v", turnServerAddr, connectErr)

							return
						}

						relAddr = conn.LocalAddr().(*net.TCPAddr).IP.String() //nolint:forcetypeassert
						relPort = conn.LocalAddr().(*net.TCPAddr).Port        //nolint:forcetypeassert
						relayProtocol = tcp
						locConn = turn.NewSTUNConn(conn)
					case urlProto == stun.ProtoTypeUDP && url.Scheme == stun.SchemeTypeTURNS:
						udpAddr, connectErr := a.net.ResolveUDPAddr(network, turnServerAddr)
						if connectErr != nil {
							a.log.Warnf("Failed to resolve UDP address %s: %v", turnServerAddr, connectErr)

							return
						}

						udpConn, dialErr := a.net.DialUDP(network, nil, udpAddr)
						if dialErr != nil {
							a.log.Warnf("Failed to dial DTLS address %s: %v", turnServerAddr, dialErr)

							return
						}

						conn, connectErr := dtls.ClientWithOptions(&fakenet.PacketConn{Conn: udpConn}, udpConn.RemoteAddr(),
							dtls.WithServerName(url.Host),
							dtls.WithInsecureSkipVerify(a.insecureSkipVerify), //nolint:gosec
							dtls.WithLoggerFactory(a.loggerFactory),
						)
						if connectErr != nil {
							a.log.Warnf("Failed to create DTLS client: %v", turnServerAddr, connectErr)
							if closeErr := udpConn.Close(); closeErr != nil {
								a.log.Errorf("Failed to close relay connection: %v", closeErr)
							}

							return
						}

						if connectErr = conn.HandshakeContext(ctx); connectErr != nil {
							a.log.Warnf("Failed to create DTLS client: %v", turnServerAddr, connectErr)
							if closeErr := conn.Close(); closeErr != nil {
								a.log.Errorf("Failed to close relay connection: %v", closeErr)
							}

							return
						}

						relAddr = conn.LocalAddr().(*net.UDPAddr).IP.String() //nolint:forcetypeassert
						relPort = conn.LocalAddr().(*net.UDPAddr).Port        //nolint:forcetypeassert
						relayProtocol = relayProtocolDTLS
						locConn = &fakenet.PacketConn{Conn: conn}
					case urlProto == stun.ProtoTypeTCP && url.Scheme == stun.SchemeTypeTURNS:
						tcpAddr, resolvErr := a.net.ResolveTCPAddr(network, turnServerAddr)
						if resolvErr != nil {
							a.log.Warnf("Failed to resolve relay address %s: %v", turnServerAddr, resolvErr)

							return
						}

						tcpConn, dialErr := a.net.DialTCP(network, nil, tcpAddr)
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

					factory := a.turnClientFactory
					if factory == nil {
						factory = defaultTurnClient
					}

					clientConfig := &turn.ClientConfig{
						Conn:          locConn,
						Username:      url.Username,
						Password:      url.Password,
						LoggerFactory: a.loggerFactory,
						Net:           a.net,
					}
					if needsTURNServerAddr {
						clientConfig.TURNServerAddr = turnServerAddr
					}

					client, err := factory(clientConfig)
					if err != nil {
						closeConnAndLog(locConn, a.log, "failed to create new TURN client %s %s", turnServerAddr, err)

						return
					}

					if err = client.Listen(); err != nil {
						client.Close()
						closeConnAndLog(locConn, a.log, "failed to listen on TURN client %s %s", turnServerAddr, err)

						return
					}

					relayConn, err := client.AllocateWithContext(ctx)
					if err != nil {
						client.Close()
						closeConnAndLog(locConn, a.log, "failed to allocate on TURN client %s %s", turnServerAddr, err)

						return
					}

					closeRelayConn := func() {
						if relayConErr := relayConn.Close(); relayConErr != nil {
							a.log.Warnf("Failed to close relay %v", relayConErr)
						}
					}

					rAddr := relayConn.LocalAddr().(*net.UDPAddr) //nolint:forcetypeassert
					if shouldFilterLocationTracked(rAddr.IP) {
						closeRelayConn()
						client.Close()
						closeConnAndLog(locConn, a.log,
							"TURN address %s is somehow filtered for location tracking reasons", rAddr.IP)

						return
					}

					// Relay allocations currently produce UDP relay endpoints regardless of
					// whether the TURN control connection uses UDP/TCP/TLS/DTLS.
					a.addRelayCandidates(ctx, generation, config.networkTypes, relayEndpoint{
						network:  udp,
						address:  rAddr.IP,
						port:     rAddr.Port,
						relAddr:  relAddr,
						relPort:  relPort,
						iface:    findIfaceForIP(ifaces, net.ParseIP(relAddr)),
						protocol: relayProtocol,
						conn:     relayConn,
						onClose: func() error {
							client.Close()

							return locConn.Close()
						},
						closeConn: closeRelayConn,
					})
				}(*url, network, urlProto, localBindAddr)
			}
		}
	}
}

type relayEndpoint struct {
	network   string
	address   net.IP
	port      int
	relAddr   string
	relPort   int
	protocol  string
	iface     string
	conn      net.PacketConn
	onClose   func() error
	closeConn func()
}

func findIfaceForIP(ifaces []ifaceAddr, ip net.IP) string {
	if ip == nil {
		return ""
	}
	for _, info := range ifaces {
		if info.addr.String() == ip.String() {
			return info.iface
		}
	}

	return ""
}

func (a *Agent) createRelayCandidate(
	ctx context.Context, ep relayEndpoint, ip string, generation uint64, onClose func() error,
) error {
	relayConfig := CandidateRelayConfig{
		Network:       ep.network,
		Component:     ComponentRTP,
		Address:       rewrittenCandidateIP(ip, ep.address).String(),
		Port:          ep.port,
		RelAddr:       ep.relAddr,
		RelPort:       ep.relPort,
		RelayProtocol: ep.protocol,
		OnClose:       onClose,
	}
	candidate, err := NewCandidateRelay(&relayConfig)
	if err != nil {
		a.log.Warnf("failed to create relay candidate: %s %d: %v", ip, ep.port, err)

		return err
	}
	candidate.address = ip
	a.rewriteCandidatePort(&candidate.candidateBase, ep.relAddr, ep.iface)

	if err := a.addCandidate(ctx, candidate, ep.conn, &generation, false); err != nil {
		if closeErr := candidate.close(); closeErr != nil {
			a.log.Warnf("Failed to close candidate: %v", closeErr)
		}
		a.log.Warnf("Failed to append to localCandidates and run onCandidateHdlr: %v", err)

		return err
	}

	return nil
}

func (a *Agent) addRelayCandidates(ctx context.Context, generation uint64, networkTypes []NetworkType, ep relayEndpoint) { //nolint:cyclop
	if ep.conn == nil || ep.address == nil {
		return
	}

	addresses, ok := a.rewriteCandidateAddresses(CandidateTypeRelay, ep.address.String(), ep.relAddr, ep.iface)
	if !ok {
		a.closeRelayEndpoint(ep)

		return
	}

	// Candidate families are independent of the transport used to reach TURN.
	allowedNetworks := relayNetworkTypesForConfiguredCandidates(networkTypes)
	addresses = slices.DeleteFunc(addresses, func(address string) bool {
		ip := rewrittenCandidateIP(address, ep.address)
		network := NetworkTypeUDP6
		if ip.To4() != nil {
			network = NetworkTypeUDP4
		}

		return !slices.Contains(allowedNetworks, network)
	})
	if len(addresses) == 0 {
		a.closeRelayEndpoint(ep)

		return
	}

	for idx, ip := range addresses {
		onClose := ep.onClose
		if idx > 0 {
			onClose = nil
		}

		if err := a.createRelayCandidate(ctx, ep, ip, generation, onClose); err != nil {
			if idx == 0 {
				if ep.closeConn != nil {
					ep.closeConn()
				}

				return
			}

			a.log.Warnf("failed to create additional relay candidate for %s: %v", ip, err)

			continue
		}
	}
}

func (a *Agent) closeRelayEndpoint(ep relayEndpoint) {
	if ep.closeConn != nil {
		ep.closeConn()
	}
	if ep.onClose != nil {
		if err := ep.onClose(); err != nil {
			a.log.Warnf("Failed to close filtered relay connection: %v", err)
		}
	}
}
