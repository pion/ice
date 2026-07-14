// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package ice

import (
	"testing"
	"time"

	"github.com/pion/transport/v4/test"
	"github.com/stretchr/testify/require"
)

// TestAgentRestartThenGatherRepeatedly guards a gathering-state race where
// restarting mid-gather leaves gatheringState wedged, so subsequent
// GatherCandidates calls fail with ErrMultipleGatherAttempted.
func TestAgentRestartThenGatherRepeatedly(t *testing.T) {
	defer test.CheckRoutines(t)()
	defer test.TimeOut(30 * time.Second).Stop()

	agent, err := NewAgent(&AgentConfig{})
	require.NoError(t, err)
	defer func() { require.NoError(t, agent.Close()) }()

	require.NoError(t, agent.OnCandidate(func(Candidate) {}))
	require.NoError(t, agent.GatherCandidates())

	// Restart immediately, before the previous cycle has settled, many times.
	const restarts = 300
	for range restarts {
		require.NoError(t, agent.Restart("", ""))
		require.NoError(t, agent.GatherCandidates())
	}
}
