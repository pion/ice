// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"fmt"
	"testing"
	"time"

	"github.com/pion/stun"
	"github.com/pion/transport/v2/test"
	"github.com/pion/transport/v2/vnet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConnectivityLite(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 30).Stop()

	stunServerURL := &stun.URI{
		Scheme: SchemeTypeSTUN,
		Host:   "1.2.3.4",
		Port:   3478,
		Proto:  stun.ProtoTypeUDP,
	}

	natType := &vnet.NATType{
		MappingBehavior:   vnet.EndpointIndependent,
		FilteringBehavior: vnet.EndpointIndependent,
	}
	v, err := newVirtualNet(natType, natType)
	require.NoError(err)

	defer func() {
		require.NoError(v.Close())
	}()

	aNotifier, aConnected := onConnected()
	bNotifier, bConnected := onConnected()

	cfg0 := &AgentConfig{
		Urls:             []*stun.URI{stunServerURL},
		NetworkTypes:     supportedNetworkTypes(),
		MulticastDNSMode: MulticastDNSModeDisabled,
		Net:              v.net0,
	}

	aAgent, err := NewAgent(cfg0)
	require.NoError(err)
	require.NoError(aAgent.OnConnectionStateChange(aNotifier))

	cfg1 := &AgentConfig{
		Urls:             []*stun.URI{},
		Lite:             true,
		CandidateTypes:   []CandidateType{CandidateTypeHost},
		NetworkTypes:     supportedNetworkTypes(),
		MulticastDNSMode: MulticastDNSModeDisabled,
		Net:              v.net1,
	}

	bAgent, err := NewAgent(cfg1)
	require.NoError(err)
	require.NoError(bAgent.OnConnectionStateChange(bNotifier))

	aConn, bConn := connectWithVirtualNet(aAgent, bAgent)

	// Ensure pair selected
	// Note: this assumes ConnectionStateConnected is thrown after selecting the final pair
	<-aConnected
	<-bConnected

	assert.NoError(aConn.Close())
	assert.NoError(bConn.Close())
}

// Assert that a Lite agent goes to disconnected and failed
func TestLiteLifecycle(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 30).Stop()

	aNotifier, aConnected := onConnected()

	aAgent, err := NewAgent(&AgentConfig{
		NetworkTypes:     supportedNetworkTypes(),
		MulticastDNSMode: MulticastDNSModeDisabled,
	})
	require.NoError(err)
	require.NoError(aAgent.OnConnectionStateChange(aNotifier))

	disconnectedDuration := time.Second
	failedDuration := time.Second
	KeepaliveInterval := time.Duration(0)
	CheckInterval := 500 * time.Millisecond
	bAgent, err := NewAgent(&AgentConfig{
		Lite:                true,
		CandidateTypes:      []CandidateType{CandidateTypeHost},
		NetworkTypes:        supportedNetworkTypes(),
		MulticastDNSMode:    MulticastDNSModeDisabled,
		DisconnectedTimeout: &disconnectedDuration,
		FailedTimeout:       &failedDuration,
		KeepaliveInterval:   &KeepaliveInterval,
		CheckInterval:       &CheckInterval,
	})
	require.NoError(err)

	bConnected := make(chan interface{})
	bDisconnected := make(chan interface{})
	bFailed := make(chan interface{})

	require.NoError(bAgent.OnConnectionStateChange(func(c ConnectionState) {
		fmt.Println(c)
		switch c {
		case ConnectionStateConnected:
			close(bConnected)
		case ConnectionStateDisconnected:
			close(bDisconnected)
		case ConnectionStateFailed:
			close(bFailed)
		default:
		}
	}))

	connectWithVirtualNet(bAgent, aAgent)

	<-aConnected
	<-bConnected
	assert.NoError(aAgent.Close())

	<-bDisconnected
	<-bFailed
	assert.NoError(bAgent.Close())
}
