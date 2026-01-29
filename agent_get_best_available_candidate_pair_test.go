// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNoBestAvailableCandidatePairAfterAgentConstruction(t *testing.T) {
	agent, err := NewAgent(&AgentConfig{})
	require.NoError(t, err)

	defer func() {
		require.NoError(t, agent.Close())
	}()

	require.Nil(t, agent.getBestAvailableCandidatePair())
}
