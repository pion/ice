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
	report := test.CheckRoutines(t)
	defer report()

	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

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
	v, err := buildVNet(natType, natType)
	require.NoError(t, err, "should succeed")
	defer v.close()

	aNotifier, aConnected := onConnected()
	bNotifier, bConnected := onConnected()

	cfg0 := &AgentConfig{
		Urls:             []*stun.URI{stunServerURL},
		NetworkTypes:     supportedNetworkTypes(),
		MulticastDNSMode: MulticastDNSModeDisabled,
		Net:              v.net0,
	}

	aAgent, err := NewAgent(cfg0)
	require.NoError(t, err)
	require.NoError(t, aAgent.OnConnectionStateChange(aNotifier))

	cfg1 := &AgentConfig{
		Urls:             []*stun.URI{},
		Lite:             true,
		CandidateTypes:   []CandidateType{CandidateTypeHost},
		NetworkTypes:     supportedNetworkTypes(),
		MulticastDNSMode: MulticastDNSModeDisabled,
		Net:              v.net1,
	}

	bAgent, err := NewAgent(cfg1)
	require.NoError(t, err)
	require.NoError(t, bAgent.OnConnectionStateChange(bNotifier))

	aConn, bConn := connectWithVNet(aAgent, bAgent)

	// Ensure pair selected
	// Note: this assumes ConnectionStateConnected is thrown after selecting the final pair
	<-aConnected
	<-bConnected

	if !closePipe(t, aConn, bConn) {
		return
	}
}

// Assert that a Lite agent goes to disconnected and failed
func TestLiteLifecycle(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	aNotifier, aConnected := onConnected()

	aAgent, err := NewAgent(&AgentConfig{
		NetworkTypes:     supportedNetworkTypes(),
		MulticastDNSMode: MulticastDNSModeDisabled,
	})
	require.NoError(t, err)
	require.NoError(t, aAgent.OnConnectionStateChange(aNotifier))

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
	require.NoError(t, err)

	bConnected := make(chan interface{})
	bDisconnected := make(chan interface{})
	bFailed := make(chan interface{})

	require.NoError(t, bAgent.OnConnectionStateChange(func(c ConnectionState) {
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

	connectWithVNet(bAgent, aAgent)

	<-aConnected
	<-bConnected
	assert.NoError(t, aAgent.Close())

	<-bDisconnected
	<-bFailed
	assert.NoError(t, bAgent.Close())
}
