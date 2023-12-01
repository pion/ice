// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"net"
	"strings"
)

func validateIPString(ipStr string) (net.IP, bool, error) {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return nil, false, ErrInvalidNAT1To1IPMapping
	}
	return ip, (ip.To4() != nil), nil
}

// ipMapping holds the mapping of local and external IP address for a particular IP family
type ipMapping struct {
	// When non-nil, this is the sole external IP for one local IP assumed
	ipSole net.IP
	// Local-to-external IP mapping (k: local, v: []external). We allow an
	// additional external IP if it matches the local IP.
	ipMap map[string][]net.IP
	// If not set any external IP, valid is false
	valid bool
}

func (m *ipMapping) setSoleIP(ip net.IP) error {
	if m.ipSole != nil || len(m.ipMap) > 0 {
		return ErrInvalidNAT1To1IPMapping
	}

	m.ipSole = ip
	m.valid = true

	return nil
}

func (m *ipMapping) addIPMapping(locIP, extIP net.IP) error {
	if m.ipSole != nil {
		return ErrInvalidNAT1To1IPMapping
	}

	locIPStr := locIP.String()

	extIPs, ok := m.ipMap[locIPStr]
	// We only allow mapping a local address to either a single external address,
	// itself or both.
	if ok && len(extIPs) > 1 {
		return ErrInvalidNAT1To1IPMapping
	}

	// De-duplication check.
	for _, ip := range extIPs {
		// If the address is external we only allow one.
		if locIPStr != extIP.String() && ip.String() != locIPStr {
			return ErrInvalidNAT1To1IPMapping
		}

		// Otherwise the local IP can only map to itself once.
		if ip.String() == extIP.String() {
			return ErrInvalidNAT1To1IPMapping
		}
	}

	m.ipMap[locIPStr] = append(m.ipMap[locIPStr], extIP)
	m.valid = true

	return nil
}

func (m *ipMapping) findExternalIPs(locIP net.IP) ([]net.IP, error) {
	if !m.valid {
		return []net.IP{locIP}, nil
	}

	if m.ipSole != nil {
		return []net.IP{m.ipSole}, nil
	}

	extIPs, ok := m.ipMap[locIP.String()]
	if !ok || len(extIPs) == 0 {
		return nil, ErrExternalMappedIPNotFound
	}

	return extIPs, nil
}

type externalIPMapper struct {
	ipv4Mapping   ipMapping
	ipv6Mapping   ipMapping
	candidateType CandidateType
}

func newExternalIPMapper(candidateType CandidateType, ips []string) (*externalIPMapper, error) { //nolint:gocognit
	if len(ips) == 0 {
		return nil, nil //nolint:nilnil
	}
	if candidateType == CandidateTypeUnspecified {
		candidateType = CandidateTypeHost // Defaults to host
	} else if candidateType != CandidateTypeHost && candidateType != CandidateTypeServerReflexive {
		return nil, ErrUnsupportedNAT1To1IPCandidateType
	}

	m := &externalIPMapper{
		ipv4Mapping:   ipMapping{ipMap: map[string][]net.IP{}},
		ipv6Mapping:   ipMapping{ipMap: map[string][]net.IP{}},
		candidateType: candidateType,
	}

	for _, extIPStr := range ips {
		ipPair := strings.Split(extIPStr, "/")
		if len(ipPair) == 0 || len(ipPair) > 2 {
			return nil, ErrInvalidNAT1To1IPMapping
		}

		extIP, isExtIPv4, err := validateIPString(ipPair[0])
		if err != nil {
			return nil, err
		}
		if len(ipPair) == 1 {
			if isExtIPv4 {
				if err := m.ipv4Mapping.setSoleIP(extIP); err != nil {
					return nil, err
				}
			} else {
				if err := m.ipv6Mapping.setSoleIP(extIP); err != nil {
					return nil, err
				}
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

				if err := m.ipv4Mapping.addIPMapping(locIP, extIP); err != nil {
					return nil, err
				}
			} else {
				if isLocIPv4 {
					return nil, ErrInvalidNAT1To1IPMapping
				}

				if err := m.ipv6Mapping.addIPMapping(locIP, extIP); err != nil {
					return nil, err
				}
			}
		}
	}

	return m, nil
}

func (m *externalIPMapper) findExternalIPs(localIPStr string) ([]net.IP, error) {
	locIP, isLocIPv4, err := validateIPString(localIPStr)
	if err != nil {
		return nil, err
	}

	if isLocIPv4 {
		return m.ipv4Mapping.findExternalIPs(locIP)
	}

	return m.ipv6Mapping.findExternalIPs(locIP)
}
