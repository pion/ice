// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"errors"
	"net"
	"strings"
)

// NAT1To1Rule represents a rule for mapping 1:1 NAT IP addresses to candidate types.
// This includes support for hybrid deployments such as NAT64/CLAT where IPv6
// local interfaces must be advertised with stable IPv4 addresses.
type NAT1To1Rule struct {
	// PublicIPs are the 1:1 external addresses to advertise. Each entry may
	// optionally include a local IP (e.g. "203.0.113.10/10.0.0.5") to build a
	// per-address mapping; otherwise the external IP is treated as the sole
	// replacement for the candidate family.
	PublicIPs []string
	// Iface is the optional interface name to limit the rule to, empty = any.
	Iface string
	// CIDR is the optional CIDR to limit the rule to, empty = any.
	CIDR string
	// As is the candidate type to publish as,
	// the 1:1 NAT IP addresses should be mapped to.
	// IfCandidateTypeHost, NAT1To1IPs are used to replace host candidate IPs.
	// If CandidateTypeServerReflexive, it will insert a srflx candidate (as if it was derived
	// from a STUN server) with its port number being the one for the actual host candidate.
	// Other values will result in an error.
	AsCandidateType CandidateType
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

	cloned := make([]net.IP, len(src))
	for i, ip := range src {
		if ip == nil {
			continue
		}
		copied := make(net.IP, len(ip))
		copy(copied, ip)
		cloned[i] = copied
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

type natRuleMapping struct {
	rule        NAT1To1Rule
	ipv4Mapping ipMapping
	ipv6Mapping ipMapping
	cidr        *net.IPNet
	allowIPv4   bool
	allowIPv6   bool
}

func (m *natRuleMapping) hasMappings() bool {
	return m.ipv4Mapping.valid || m.ipv6Mapping.valid
}

func (m *natRuleMapping) mappingForFamily(isIPv4 bool) *ipMapping {
	if isIPv4 {
		return &m.ipv4Mapping
	}

	return &m.ipv6Mapping
}

type externalIPMapper struct {
	rulesByCandidateType map[CandidateType][]*natRuleMapping
}

//nolint:gocognit,gocyclo,cyclop
func newExternalIPMapper(rules []NAT1To1Rule) (*externalIPMapper, error) {
	if len(rules) == 0 {
		return nil, nil //nolint:nilnil
	}

	mapper := &externalIPMapper{
		rulesByCandidateType: make(map[CandidateType][]*natRuleMapping),
	}

	for _, rule := range rules {
		candidateType := rule.AsCandidateType
		if candidateType == CandidateTypeUnspecified {
			candidateType = CandidateTypeHost
		}
		if candidateType != CandidateTypeHost && candidateType != CandidateTypeServerReflexive {
			return nil, ErrUnsupportedNAT1To1IPCandidateType
		}

		if len(rule.PublicIPs) == 0 {
			continue
		}

		ruleMapping := &natRuleMapping{
			rule:        rule,
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

		for _, extIPStr := range rule.PublicIPs {
			ipPair := strings.Split(extIPStr, "/")
			if len(ipPair) == 0 || len(ipPair) > 2 {
				return nil, ErrInvalidNAT1To1IPMapping
			}

			extIP, isExtIPv4, err := validateIPString(ipPair[0])
			if err != nil {
				return nil, err
			}
			if len(ipPair) == 1 { //nolint:nestif
				if isExtIPv4 {
					if !ruleMapping.allowIPv4 {
						continue
					}
					ruleMapping.ipv4Mapping.addSoleIP(extIP)
				} else {
					if !ruleMapping.allowIPv6 {
						continue
					}
					ruleMapping.ipv6Mapping.addSoleIP(extIP)
				}
			} else {
				locIP, isLocIPv4, err := validateIPString(ipPair[1])
				if err != nil {
					return nil, err
				}
				if isExtIPv4 {
					if !isLocIPv4 {
						return nil, ErrInvalidNAT1To1IPMapping
					}

					if !ruleMapping.allowIPv4 {
						continue
					}

					ruleMapping.ipv4Mapping.addIPMapping(locIP, extIP)
					if ruleMapping.cidr != nil && !ruleMapping.cidr.Contains(locIP) {
						return nil, ErrInvalidNAT1To1IPMapping
					}
				} else {
					if isLocIPv4 {
						return nil, ErrInvalidNAT1To1IPMapping
					}

					if !ruleMapping.allowIPv6 {
						continue
					}

					ruleMapping.ipv6Mapping.addIPMapping(locIP, extIP)
					if ruleMapping.cidr != nil && !ruleMapping.cidr.Contains(locIP) {
						return nil, ErrInvalidNAT1To1IPMapping
					}
				}
			}
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

func (m *externalIPMapper) hasCandidateType(candidateType CandidateType) bool {
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

func (m *externalIPMapper) findExternalIPs(candidateType CandidateType, localIPStr string) ([]net.IP, bool, error) {
	locIP, isLocIPv4, err := validateIPString(localIPStr)
	if err != nil {
		return nil, false, err
	}

	rules := m.rulesByCandidateType[candidateType]
	foundMapping := false

	for _, rule := range rules {
		if rule.cidr != nil && !rule.cidr.Contains(locIP) {
			continue
		}

		ipMapping := rule.mappingForFamily(isLocIPv4)
		if !ipMapping.valid {
			continue
		}

		foundMapping = true

		extIP, err := ipMapping.findExternalIPs(locIP)
		if err != nil {
			if errors.Is(err, ErrExternalMappedIPNotFound) {
				continue
			}

			return nil, true, err
		}

		if len(extIP) == 0 {
			continue
		}

		return extIP, true, nil
	}

	if foundMapping {
		return nil, true, ErrExternalMappedIPNotFound
	}

	return nil, false, nil
}
