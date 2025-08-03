// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"net/netip"
	"strings"
)

// CandidateHost is a candidate of type host.
type CandidateHost struct {
	candidateBase

	network string
	addresses []string
	ports []int
}

// CandidateHostConfig is the config required to create a new CandidateHost.
type CandidateHostConfig struct {
	CandidateID       string
	Network           string
	Address           string
	Addresses         []string
	Port              int
	Ports             []int
	Component         uint16
	Priority          uint32
	Foundation        string
	TCPType           TCPType
	IsLocationTracked bool
}

// NewCandidateHost creates a new host candidate.
func NewCandidateHost(config *CandidateHostConfig) (*CandidateHost, error) {
	candidateID := config.CandidateID

	if candidateID == "" {
		candidateID = globalCandidateIDGenerator.Generate()
	}

	primaryAddress := config.Address
	primaryPort := config.Port
	if len(config.Addresses) > 0 {
		primaryAddress = config.Addresses[0]
	}
	if len(config.Ports) > 0 {
		primaryPort = config.Ports[0]
	}

	candidateHost := &CandidateHost{
		candidateBase: candidateBase{
			id:                    candidateID,
			address:               primaryAddress,
			candidateType:         CandidateTypeHost,
			component:             config.Component,
			port:                  primaryPort,
			tcpType:               config.TCPType,
			foundationOverride:    config.Foundation,
			priorityOverride:      config.Priority,
			remoteCandidateCaches: map[AddrPort]Candidate{},
			isLocationTracked:     config.IsLocationTracked,
		},
		network: config.Network,
		addresses: config.Addresses,
		ports:     config.Ports,
	}

	if !strings.HasSuffix(primaryAddress, ".local") {
		ipAddr, err := netip.ParseAddr(primaryAddress)
		if err != nil {
			return nil, err
		}

		if err := candidateHost.setIPAddr(ipAddr); err != nil {
			return nil, err
		}
	} else {
		// Until mDNS candidate is resolved assume it is UDPv4
		candidateHost.candidateBase.networkType = NetworkTypeUDP4
	}

	return candidateHost, nil
}

func (c *CandidateHost) setIPAddr(addr netip.Addr) error {
	networkType, err := determineNetworkType(c.network, addr)
	if err != nil {
		return err
	}

	c.candidateBase.networkType = networkType
	c.candidateBase.resolvedAddr = createAddr(networkType, addr, c.port)

	return nil
}
