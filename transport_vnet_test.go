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
)

func TestRemoteLocalAddr(t *testing.T) {
	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 20).Stop()

	// Agent0 is behind 1:1 NAT
	natType0 := &vnet.NATType{Mode: vnet.NATModeNAT1To1}
	// Agent1 is behind 1:1 NAT
	natType1 := &vnet.NATType{Mode: vnet.NATModeNAT1To1}

	v, errVnet := buildVNet(natType0, natType1)
	if !assert.NoError(t, errVnet) {
		return
	}
	defer v.close()

	stunServerURL := &stun.URI{
		Scheme: stun.SchemeTypeSTUN,
		Host:   vnetSTUNServerIP,
		Port:   vnetSTUNServerPort,
		Proto:  stun.ProtoTypeUDP,
	}

	t.Run("Disconnected Returns nil", func(t *testing.T) {
		assert := assert.New(t)

		disconnectedAgent, err := NewAgent(&AgentConfig{})
		assert.NoError(err)

		disconnectedConn := Conn{agent: disconnectedAgent}
		assert.Nil(disconnectedConn.RemoteAddr())
		assert.Nil(disconnectedConn.LocalAddr())

		assert.NoError(disconnectedConn.Close())
	})

	t.Run("Remote/Local Pair Match between Agents", func(t *testing.T) {
		assert := assert.New(t)

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
		assert.NotNil(aRAddr)
		assert.NotNil(aLAddr)
		assert.NotNil(bRAddr)
		assert.NotNil(bLAddr)

		// Assert addresses
		assert.Equal(aLAddr.String(),
			fmt.Sprintf("%s:%d", vnetLocalIPA, bRAddr.(*net.UDPAddr).Port), //nolint:forcetypeassert
		)
		assert.Equal(bLAddr.String(),
			fmt.Sprintf("%s:%d", vnetLocalIPB, aRAddr.(*net.UDPAddr).Port), //nolint:forcetypeassert
		)
		assert.Equal(aRAddr.String(),
			fmt.Sprintf("%s:%d", vnetGlobalIPB, bLAddr.(*net.UDPAddr).Port), //nolint:forcetypeassert
		)
		assert.Equal(bRAddr.String(),
			fmt.Sprintf("%s:%d", vnetGlobalIPA, aLAddr.(*net.UDPAddr).Port), //nolint:forcetypeassert
		)

		// Close
		assert.NoError(ca.Close())
		assert.NoError(cb.Close())
	})
}
