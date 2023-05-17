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

func TestNoBestAvailableCandidatePairAfterAgentConstruction(t *testing.T) {
	agent, err := NewAgent(&AgentConfig{})
	require.NoError(t, err)

	require.Nil(t, agent.getBestAvailableCandidatePair())

	tearDownTest(t, agent)
}

func tearDownTest(t *testing.T, agent *Agent) {
	require.NoError(t, agent.Close())
}

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
	var err error

	fixture := &TestAgentGetBestValidCandidatePairFixture{
		hostLocal:   newHostLocal(t),
		relayRemote: newRelayRemote(t),
		srflxRemote: newSrflxRemote(t),
		prflxRemote: newPrflxRemote(t),
		hostRemote:  newHostRemote(t),
	}

	fixture.sut, err = NewAgent(&AgentConfig{})
	require.NoError(t, err)

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
