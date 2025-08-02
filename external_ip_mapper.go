// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"net"
	"strconv"
	"strings"
)

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
	ipSole net.IP            // When non-nil, this is the sole external IP for one local IP assumed
	ipMap  map[string]net.IP // Local-to-external IP mapping (k: local, v: external)
	valid  bool              // If not set any external IP, valid is false
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

	// Check if dup of local IP
	if _, ok := m.ipMap[locIPStr]; ok {
		return ErrInvalidNAT1To1IPMapping
	}

	m.ipMap[locIPStr] = extIP
	m.valid = true

	return nil
}

func (m *ipMapping) findExternalIP(locIP net.IP) (net.IP, error) {
	if !m.valid {
		return locIP, nil
	}

	if m.ipSole != nil {
		return m.ipSole, nil
	}

	extIP, ok := m.ipMap[locIP.String()]
	if !ok {
		return nil, ErrExternalMappedIPNotFound
	}

	return extIP, nil
}

type externalIPMapper struct {
	ipv4Mapping   ipMapping
	ipv6Mapping   ipMapping
	candidateType CandidateType
}

//nolint:gocognit,cyclop
func newExternalIPMapper(
	candidateType CandidateType,
	ips []string,
) (*externalIPMapper, error) {
	if len(ips) == 0 {
		return nil, nil //nolint:nilnil
	}
	if candidateType == CandidateTypeUnspecified {
		candidateType = CandidateTypeHost // Defaults to host
	} else if candidateType != CandidateTypeHost && candidateType != CandidateTypeServerReflexive {
		return nil, ErrUnsupportedNAT1To1IPCandidateType
	}

	mapper := &externalIPMapper{
		ipv4Mapping:   ipMapping{ipMap: map[string]net.IP{}},
		ipv6Mapping:   ipMapping{ipMap: map[string]net.IP{}},
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
		if len(ipPair) == 1 { //nolint:nestif
			if isExtIPv4 {
				if err := mapper.ipv4Mapping.setSoleIP(extIP); err != nil {
					return nil, err
				}
			} else {
				if err := mapper.ipv6Mapping.setSoleIP(extIP); err != nil {
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

				if err := mapper.ipv4Mapping.addIPMapping(locIP, extIP); err != nil {
					return nil, err
				}
			} else {
				if isLocIPv4 {
					return nil, ErrInvalidNAT1To1IPMapping
				}

				if err := mapper.ipv6Mapping.addIPMapping(locIP, extIP); err != nil {
					return nil, err
				}
			}
		}
	}

	return mapper, nil
}

func (m *externalIPMapper) findExternalIP(localIPStr string) (net.IP, error) {
	locIP, isLocIPv4, err := validateIPString(localIPStr)
	if err != nil {
		return nil, err
	}

	if isLocIPv4 {
		return m.ipv4Mapping.findExternalIP(locIP)
	}

	return m.ipv6Mapping.findExternalIP(locIP)
}

// ----- Extended per-protocol, port-aware mapping support (optional) -----

type endpoint struct {
	ip   net.IP
	port int // 0 = use local
}

type protoMaps struct {
	// defaults per family
	v4Default *endpoint
	v6Default *endpoint
	// per-local mappings
	v4 map[string]endpoint
	v6 map[string]endpoint
}

type externalIPMapperAdvanced struct {
	udp protoMaps
	tcp protoMaps
	candidateType CandidateType
}

// newExternalIPMapperFromAdvanced creates a mapper using advanced entries.
// Entry format:
//
//	"udp:extIP[:port]" or "tcp:extIP[:port]" (sole per-proto)
//	"udp:extIP[:port]/localIP" or "tcp:extIP[:port]/localIP" (per-local per-proto)
//
// IPv6 with port must be bracketed (e.g. tcp:[2001:db8::1]:443/fe80::1).
func newExternalIPMapperAdvanced(candidateType CandidateType, entries []string) (*externalIPMapperAdvanced, error) {
	if len(entries) == 0 {
		return nil, nil //nolint:nilnil
	}
	if candidateType == CandidateTypeUnspecified {
		candidateType = CandidateTypeHost
	} else if candidateType != CandidateTypeHost && candidateType != CandidateTypeServerReflexive {
		return nil, ErrUnsupportedNAT1To1IPCandidateType
	}	

	mapper := &externalIPMapperAdvanced{
		udp: protoMaps{
			v4: make(map[string]endpoint),
			v6: make(map[string]endpoint),
		},
		tcp: protoMaps{
			v4: make(map[string]endpoint),
			v6: make(map[string]endpoint),
		},
		candidateType: candidateType,
	}

	parseExt := func(s string) (net.IP, int, error) {
		if h, p, err := net.SplitHostPort(s); err == nil {
			ip, _, err2 := validateIPString(h)
			if err2 != nil {
				return nil, 0, err2
			}
			port, perr := strconv.Atoi(p)
			if perr != nil || port < 0 || port > 65535 {
				return nil, 0, ErrInvalidNAT1To1IPMapping
			}
			return ip, port, nil
		}
		ip, _, err := validateIPString(s)
		if err != nil {
			return nil, 0, err
		}
		return ip, 0, nil
	}

	for _, raw := range entries {
		s := strings.TrimSpace(raw)
		proto := ""
		if strings.HasPrefix(strings.ToLower(s), "udp:") {
			proto = "udp"
			s = s[4:]
		} else if strings.HasPrefix(strings.ToLower(s), "tcp:") {
			proto = "tcp"
			s = s[4:]
		}
		parts := strings.Split(s, "/")
		if len(parts) == 0 || len(parts) > 2 {
			return nil, ErrInvalidExternalIPMapping
		}
		extIP, extPort, err := parseExt(strings.TrimSpace(parts[0]))
		if err != nil {
			return nil, err
		}
		isV4 := extIP.To4() != nil
		var localIP net.IP
		if len(parts) == 2 {
			localIP, _, err = validateIPString(strings.TrimSpace(parts[1]))
			if err != nil {
				return nil, err
			}
			if (localIP.To4() != nil) != isV4 {
				return nil, ErrInvalidExternalIPMapping
			}
		}
		ep := endpoint{ip: extIP, port: extPort}
		mapperList := []*protoMaps{}
		if proto == "udp" {
			mapperList = append(mapperList, &mapper.udp)
		} else if proto == "tcp" {
			mapperList = append(mapperList, &mapper.tcp)
		} else {
			mapperList = append(mapperList, &mapper.udp, &mapper.tcp)
		}
		for _, pm := range mapperList {
			if isV4 {
				if localIP == nil {
					pm.v4Default = &ep
				} else {
					pm.v4[localIP.String()] = ep
				}
			} else {
				if localIP == nil {
					pm.v6Default = &ep
				} else {
					pm.v6[localIP.String()] = ep
				}
			}
		}
	}

	return mapper, nil
}

// findExternalEndpoint returns the external IP and port for a given proto/local.
// If advanced mapping exists it is used; otherwise falls back to legacy per-family mapping
// and returns the input localPort unchanged.
func (m *externalIPMapperAdvanced) findExternalEndpoint(network string, localIP net.IP) (net.IP, int, error) {
	isV4 := localIP.To4() != nil
	pm := m.udp
	if network == "tcp" {
		pm = m.tcp
	}
	if isV4 {
		if ep, ok2 := pm.v4[localIP.String()]; ok2 {
			return ep.ip, ep.port, nil
		}
		if pm.v4Default != nil {
			return pm.v4Default.ip, pm.v4Default.port, nil
		}
	} else {
		if ep, ok2 := pm.v6[localIP.String()]; ok2 {
			return ep.ip, ep.port, nil
		}
		if pm.v6Default != nil {
			return pm.v6Default.ip, pm.v6Default.port, nil
		}
	}
	return nil, 0, ErrExternalMappedIPNotFound
}
