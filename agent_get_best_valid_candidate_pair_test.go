// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentGetBestValidCandidatePair(t *testing.T) {
	f := setupTestAgentGetBestValidCandidatePair(t)

	remoteCandidatesFromLowestPriorityToHighest := []Candidate{f.relayRemote, f.srflxRemote, f.prflxRemote, f.hostRemote}

	for _, remoteCandidate := range remoteCandidatesFromLowestPriorityToHighest {
		candidatePair := f.sut.addPair(f.hostLocal, remoteCandidate)
		candidatePair.state = CandidatePairStateSucceeded

		actualBestPair := f.sut.getBestValidCandidatePair()
		expectedBestPair := &CandidatePair{Remote: remoteCandidate, Local: f.hostLocal}

		require.Equal(t, actualBestPair.String(), expectedBestPair.String())
	}

	assert.NoError(t, f.sut.Close())
}

func setupTestAgentGetBestValidCandidatePair(t *testing.T) *TestAgentGetBestValidCandidatePairFixture {
	fixture := new(TestAgentGetBestValidCandidatePairFixture)
	fixture.hostLocal = newHostLocal(t)
	fixture.relayRemote = newRelayRemote(t)
	fixture.srflxRemote = newSrflxRemote(t)
	fixture.prflxRemote = newPrflxRemote(t)
	fixture.hostRemote = newHostRemote(t)

	agent, err := NewAgent(&AgentConfig{})
	require.NoError(t, err)
	fixture.sut = agent

	return fixture
}

type TestAgentGetBestValidCandidatePairFixture struct {
	sut *Agent

	hostLocal   Candidate
	relayRemote Candidate
	srflxRemote Candidate
	prflxRemote Candidate
	hostRemote  Candidate
}

func newHostRemote(t *testing.T) *CandidateHost {
	remoteHostConfig := &CandidateHostConfig{
		Network:   "udp",
		Address:   "1.2.3.5",
		Port:      12350,
		Component: 1,
	}
	hostRemote, err := NewCandidateHost(remoteHostConfig)
	require.NoError(t, err)
	return hostRemote
}

func newPrflxRemote(t *testing.T) *CandidatePeerReflexive {
	prflxConfig := &CandidatePeerReflexiveConfig{
		Network:   "udp",
		Address:   "10.10.10.2",
		Port:      19217,
		Component: 1,
		RelAddr:   "4.3.2.1",
		RelPort:   43211,
	}
	prflxRemote, err := NewCandidatePeerReflexive(prflxConfig)
	require.NoError(t, err)
	return prflxRemote
}

func newSrflxRemote(t *testing.T) *CandidateServerReflexive {
	srflxConfig := &CandidateServerReflexiveConfig{
		Network:   "udp",
		Address:   "10.10.10.2",
		Port:      19218,
		Component: 1,
		RelAddr:   "4.3.2.1",
		RelPort:   43212,
	}
	srflxRemote, err := NewCandidateServerReflexive(srflxConfig)
	require.NoError(t, err)
	return srflxRemote
}

func newRelayRemote(t *testing.T) *CandidateRelay {
	relayConfig := &CandidateRelayConfig{
		Network:   "udp",
		Address:   "1.2.3.4",
		Port:      12340,
		Component: 1,
		RelAddr:   "4.3.2.1",
		RelPort:   43210,
	}
	relayRemote, err := NewCandidateRelay(relayConfig)
	require.NoError(t, err)
	return relayRemote
}

func newHostLocal(t *testing.T) *CandidateHost {
	localHostConfig := &CandidateHostConfig{
		Network:   "udp",
		Address:   "192.168.1.1",
		Port:      19216,
		Component: 1,
	}
	hostLocal, err := NewCandidateHost(localHostConfig)
	require.NoError(t, err)
	return hostLocal
}
