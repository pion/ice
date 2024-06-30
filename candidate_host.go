// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"net/netip"
	"strings"
)

// CandidateHost is a candidate of type host
type CandidateHost struct {
	candidateBase

	network string
}

// CandidateHostConfig is the config required to create a new CandidateHost
type CandidateHostConfig struct {
	CandidateID       string
	Network           string
	Address           string
	Port              int
	Component         uint16
	Priority          uint32
	Foundation        string
	TCPType           TCPType
	IsLocationTracked bool
}

// NewCandidateHost creates a new host candidate
func NewCandidateHost(config *CandidateHostConfig) (*CandidateHost, error) {
	candidateID := config.CandidateID

	if candidateID == "" {
		candidateID = globalCandidateIDGenerator.Generate()
	}

	c := &CandidateHost{
		candidateBase: candidateBase{
			id:                    candidateID,
			address:               config.Address,
			candidateType:         CandidateTypeHost,
			component:             config.Component,
			port:                  config.Port,
			tcpType:               config.TCPType,
			foundationOverride:    config.Foundation,
			priorityOverride:      config.Priority,
			remoteCandidateCaches: map[AddrPort]Candidate{},
			isLocationTracked:     config.IsLocationTracked,
		},
		network: config.Network,
	}

	if !strings.HasSuffix(c.candidateBase.address, ".local") {
		if addr, zoneID, found := strings.Cut(c.candidateBase.address, "%"); found {
			c.candidateBase.address = addr + "%" + strings.ReplaceAll(zoneID, " ", "%20")
		}

		ipAddr, err := netip.ParseAddr(c.candidateBase.address)
		if err != nil {
			return nil, err
		}

		if err := c.setIPAddr(ipAddr); err != nil {
			return nil, err
		}
	} else {
		// Until mDNS candidate is resolved assume it is UDPv4
		c.candidateBase.networkType = NetworkTypeUDP4
	}

	return c, nil
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
