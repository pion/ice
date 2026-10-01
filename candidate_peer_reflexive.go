// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

// Package ice ...
//
//nolint:dupl
package ice

import (
	"net/netip"
)

// CandidatePeerReflexive ...
type CandidatePeerReflexive struct {
	candidateBase
}

// CandidatePeerReflexiveConfig is the config required to create a new CandidatePeerReflexive.
type CandidatePeerReflexiveConfig struct {
	CandidateID string
	Network     string
	Address     string
	Port        int
	Component   uint16
	Priority    uint32
	Foundation  string
	// Extensions contains candidate attributes.
	Extensions []CandidateExtension
}

// NewCandidatePeerReflexive creates a new peer reflective candidate.
func NewCandidatePeerReflexive(config *CandidatePeerReflexiveConfig) (*CandidatePeerReflexive, error) {
	ipAddr, err := netip.ParseAddr(config.Address)
	if err != nil {
		return nil, err
	}

	networkType, err := determineNetworkType(config.Network, ipAddr)
	if err != nil {
		return nil, err
	}

	candidateID := config.CandidateID
	if candidateID == "" {
		candidateID = generateCandidateID()
	}

	candidate := &CandidatePeerReflexive{
		candidateBase: candidateBase{
			id:                 candidateID,
			networkType:        networkType,
			candidateType:      CandidateTypePeerReflexive,
			address:            config.Address,
			port:               config.Port,
			component:          config.Component,
			foundationOverride: config.Foundation,
			priorityOverride:   config.Priority,
		},
	}
	if err := candidate.SetExtensions(config.Extensions); err != nil {
		return nil, err
	}
	candidate.setResolvedAddr(createAddr(networkType, ipAddr, config.Port))

	return candidate, nil
}
