// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"net"
	"strings"
)

// AddressRewriteMode controls whether a rule replaces or appends candidates.
type AddressRewriteMode int

const (
	addressRewriteModeUnspecified AddressRewriteMode = iota
	AddressRewriteReplace
	AddressRewriteAppend
)

// AddressRewriteRule represents a rule for remapping candidate addresses.
// This includes support for hybrid deployments such as NAT64/CLAT where IPv6
// local interfaces must be advertised with stable IPv4 addresses.
type AddressRewriteRule struct {
	// External are the 1:1 external addresses to advertise for this rule.
	External []string
	// Local optionally pins this rule to a specific local address. When set,
	// external IPs map to that address regardless of IP family. When empty,
	// External acts as a catch-all for the family implied by the local scope
	// (CIDR when set, otherwise the external IP family).
	Local string
	// Iface is the optional interface name to limit the rule to, empty = any.
	Iface string
	// CIDR is the optional CIDR to limit the rule to, empty = any.
	CIDR string
	// AsCandidateType is the candidate type to publish as for this rule. Defaults to host
	// when unspecified. Supported values: host, server reflexive, relay.
	AsCandidateType CandidateType
	// Mode controls whether we replace the original candidate or append extra
	// candidates.
	//
	// If Mode is zero, the default is:
	//   - CandidateTypeHost           -> AddressRewriteReplace
	//   - CandidateTypeServerReflexive, CandidateTypeRelay -> AddressRewriteAppend
	Mode AddressRewriteMode
	// Networks is the optional networks to limit the rule to, nil/empty = all.
	Networks []NetworkType
}

func validateIPString(ipStr string) (net.IP, bool, error) {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return nil, false, ErrInvalidNAT1To1IPMapping
	}

	return ip, (ip.To4() != nil), nil
}

// ipMapping holds the mapping of local and external IP address
//
//	for a particular IP family.
type ipMapping struct {
	ipSole []net.IP            // When non-empty, these are the catch-all external IPs for one local IP family
	ipMap  map[string][]net.IP // Local-to-external IP mapping (k: local, v: external IPs)
	valid  bool                // If not set any external IP, valid is false
}

func newIPMapping() ipMapping {
	return ipMapping{
		ipMap: make(map[string][]net.IP),
	}
}

func (m *ipMapping) addSoleIP(ip net.IP) {
	m.ipSole = append(m.ipSole, ip)
	m.valid = true
}

func (m *ipMapping) addIPMapping(locIP, extIP net.IP) {
	locIPStr := locIP.String()

	m.ipMap[locIPStr] = append(m.ipMap[locIPStr], extIP)
	m.valid = true
}

func cloneIPs(src []net.IP) []net.IP {
	if len(src) == 0 {
		return nil
	}

	cloned := make([]net.IP, 0, len(src))
	for _, ip := range src {
		if ip == nil {
			continue
		}
		copied := make(net.IP, len(ip))
		copy(copied, ip)
		cloned = append(cloned, copied)
	}

	return cloned
}

func (m *ipMapping) findExternalIPs(locIP net.IP) ([]net.IP, error) {
	if !m.valid {
		return nil, nil
	}

	if m.ipMap != nil {
		if extIPs, ok := m.ipMap[locIP.String()]; ok && len(extIPs) > 0 {
			return cloneIPs(extIPs), nil
		}
	}

	if len(m.ipSole) > 0 {
		return cloneIPs(m.ipSole), nil
	}

	return nil, ErrExternalMappedIPNotFound
}

type addressRewriteRuleMapping struct {
	rule        AddressRewriteRule
	mode        AddressRewriteMode
	ipv4Mapping ipMapping
	ipv6Mapping ipMapping
	cidr        *net.IPNet
	allowIPv4   bool
	allowIPv6   bool
}

func (m *addressRewriteRuleMapping) hasMappings() bool {
	return m.ipv4Mapping.valid || m.ipv6Mapping.valid
}

func (m *addressRewriteRuleMapping) mappingForFamily(isIPv4 bool) *ipMapping {
	if isIPv4 {
		return &m.ipv4Mapping
	}

	return &m.ipv6Mapping
}

func (m *addressRewriteRuleMapping) isFamilyAllowed(isLocalIPv4 bool) bool {
	if isLocalIPv4 {
		return m.allowIPv4
	}

	return m.allowIPv6
}

func (m *addressRewriteRuleMapping) addImplicitMapping(
	extIP net.IP,
	isLocalIPv4 bool,
	hasLocalAddr bool,
	localAddr net.IP,
) {
	mapping := m.mappingForFamily(isLocalIPv4)
	if hasLocalAddr {
		mapping.addIPMapping(localAddr, extIP)
	} else {
		mapping.addSoleIP(extIP)
	}
}

type addressRewriteMapper struct {
	rulesByCandidateType map[CandidateType][]*addressRewriteRuleMapping
}

