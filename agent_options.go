// SPDX-FileCopyrightText: 2025 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pion/logging"
	"github.com/pion/stun/v3"
	"github.com/pion/transport/v3"
	"golang.org/x/net/proxy"
)

// AgentOption represents a function that can be used to configure an Agent.
type AgentOption func(*Agent) error

// NominationValueGenerator is a function that generates nomination values for renomination.
type NominationValueGenerator func() uint32

// DefaultNominationValueGenerator returns a generator that starts at 1 and increments for each call.
// This provides a simple, monotonically increasing sequence suitable for renomination.
func DefaultNominationValueGenerator() NominationValueGenerator {
	var counter atomic.Uint32

	return func() uint32 {
		return counter.Add(1)
	}
}

// WithNAT1To1HostIPs appends a NAT 1:1 rule that exposes the provided public IP
// addresses as host candidates.
func WithNAT1To1HostIPs(ips ...string) AgentOption {
	return func(agent *Agent) error {
		if len(ips) == 0 {
			return nil
		}

		return appendNAT1To1Rules(agent, NAT1To1Rule{
			PublicIPs:       ips,
			AsCandidateType: CandidateTypeHost,
		})
	}
}

// WithNAT1To1SrflxIPs appends a NAT 1:1 rule that exposes the provided public
// IP addresses as server-reflexive candidates.
func WithNAT1To1SrflxIPs(ips ...string) AgentOption {
	return func(agent *Agent) error {
		if len(ips) == 0 {
			return nil
		}

		return appendNAT1To1Rules(agent, NAT1To1Rule{
			PublicIPs:       ips,
			AsCandidateType: CandidateTypeServerReflexive,
		})
	}
}

// WithNAT1To1Rules appends the provided NAT 1:1 rules to the agent's existing
// configuration. Each `NAT1To1Rule` can limit the mapping to a specific
// interface (`Iface`), CIDR block (`CIDR`), or subset of network types
// (`Networks`), allowing fine-grained control over which local addresses are
// replaced with the supplied public IPs. This covers mixed topologies such as
// NAT64/CLAT environments where IPv6-only hosts need deterministic IPv4
// representations.
//
// Matching precedence is first-determined by rule order (the earliest matching
// rule wins) and within a rule by explicit local overrides inside
// `PublicIPs` (e.g. `203.0.113.1/10.0.0.5`) are preferred, then `CIDR` scopes,
// followed by `Networks`, and finally the per-family catch-all entry.
func WithNAT1To1Rules(rules ...NAT1To1Rule) AgentOption {
	return func(agent *Agent) error {
		return appendNAT1To1Rules(agent, rules...)
	}
}

func warnOnNAT1To1Conflicts(agent *Agent) {
	if agent == nil || agent.log == nil {
		return
	}

	for _, conflict := range findNAT1To1RuleConflicts(agent.nat1To1Rules) {
		scope := conflict.scope
		scopeSummary := fmt.Sprintf(
			"candidate=%s iface=%s cidr=%s networks=%s local=%s",
			scope.candidateType.String(),
			emptyScopeValue(scope.iface),
			emptyScopeValue(scope.cidr),
			emptyScopeValue(scope.networksKey),
			scope.localKey,
		)

		message := fmt.Sprintf(
			"detected overlapping NAT 1:1 rule (%s): existing public IPs [%s], additional public IP %s",
			scopeSummary,
			strings.Join(conflict.existingPublicIPs, ", "),
			conflict.conflictingPublicIP,
		)

		agent.log.Warn(message)
	}
}

func emptyScopeValue(v string) string {
	if v == "" {
		return "*"
	}

	return v
}

func appendNAT1To1Rules(agent *Agent, rules ...NAT1To1Rule) error {
	if len(rules) == 0 {
		return nil
	}

	sanitized := make([]NAT1To1Rule, 0, len(rules))
	for _, rule := range rules {
		normalized, err := sanitizeNAT1To1Rule(rule)
		if err != nil {
			return err
		}

		sanitized = append(sanitized, normalized)
	}

	agent.nat1To1Rules = append(agent.nat1To1Rules, sanitized...)
	warnOnNAT1To1Conflicts(agent)

	return nil
}

