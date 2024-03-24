// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/pion/stun/v2"
	"github.com/pion/transport/v3/test"
	"github.com/pion/transport/v3/vnet"
	"github.com/stretchr/testify/require"
)

func TestRemoteLocalAddr(t *testing.T) {
	// Check for leaking routines
	defer test.CheckRoutines(t)()

	// Limit runtime in case of deadlocks
	lim := test.TimeOut(time.Second * 20)
	defer lim.Stop()

	// Agent0 is behind 1:1 NAT
	natType0 := &vnet.NATType{Mode: vnet.NATModeNAT1To1}
	// Agent1 is behind 1:1 NAT
	natType1 := &vnet.NATType{Mode: vnet.NATModeNAT1To1}

	v, errVnet := buildVNet(natType0, natType1)
	require.NoError(t, errVnet, "should succeed")
	defer v.close()

	stunServerURL := &stun.URI{
		Scheme: stun.SchemeTypeSTUN,
		Host:   vnetSTUNServerIP,
		Port:   vnetSTUNServerPort,
		Proto:  stun.ProtoTypeUDP,
	}

	t.Run("Disconnected Returns nil", func(t *testing.T) {
		disconnectedAgent, err := NewAgent(&AgentConfig{})
		require.NoError(t, err)

		disconnectedConn := Conn{agent: disconnectedAgent}
		require.Nil(t, disconnectedConn.RemoteAddr())
		require.Nil(t, disconnectedConn.LocalAddr())

		require.NoError(t, disconnectedConn.Close())
	})

	t.Run("Remote/Local Pair Match between Agents", func(t *testing.T) {
		ca, cb := pipeWithVNet(v,
			&agentTestConfig{
				urls: []*stun.URI{stunServerURL},
			},
			&agentTestConfig{
				urls: []*stun.URI{stunServerURL},
			},
		)

		aRAddr := ca.RemoteAddr()
		aLAddr := ca.LocalAddr()
		bRAddr := cb.RemoteAddr()
		bLAddr := cb.LocalAddr()

		// Assert that nothing is nil
		require.NotNil(t, aRAddr)
		require.NotNil(t, aLAddr)
		require.NotNil(t, bRAddr)
		require.NotNil(t, bLAddr)

		// Assert addresses
		require.Equal(t, aLAddr.String(),
			fmt.Sprintf("%s:%d", vnetLocalIPA, bRAddr.(*net.UDPAddr).Port), //nolint:forcetypeassert
		)
		require.Equal(t, bLAddr.String(),
			fmt.Sprintf("%s:%d", vnetLocalIPB, aRAddr.(*net.UDPAddr).Port), //nolint:forcetypeassert
		)
		require.Equal(t, aRAddr.String(),
			fmt.Sprintf("%s:%d", vnetGlobalIPB, bLAddr.(*net.UDPAddr).Port), //nolint:forcetypeassert
		)
		require.Equal(t, bRAddr.String(),
			fmt.Sprintf("%s:%d", vnetGlobalIPA, aLAddr.(*net.UDPAddr).Port), //nolint:forcetypeassert
		)

		// Close
		require.NoError(t, ca.Close())
		require.NoError(t, cb.Close())
	})
}
