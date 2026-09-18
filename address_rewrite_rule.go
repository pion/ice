// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"fmt"
	"net"
	"regexp"
	"slices"
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
type AddressRewriteRule struct {
	// External are the 1:1 external addresses to advertise for this rule. At
	// least one valid IP address or FQDN is required.
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
	// OriginalPort and NewPort optionally rewrite a gathered candidate's
	// advertised port. Set OriginalPort to zero to rewrite every port. NewPort
	// must be non-zero unless both fields are zero, which leaves ports unchanged.
	OriginalPort int
	NewPort      int
}

var fqdnRegex = regexp.MustCompile(`^([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)

func validateFQDN(val string) bool {
	val = strings.TrimSuffix(val, ".")

	return len(val) <= 253 && fqdnRegex.MatchString(val)
}

func validateIPString(ipStr string) (net.IP, bool, error) {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return nil, false, ErrInvalidAddressRewriteMapping
	}

	return ip, (ip.To4() != nil), nil
}

// ipMapping holds a rule's external addresses for one local IP family.
type ipMapping struct {
	addresses []string
	valid     bool // A valid empty mapping suppresses candidates in replace mode.
}

func addExternalMappings(
	external []string,
	ruleMapping *addressRewriteRuleMapping,
	localAddr net.IP,
) (bool, error) {
	added := false

	for _, raw := range external {
		extIPStr := strings.TrimSpace(raw)
		extIP, isExtIPv4, err := validateIPString(extIPStr)
		if err != nil && !validateFQDN(extIPStr) {
			return false, err
		}
		if extIP != nil {
			extIPStr = extIP.String()
		}
		families := []bool{isExtIPv4}
		if extIP == nil {
			families = []bool{true, false}
		}
		if localAddr != nil {
			families = []bool{localAddr.To4() != nil}
		} else if ruleMapping.cidr != nil {
			families = []bool{ruleMapping.cidr.IP.To4() != nil}
		}

		for _, ipv4 := range families {
			if !ruleMapping.isFamilyAllowed(ipv4) {
				continue
			}
			mapping := ruleMapping.mappingForFamily(ipv4)
			mapping.addresses = append(mapping.addresses, extIPStr)
			mapping.valid = true
			added = true
		}
	}

	return added, nil
}

func maybeMarkEmptyMapping(
	ruleMapping *addressRewriteRuleMapping,
	added bool,
	localAddr net.IP,
) {
	if added {
		return
	}

	if localAddr != nil {
		localIsIPv4 := localAddr.To4() != nil
		if ruleMapping.isFamilyAllowed(localIsIPv4) {
			family := ruleMapping.mappingForFamily(localIsIPv4)
			family.valid = true
		}

		return
	}

	ruleMapping.ipv4Mapping.valid = ruleMapping.allowIPv4
	ruleMapping.ipv6Mapping.valid = ruleMapping.allowIPv6
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
			return nil, ErrUnsupportedAddressRewriteCandidateType
		}

		mode := rule.Mode
		if mode == addressRewriteModeUnspecified {
			mode = defaultAddressRewriteMode(candidateType)
		}

		rule.Local = strings.TrimSpace(rule.Local)
		ruleMapping := &addressRewriteRuleMapping{
			rule:      rule,
			mode:      mode,
			allowIPv4: true,
			allowIPv6: true,
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
				return nil, ErrInvalidAddressRewriteMapping
			}
			ruleMapping.cidr = ipNet
		}

		var (
			localAddr net.IP
			err       error
		)
		if rule.Local != "" {
			localAddr, _, err = validateIPString(rule.Local)
			if err != nil {
				return nil, err
			}

			if ruleMapping.cidr != nil && !ruleMapping.cidr.Contains(localAddr) {
				return nil, fmt.Errorf("%w: Invalid local IP is outside CIDR", ErrInvalidAddressRewriteMapping)
			}
			ruleMapping.rule.Local = localAddr.String()
		}

		added, mapErr := addExternalMappings(rule.External, ruleMapping, localAddr)
		if mapErr != nil {
			return nil, mapErr
		}
		maybeMarkEmptyMapping(ruleMapping, added, localAddr)

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
	return len(m.rulesByCandidateType[candidateType]) > 0
}

func (m *addressRewriteMapper) shouldReplace(candidateType CandidateType) bool {
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
	iface string,
) ([]string, bool, AddressRewriteMode, error) {
	locIP, isLocIPv4, err := validateIPString(localIPStr)
	if err != nil {
		return nil, false, addressRewriteModeUnspecified, err
	}

	rules := m.rulesByCandidateType[candidateType]
	ips, matched, mode := evaluateRewriteRules(rules, locIP, isLocIPv4, iface)

	return ips, matched, mode, nil
}

func (m *addressRewriteMapper) findExternalPort(
	candidateType CandidateType,
	localIP string,
	iface string,
	originalPort int,
) int {
	locIP, isLocIPv4, err := validateIPString(localIP)
	if err != nil {
		return originalPort
	}

	mappedPort := originalPort
	bestSpec := -1
	for _, rule := range m.rulesByCandidateType[candidateType] {
		if rule.rule.NewPort == 0 || (rule.rule.OriginalPort != 0 && rule.rule.OriginalPort != originalPort) {
			continue
		}

		_, ok := ruleMappingForLookup(rule, locIP, isLocIPv4, iface)
		if !ok {
			continue
		}
		if rule.rule.Local != "" {
			return rule.rule.NewPort
		}
		if spec := catchAllSpecificity(rule, iface); spec > bestSpec {
			mappedPort = rule.rule.NewPort
			bestSpec = spec
		}
	}

	return mappedPort
}

func ruleMappingForLookup(
	rule *addressRewriteRuleMapping,
	locIP net.IP,
	isLocIPv4 bool,
	iface string,
) (*ipMapping, bool) {
	if rule.rule.Iface != "" && rule.rule.Iface != iface {
		return nil, false
	}
	if rule.cidr != nil && !rule.cidr.Contains(locIP) {
		return nil, false
	}
	if rule.rule.Local != "" && rule.rule.Local != locIP.String() {
		return nil, false
	}

	ipMapping := rule.mappingForFamily(isLocIPv4)
	if !ipMapping.valid {
		return nil, false
	}

	return ipMapping, true
}

func catchAllSpecificity(rule *addressRewriteRuleMapping, iface string) int {
	spec := 0
	if rule.rule.Iface != "" {
		spec += 2
		if rule.cidr != nil {
			spec++
		}
	} else if iface == "" && rule.cidr != nil {
		spec++
	}

	return spec
}

func evaluateRewriteRules(
	rules []*addressRewriteRuleMapping,
	locIP net.IP,
	isLocIPv4 bool,
	iface string,
) (ips []string, matched bool, mode AddressRewriteMode) {
	var (
		catchAll     []string
		catchAllMode AddressRewriteMode
		bestSpec     = -1
	)

	for _, rule := range rules {
		ipMapping, ok := ruleMappingForLookup(rule, locIP, isLocIPv4, iface)
		if !ok {
			continue
		}

		if rule.rule.Local != "" {
			return slices.Clone(ipMapping.addresses), true, rule.mode
		}

		if spec := catchAllSpecificity(rule, iface); spec > bestSpec {
			catchAll = ipMapping.addresses
			catchAllMode = rule.mode
			bestSpec = spec
		}
	}

	if bestSpec >= 0 {
		return slices.Clone(catchAll), true, catchAllMode
	}

	return nil, false, addressRewriteModeUnspecified
}