func sanitizeNAT1To1Rule(rule NAT1To1Rule) (NAT1To1Rule, error) {
	cleaned, err := sanitizePublicIPs(rule.PublicIPs)
	if err != nil {
		return NAT1To1Rule{}, err
	}

	normalized := rule
	normalized.PublicIPs = cleaned
	if len(rule.Networks) > 0 {
		normalized.Networks = append([]NetworkType(nil), rule.Networks...)
	}

	return normalized, nil
}

func sanitizePublicIPs(ips []string) ([]string, error) {
	seen := make(map[string]struct{}, len(ips))
	sanitized := make([]string, 0, len(ips))

	for _, raw := range ips {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}

		if _, ok := seen[trimmed]; ok {
			continue
		}

		parts := strings.Split(trimmed, "/")
		if len(parts) == 0 || len(parts) > 2 {
			return nil, ErrInvalidNAT1To1IPMapping
		}

		if _, _, err := validateIPString(parts[0]); err != nil {
			return nil, err
		}

		if len(parts) == 2 {
			if _, _, err := validateIPString(parts[1]); err != nil {
				return nil, err
			}
		}

		seen[trimmed] = struct{}{}
		sanitized = append(sanitized, trimmed)
	}

	if len(sanitized) == 0 {
		return nil, ErrInvalidNAT1To1IPMapping
	}

	return sanitized, nil
}

type nat1To1ScopeKey struct {
	candidateType CandidateType
	iface         string
	cidr          string
	networksKey   string
	localKey      string
}

type nat1To1Conflict struct {
	scope               nat1To1ScopeKey
	existingPublicIPs   []string
	conflictingPublicIP string
}

func findNAT1To1RuleConflicts(rules []NAT1To1Rule) []nat1To1Conflict {
	conflicts := make([]nat1To1Conflict, 0)
	scopeState := make(map[nat1To1ScopeKey]map[string]struct{})

	for _, rule := range rules {
		candidateType := rule.AsCandidateType
		if candidateType == CandidateTypeUnspecified {
			candidateType = CandidateTypeHost
		}

		networksKey := "*"
		if len(rule.Networks) > 0 {
			names := make([]string, len(rule.Networks))
			for i, network := range rule.Networks {
				names[i] = network.String()
			}
			sort.Strings(names)
			networksKey = strings.Join(names, ",")
		}

		publicIPEntries := enumerateNAT1To1PublicIPEntries(rule.PublicIPs)
		for _, entry := range publicIPEntries {
			key := nat1To1ScopeKey{
				candidateType: candidateType,
				iface:         rule.Iface,
				cidr:          rule.CIDR,
				networksKey:   networksKey,
				localKey:      entry.localScopeKey,
			}

			existing := scopeState[key]
			if existing == nil {
				existing = make(map[string]struct{})
				scopeState[key] = existing
			}

			if len(existing) > 0 {
				if _, ok := existing[entry.publicIP]; !ok {
					conflicts = append(conflicts, nat1To1Conflict{
						scope:               key,
						existingPublicIPs:   mapKeys(existing),
						conflictingPublicIP: entry.publicIP,
					})
				}
			}

			existing[entry.publicIP] = struct{}{}
		}
	}

	return conflicts
}

type nat1To1PublicIPEntry struct {
	publicIP      string
	localScopeKey string
}

func enumerateNAT1To1PublicIPEntries(publicIPs []string) []nat1To1PublicIPEntry {
	if len(publicIPs) == 0 {
		return nil
	}

	entries := make([]nat1To1PublicIPEntry, 0, len(publicIPs))

	for _, mapping := range publicIPs {
		if mapping == "" {
			continue
		}

		parts := strings.Split(mapping, "/")
		publicIP := strings.TrimSpace(parts[0])
		if publicIP == "" {
			continue
		}

		entries = append(entries, nat1To1PublicIPEntry{
			publicIP:      publicIP,
			localScopeKey: deriveNAT1To1ScopeKey(publicIP, parts),
		})
	}

	return entries
}

