// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOnSelectedCandidatePairChange(t *testing.T) {
	agent, candidatePair := fixtureTestOnSelectedCandidatePairChange(t)

	callbackCalled := make(chan struct{}, 1)
	err := agent.OnSelectedCandidatePairChange(func(_, _ Candidate) {
		close(callbackCalled)
	})
	require.NoError(t, err)

	err = agent.loop.Run(func(_ context.Context) {
		agent.setSelectedPair(candidatePair) // nolint: contextcheck
	})
	require.NoError(t, err)

	<-callbackCalled
	require.NoError(t, agent.Close())
}

func fixtureTestOnSelectedCandidatePairChange(t *testing.T) (*Agent, *CandidatePair) {
	agent, err := NewAgent(&AgentConfig{})
	require.NoError(t, err)

	candidatePair := makeCandidatePair(t)
	return agent, candidatePair
}

func makeCandidatePair(t *testing.T) *CandidatePair {
	hostLocal := newHostLocal(t)
	relayRemote := newRelayRemote(t)

	candidatePair := newCandidatePair(hostLocal, relayRemote, false)
	return candidatePair
}