//nolint:gocognit,gocyclo,cyclop
func newAddressRewriteMapper(rules []AddressRewriteRule) (*addressRewriteMapper, error) {
	if len(rules) == 0 {
		return nil, nil //nolint:nilnil
	}

	mapper := &addressRewriteMapper{
		rulesByCandidateType: make(map[CandidateType][]*addressRewriteRuleMapping),
	}

	for _, rule := range rules {
		candidateType := rule.AsCandidateType
		if candidateType == CandidateTypeUnspecified {
			candidateType = CandidateTypeHost
		}
		if candidateType == CandidateTypePeerReflexive {
			return nil, ErrUnsupportedNAT1To1IPCandidateType
		}

		if len(rule.External) == 0 {
			continue
		}

		mode := rule.Mode
		if mode == addressRewriteModeUnspecified {
			mode = defaultAddressRewriteMode(candidateType)
		}

		ruleMapping := &addressRewriteRuleMapping{
			rule:        rule,
			mode:        mode,
			ipv4Mapping: newIPMapping(),
			ipv6Mapping: newIPMapping(),
			allowIPv4:   true,
			allowIPv6:   true,
		}

		if len(rule.Networks) > 0 {
			ruleMapping.allowIPv4 = false
			ruleMapping.allowIPv6 = false
			for _, network := range rule.Networks {
				if network.IsIPv4() {
					ruleMapping.allowIPv4 = true
				}
				if network.IsIPv6() {
					ruleMapping.allowIPv6 = true
				}
			}
			if !ruleMapping.allowIPv4 && !ruleMapping.allowIPv6 {
				continue
			}
		}
		if rule.CIDR != "" {
			_, ipNet, err := net.ParseCIDR(rule.CIDR)
			if err != nil {
				return nil, ErrInvalidNAT1To1IPMapping
			}
			ruleMapping.cidr = ipNet
		}

		var (
			localAddr    net.IP
			localIsIPv4  bool
			hasLocalAddr bool
			err          error
		)
		if trimmedLocal := strings.TrimSpace(rule.Local); trimmedLocal != "" {
			localAddr, localIsIPv4, err = validateIPString(trimmedLocal)
			if err != nil {
				return nil, err
			}
			hasLocalAddr = true

			if ruleMapping.cidr != nil && !ruleMapping.cidr.Contains(localAddr) {
				return nil, ErrInvalidNAT1To1IPMapping
			}
		}

		for _, raw := range rule.External {
			extIPStr := strings.TrimSpace(raw)
			ipPair := strings.Split(extIPStr, "/")
			if len(ipPair) != 1 {
				return nil, ErrInvalidNAT1To1IPMapping
			}

			extIP, isExtIPv4, err := validateIPString(ipPair[0])
			if err != nil {
				return nil, err
			}

			targetLocalIPv4 := isExtIPv4
			if hasLocalAddr {
				targetLocalIPv4 = localIsIPv4
			} else if ruleMapping.cidr != nil {
				targetLocalIPv4 = ruleMapping.cidr.IP.To4() != nil
			}

			if !ruleMapping.isFamilyAllowed(targetLocalIPv4) {
				continue
			}

			ruleMapping.addImplicitMapping(extIP, targetLocalIPv4, hasLocalAddr, localAddr)
		}

		if ruleMapping.hasMappings() {
			mapper.rulesByCandidateType[candidateType] = append(mapper.rulesByCandidateType[candidateType], ruleMapping)
		}
	}

	if len(mapper.rulesByCandidateType) == 0 {
		return nil, nil //nolint:nilnil
	}

	return mapper, nil
}

func (m *addressRewriteMapper) hasCandidateType(candidateType CandidateType) bool {
	if m == nil {
		return false
	}

	rules := m.rulesByCandidateType[candidateType]
	for _, rule := range rules {
		if rule.hasMappings() {
			return true
		}
	}

	return false
}

func (m *addressRewriteMapper) shouldReplace(candidateType CandidateType) bool {
	if m == nil {
		return false
	}

	for _, rule := range m.rulesByCandidateType[candidateType] {
		if rule.mode == AddressRewriteReplace {
			return true
		}
	}

	return false
}

func (m *addressRewriteMapper) findExternalIPs(
	candidateType CandidateType,
	localIPStr string,
) ([]net.IP, bool, AddressRewriteMode, error) {
	locIP, isLocIPv4, err := validateIPString(localIPStr)
	if err != nil {
		return nil, false, addressRewriteModeUnspecified, err
	}

	rules := m.rulesByCandidateType[candidateType]
	var (
		catchAll     []net.IP
		catchAllMode AddressRewriteMode
		hasCatchAll  bool
		foundMapping bool
	)

	for _, rule := range rules {
		if rule.cidr != nil && !rule.cidr.Contains(locIP) {
			continue
		}

		ipMapping := rule.mappingForFamily(isLocIPv4)
		if !ipMapping.valid {
			continue
		}

		foundMapping = true

		if explicit := ipMapping.ipMap[locIP.String()]; len(explicit) > 0 {
			return cloneIPs(explicit), true, rule.mode, nil
		}

		if len(ipMapping.ipSole) > 0 && !hasCatchAll {
			catchAll = cloneIPs(ipMapping.ipSole)
			catchAllMode = rule.mode
			hasCatchAll = true
		}
	}

	if hasCatchAll {
		return externalIPResult(catchAll, catchAllMode, true, false)
	}

	return externalIPResult(nil, addressRewriteModeUnspecified, false, foundMapping)
}

func externalIPResult(
	ips []net.IP,
	mode AddressRewriteMode,
	hasCatchAll bool,
	foundMapping bool,
) ([]net.IP, bool, AddressRewriteMode, error) {
	if hasCatchAll {
		return ips, true, mode, nil
	}

	if foundMapping {
		return nil, true, addressRewriteModeUnspecified, ErrExternalMappedIPNotFound
	}

	return nil, false, addressRewriteModeUnspecified, nil
}