func deriveNAT1To1ScopeKey(publicIP string, parts []string) string {
	if len(parts) > 1 {
		local := strings.TrimSpace(parts[1])
		if local != "" {
			return "local:" + local
		}

		return "family:unknown"
	}

	ip, _, err := validateIPString(publicIP)
	if err != nil {
		return "family:unknown"
	}

	if ip.To4() != nil {
		return "family:ipv4"
	}

	return "family:ipv6"
}

func mapKeys(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}

	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	return keys
}

// WithICELite configures whether the agent operates in lite mode.
// Lite agents do not perform connectivity checks and only provide host candidates.
func WithICELite(lite bool) AgentOption {
	return func(a *Agent) error {
		a.lite = lite

		return nil
	}
}

// WithUrls sets the STUN/TURN server URLs used by the agent.
func WithUrls(urls []*stun.URI) AgentOption {
	return func(a *Agent) error {
		if len(urls) == 0 {
			a.urls = nil

			return nil
		}

		cloned := make([]*stun.URI, len(urls))
		copy(cloned, urls)
		a.urls = cloned

		return nil
	}
}

// WithPortRange sets the UDP port range for host candidates.
func WithPortRange(portMin, portMax uint16) AgentOption {
	return func(a *Agent) error {
		if portMax < portMin {
			return ErrPort
		}

		a.portMin = portMin
		a.portMax = portMax

		return nil
	}
}

// WithDisconnectedTimeout sets the duration before the agent transitions to disconnected state.
// A timeout of 0 disables the transition.
func WithDisconnectedTimeout(timeout time.Duration) AgentOption {
	return func(a *Agent) error {
		a.disconnectedTimeout = timeout

		return nil
	}
}

// WithFailedTimeout sets the duration before the agent transitions to failed state after disconnected.
// A timeout of 0 disables the transition.
func WithFailedTimeout(timeout time.Duration) AgentOption {
	return func(a *Agent) error {
		a.failedTimeout = timeout

		return nil
	}
}

// WithKeepaliveInterval sets how often ICE keepalive packets are sent.
// An interval of 0 disables keepalives.
func WithKeepaliveInterval(interval time.Duration) AgentOption {
	return func(a *Agent) error {
		a.keepaliveInterval = interval

		return nil
	}
}

// WithHostAcceptanceMinWait sets the minimum wait before selecting host candidates.
func WithHostAcceptanceMinWait(wait time.Duration) AgentOption {
	return func(a *Agent) error {
		a.hostAcceptanceMinWait = wait

		return nil
	}
}

// WithSrflxAcceptanceMinWait sets the minimum wait before selecting srflx candidates.
func WithSrflxAcceptanceMinWait(wait time.Duration) AgentOption {
	return func(a *Agent) error {
		a.srflxAcceptanceMinWait = wait

		return nil
	}
}

// WithPrflxAcceptanceMinWait sets the minimum wait before selecting prflx candidates.
func WithPrflxAcceptanceMinWait(wait time.Duration) AgentOption {
	return func(a *Agent) error {
		a.prflxAcceptanceMinWait = wait

		return nil
	}
}

// WithRelayAcceptanceMinWait sets the minimum wait before selecting relay candidates.
func WithRelayAcceptanceMinWait(wait time.Duration) AgentOption {
	return func(a *Agent) error {
		a.relayAcceptanceMinWait = wait

		return nil
	}
}

// WithSTUNGatherTimeout sets the STUN gather timeout.
func WithSTUNGatherTimeout(timeout time.Duration) AgentOption {
	return func(a *Agent) error {
		a.stunGatherTimeout = timeout

		return nil
	}
}

