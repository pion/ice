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

	"github.com/pion/stun"
	"github.com/pion/transport/v2/test"
	"github.com/pion/transport/v2/vnet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRemoteLocalAddr(t *testing.T) {
	require := require.New(t)

	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 20).Stop()

	// Agent0 is behind 1:1 NAT
	natType0 := &vnet.NATType{Mode: vnet.NATModeNAT1To1}
	// Agent1 is behind 1:1 NAT
	natType1 := &vnet.NATType{Mode: vnet.NATModeNAT1To1}

	v, errVnet := newVirtualNet(natType0, natType1)
	if !assert.NoError(t, errVnet) {
		return
	}

	defer func() {
		require.NoError(v.Close())
	}()

	stunServerURL := &stun.URI{
		Scheme: stun.SchemeTypeSTUN,
		Host:   vnetSTUNServerIP,
		Port:   vnetSTUNServerPort,
		Proto:  stun.ProtoTypeUDP,
	}

	t.Run("Disconnected Returns nil", func(t *testing.T) {
		assert := assert.New(t)

		disconnectedAgent, err := NewAgent(&AgentConfig{})
		require.NoError(err)

		disconnectedConn := Conn{agent: disconnectedAgent}
		assert.Nil(disconnectedConn.RemoteAddr())
		assert.Nil(disconnectedConn.LocalAddr())

		assert.NoError(disconnectedConn.Close())
	})

	t.Run("Remote/Local Pair Match between Agents", func(t *testing.T) {
		assert := assert.New(t)

		aConn, bConn := pipeWithVirtualNet(t, v,
			&agentTestConfig{
				urls: []*stun.URI{stunServerURL},
			},
			&agentTestConfig{
				urls: []*stun.URI{stunServerURL},
			},
		)

		aRemoteAddr := aConn.RemoteAddr()
		aLocalAddr := aConn.LocalAddr()
		bRemoteAddr := bConn.RemoteAddr()
		bLocalAddr := bConn.LocalAddr()

		// Assert that nothing is nil
		assert.NotNil(aRemoteAddr)
		assert.NotNil(aLocalAddr)
		assert.NotNil(bRemoteAddr)
		assert.NotNil(bLocalAddr)

		// Assert addresses
		assert.Equal(aLocalAddr.String(),
			fmt.Sprintf("%s:%d", vnetLocalIPA, bRemoteAddr.(*net.UDPAddr).Port), //nolint:forcetypeassert
		)
		assert.Equal(bLocalAddr.String(),
			fmt.Sprintf("%s:%d", vnetLocalIPB, aRemoteAddr.(*net.UDPAddr).Port), //nolint:forcetypeassert
		)
		assert.Equal(aRemoteAddr.String(),
			fmt.Sprintf("%s:%d", vnetGlobalIPB, bLocalAddr.(*net.UDPAddr).Port), //nolint:forcetypeassert
		)
		assert.Equal(bRemoteAddr.String(),
			fmt.Sprintf("%s:%d", vnetGlobalIPA, aLocalAddr.(*net.UDPAddr).Port), //nolint:forcetypeassert
		)

		// Close
		assert.NoError(aConn.Close())
		assert.NoError(bConn.Close())
	})
}
