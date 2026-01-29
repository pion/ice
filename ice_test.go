// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConnectedState_String(t *testing.T) {
	testCases := []struct {
		connectionState ConnectionState
		expectedString  string
	}{
		{ConnectionStateUnknown, "Invalid"},
		{ConnectionStateNew, "New"},
		{ConnectionStateChecking, "Checking"},
		{ConnectionStateConnected, "Connected"},
		{ConnectionStateCompleted, "Completed"},
		{ConnectionStateFailed, "Failed"},
		{ConnectionStateDisconnected, "Disconnected"},
		{ConnectionStateClosed, "Closed"},
	}

	for i, testCase := range testCases {
		require.Equal(t,
			testCase.expectedString,
			testCase.connectionState.String(),
			"testCase: %d %v", i, testCase,
		)
	}
}

func TestGatheringState_String(t *testing.T) {
	testCases := []struct {
		gatheringState GatheringState
		expectedString string
	}{
		{GatheringStateUnknown, ErrUnknownType.Error()},
		{GatheringStateNew, "new"},
		{GatheringStateGathering, "gathering"},
		{GatheringStateComplete, "complete"},
	}

	for i, testCase := range testCases {
		require.Equal(t,
			testCase.expectedString,
			testCase.gatheringState.String(),
			"testCase: %d %v", i, testCase,
		)
	}
}