// WithIPFilter sets a filter for IP addresses used during candidate gathering.
func WithIPFilter(filter func(net.IP) bool) AgentOption {
	return func(a *Agent) error {
		a.ipFilter = filter

		return nil
	}
}

// WithNet sets the underlying network implementation for the agent.
func WithNet(net transport.Net) AgentOption {
	return func(a *Agent) error {
		a.net = net

		return nil
	}
}

// WithMulticastDNSMode configures mDNS behavior for the agent.
func WithMulticastDNSMode(mode MulticastDNSMode) AgentOption {
	return func(a *Agent) error {
		a.mDNSMode = mode

		return nil
	}
}

// WithMulticastDNSHostName sets the mDNS host name used by the agent.
func WithMulticastDNSHostName(hostName string) AgentOption {
	return func(a *Agent) error {
		if !strings.HasSuffix(hostName, ".local") || len(strings.Split(hostName, ".")) != 2 {
			return ErrInvalidMulticastDNSHostName
		}

		a.mDNSName = hostName

		return nil
	}
}

// WithLocalCredentials sets the local ICE username fragment and password used during Restart.
// If empty strings are provided, the agent will generate values during Restart.
func WithLocalCredentials(ufrag, pwd string) AgentOption {
	return func(a *Agent) error {
		if ufrag != "" && len([]rune(ufrag))*8 < 24 {
			return ErrLocalUfragInsufficientBits
		}
		if pwd != "" && len([]rune(pwd))*8 < 128 {
			return ErrLocalPwdInsufficientBits
		}

		a.localUfrag = ufrag
		a.localPwd = pwd

		return nil
	}
}

// WithTCPMux sets the TCP mux for ICE TCP multiplexing.
func WithTCPMux(tcpMux TCPMux) AgentOption {
	return func(a *Agent) error {
		a.tcpMux = tcpMux

		return nil
	}
}

// WithUDPMux sets the UDP mux used for multiplexing host candidates.
func WithUDPMux(udpMux UDPMux) AgentOption {
	return func(a *Agent) error {
		a.udpMux = udpMux

		return nil
	}
}

// WithUDPMuxSrflx sets the UDP mux for server reflexive candidates.
func WithUDPMuxSrflx(udpMuxSrflx UniversalUDPMux) AgentOption {
	return func(a *Agent) error {
		a.udpMuxSrflx = udpMuxSrflx

		return nil
	}
}

// WithProxyDialer sets the proxy dialer used for TURN over TCP/TLS/DTLS connections.
func WithProxyDialer(dialer proxy.Dialer) AgentOption {
	return func(a *Agent) error {
		a.proxyDialer = dialer

		return nil
	}
}

// WithMaxBindingRequests sets the maximum number of binding requests before considering a pair failed.
func WithMaxBindingRequests(limit uint16) AgentOption {
	return func(a *Agent) error {
		a.maxBindingRequests = limit

		return nil
	}
}

// WithCheckInterval sets how often the agent runs connectivity checks while connecting.
func WithCheckInterval(interval time.Duration) AgentOption {
	return func(a *Agent) error {
		a.checkInterval = interval

		return nil
	}
}

// WithRenomination enables ICE renomination as described in draft-thatcher-ice-renomination-01.
// When enabled, the controlling agent can renominate candidate pairs multiple times
// and the controlled agent follows "last nomination wins" rule.
//
// The generator parameter specifies how nomination values are generated.
// Use DefaultNominationValueGenerator() for a simple incrementing counter,
// or provide a custom generator for more complex scenarios.
//
// Example:
//
//	agent, err := NewAgentWithOptions(config, WithRenomination(DefaultNominationValueGenerator()))
func WithRenomination(generator NominationValueGenerator) AgentOption {
	return func(a *Agent) error {
		if generator == nil {
			return ErrInvalidNominationValueGenerator
		}
		a.enableRenomination = true
		a.nominationValueGenerator = generator

		return nil
	}
}

