// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTCPType(t *testing.T) {
	var tcpType TCPType

	require.Equal(t, TCPTypeUnspecified, tcpType)
	require.Equal(t, TCPTypeActive, NewTCPType("active"))
	require.Equal(t, TCPTypePassive, NewTCPType("passive"))
	require.Equal(t, TCPTypeSimultaneousOpen, NewTCPType("so"))
	require.Equal(t, TCPTypeUnspecified, NewTCPType("something else"))

	require.Equal(t, "", TCPTypeUnspecified.String())
	require.Equal(t, "active", TCPTypeActive.String())
	require.Equal(t, "passive", TCPTypePassive.String())
	require.Equal(t, "so", TCPTypeSimultaneousOpen.String())
	require.Equal(t, "Unknown", TCPType(-1).String())
}