// WithNominationAttribute sets the STUN attribute type to use for ICE renomination.
// The default value is 0x0030. This can be configured until the attribute is officially
// assigned by IANA for draft-thatcher-ice-renomination.
//
// This option returns an error if the provided attribute type is invalid.
// Currently, validation ensures the attribute is not 0x0000 (reserved).
// Additional validation may be added in the future.
func WithNominationAttribute(attrType uint16) AgentOption {
	return func(a *Agent) error {
		// Basic validation: ensure it's not the reserved 0x0000
		if attrType == 0x0000 {
			return ErrInvalidNominationAttribute
		}

		a.nominationAttribute = stun.AttrType(attrType)

		return nil
	}
}

// WithIncludeLoopback includes loopback addresses in the candidate list.
// By default, loopback addresses are excluded.
//
// Example:
//
//	agent, err := NewAgentWithOptions(WithIncludeLoopback())
func WithIncludeLoopback() AgentOption {
	return func(a *Agent) error {
		a.includeLoopback = true

		return nil
	}
}

// WithTCPPriorityOffset sets a number which is subtracted from the default (UDP) candidate type preference
// for host, srflx and prfx candidate types. It helps to configure relative preference of UDP candidates
// against TCP ones. Relay candidates for TCP and UDP are always 0 and not affected by this setting.
// When not set, defaultTCPPriorityOffset (27) is used.
//
// Example:
//
//	agent, err := NewAgentWithOptions(WithTCPPriorityOffset(50))
func WithTCPPriorityOffset(offset uint16) AgentOption {
	return func(a *Agent) error {
		a.tcpPriorityOffset = offset

		return nil
	}
}

// WithDisableActiveTCP disables Active TCP candidates.
// When TCP is enabled, Active TCP candidates will be created when a new passive TCP remote candidate is added
// unless this option is used.
//
// Example:
//
//	agent, err := NewAgentWithOptions(WithDisableActiveTCP())
func WithDisableActiveTCP() AgentOption {
	return func(a *Agent) error {
		a.disableActiveTCP = true

		return nil
	}
}

// WithBindingRequestHandler sets a handler to allow applications to perform logic on incoming STUN Binding Requests.
// This was implemented to allow users to:
//   - Log incoming Binding Requests for debugging
//   - Implement draft-thatcher-ice-renomination
//   - Implement custom CandidatePair switching logic
//
// Example:
//
//	handler := func(m *stun.Message, local, remote Candidate, pair *CandidatePair) bool {
//		log.Printf("Binding request from %s to %s", remote.Address(), local.Address())
//		return true // Accept the request
//	}
//	agent, err := NewAgentWithOptions(WithBindingRequestHandler(handler))
func WithBindingRequestHandler(
	handler func(m *stun.Message, local, remote Candidate, pair *CandidatePair) bool,
) AgentOption {
	return func(a *Agent) error {
		a.userBindingRequestHandler = handler

		return nil
	}
}

// WithEnableUseCandidateCheckPriority enables checking for equal or higher priority when
// switching selected candidate pair if the peer requests USE-CANDIDATE and agent is a lite agent.
// This is disabled by default, i.e. when peer requests USE-CANDIDATE, the selected pair will be
// switched to that irrespective of relative priority between current selected pair
// and priority of the pair being switched to.
//
// Example:
//
//	agent, err := NewAgentWithOptions(WithEnableUseCandidateCheckPriority())
func WithEnableUseCandidateCheckPriority() AgentOption {
	return func(a *Agent) error {
		a.enableUseCandidateCheckPriority = true

		return nil
	}
}

// WithContinualGatheringPolicy sets the continual gathering policy for the agent.
// When set to GatherContinually, the agent will continuously monitor network interfaces
// and gather new candidates as they become available.
// When set to GatherOnce (default), gathering completes after the initial phase.
//
// Example:
//
//	agent, err := NewAgentWithOptions(WithContinualGatheringPolicy(GatherContinually))
func WithContinualGatheringPolicy(policy ContinualGatheringPolicy) AgentOption {
	return func(a *Agent) error {
		a.continualGatheringPolicy = policy

		return nil
	}
}

// WithNetworkMonitorInterval sets the interval at which the agent checks for network interface changes
// when using GatherContinually policy. This option only has effect when used with
// WithContinualGatheringPolicy(GatherContinually).
// Default is 2 seconds if not specified.
//
// Example:
//
//	agent, err := NewAgentWithOptions(
//		WithContinualGatheringPolicy(GatherContinually),
//		WithNetworkMonitorInterval(5 * time.Second),
//	)
func WithNetworkMonitorInterval(interval time.Duration) AgentOption {
	return func(a *Agent) error {
		if interval <= 0 {
			return ErrInvalidNetworkMonitorInterval
		}
		a.networkMonitorInterval = interval

		return nil
	}
}

// WithNetworkTypes sets the enabled network types for candidate gathering.
// By default, all network types are enabled.
//
// Example:
//
//	agent, err := NewAgentWithOptions(
//		WithNetworkTypes([]NetworkType{NetworkTypeUDP4, NetworkTypeUDP6}),
//	)
func WithNetworkTypes(networkTypes []NetworkType) AgentOption {
	return func(a *Agent) error {
		a.networkTypes = networkTypes

		return nil
	}
}

// WithCandidateTypes sets the enabled candidate types for gathering.
// By default, host, server reflexive, and relay candidates are enabled.
//
// Example:
//
//	agent, err := NewAgentWithOptions(
//		WithCandidateTypes([]CandidateType{CandidateTypeHost, CandidateTypeServerReflexive}),
//	)
func WithCandidateTypes(candidateTypes []CandidateType) AgentOption {
	return func(a *Agent) error {
		a.candidateTypes = candidateTypes

		return nil
	}
}

// WithAutomaticRenomination enables automatic renomination of candidate pairs
// when better pairs become available after initial connection establishment.
// This feature requires renomination to be enabled and both agents to support it.
//
// When enabled, the controlling agent will periodically evaluate candidate pairs
// and renominate if a significantly better pair is found (e.g., switching from
// relay to direct connection, or when RTT improves significantly).
//
// The interval parameter specifies the minimum time to wait after connection
// before considering automatic renomination. If set to 0, it defaults to 3 seconds.
//
// Example:
//
//	agent, err := NewAgentWithOptions(
//		WithRenomination(DefaultNominationValueGenerator()),
//		WithAutomaticRenomination(3*time.Second),
//	)
func WithAutomaticRenomination(interval time.Duration) AgentOption {
	return func(a *Agent) error {
		a.automaticRenomination = true
		if interval > 0 {
			a.renominationInterval = interval
		}
		// Note: renomination must be enabled separately via WithRenomination
		return nil
	}
}

// WithInterfaceFilter sets a filter function to whitelist or blacklist network interfaces
// for ICE candidate gathering.
//
// The filter function receives the interface name and should return true to keep the interface,
// or false to exclude it.
//
// Example:
//
//	// Only use interfaces starting with "eth"
//	agent, err := NewAgentWithOptions(
//		WithInterfaceFilter(func(interfaceName string) bool {
//			return len(interfaceName) >= 3 && interfaceName[:3] == "eth"
//		}),
//	)
func WithInterfaceFilter(filter func(string) bool) AgentOption {
	return func(a *Agent) error {
		a.interfaceFilter = filter

		return nil
	}
}

// WithLoggerFactory sets the logger factory for the agent.
//
// Example:
//
//	import "github.com/pion/logging"
//
//	loggerFactory := logging.NewDefaultLoggerFactory()
//	loggerFactory.DefaultLogLevel = logging.LogLevelDebug
//	agent, err := NewAgentWithOptions(WithLoggerFactory(loggerFactory))
func WithLoggerFactory(loggerFactory logging.LoggerFactory) AgentOption {
	return func(a *Agent) error {
		a.log = loggerFactory.NewLogger("ice")

		return nil
	}
}
