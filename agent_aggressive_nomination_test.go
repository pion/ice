// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/stun"
	"github.com/pion/transport/v2/test"
	"github.com/pion/transport/v2/vnet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAcceptAggressiveNomination(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	// Create a network with two interfaces
	wan, err := vnet.NewRouter(&vnet.RouterConfig{
		CIDR:          "0.0.0.0/0",
		LoggerFactory: logging.NewDefaultLoggerFactory(),
	})
	assert.NoError(t, err)

	net0, err := vnet.NewNet(&vnet.NetConfig{
		StaticIPs: []string{"192.168.0.1"},
	})
	assert.NoError(t, err)
	assert.NoError(t, wan.AddNet(net0))

	net1, err := vnet.NewNet(&vnet.NetConfig{
		StaticIPs: []string{"192.168.0.2", "192.168.0.3", "192.168.0.4"},
	})
	assert.NoError(t, err)
	assert.NoError(t, wan.AddNet(net1))

	assert.NoError(t, wan.Start())

	aNotifier, aConnected := onConnected()
	bNotifier, bConnected := onConnected()

	KeepaliveInterval := time.Hour
	cfg0 := &AgentConfig{
		NetworkTypes:     []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
		MulticastDNSMode: MulticastDNSModeDisabled,
		Net:              net0,

		KeepaliveInterval:          &KeepaliveInterval,
		CheckInterval:              &KeepaliveInterval,
		AcceptAggressiveNomination: true,
	}

	var aAgent, bAgent *Agent
	aAgent, err = NewAgent(cfg0)
	require.NoError(t, err)
	require.NoError(t, aAgent.OnConnectionStateChange(aNotifier))

	cfg1 := &AgentConfig{
		NetworkTypes:      []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
		MulticastDNSMode:  MulticastDNSModeDisabled,
		Net:               net1,
		KeepaliveInterval: &KeepaliveInterval,
		CheckInterval:     &KeepaliveInterval,
	}

	bAgent, err = NewAgent(cfg1)
	require.NoError(t, err)
	require.NoError(t, bAgent.OnConnectionStateChange(bNotifier))

	aConn, bConn := connect(aAgent, bAgent)

	// Ensure pair selected
	// Note: this assumes ConnectionStateConnected is thrown after selecting the final pair
	<-aConnected
	<-bConnected

	// Send new USE-CANDIDATE message with higher priority to update the selected pair
	buildMsg := func(class stun.MessageClass, username, key string, priority uint32) *stun.Message {
		msg, err1 := stun.Build(stun.NewType(stun.MethodBinding, class), stun.TransactionID,
			stun.NewUsername(username),
			stun.NewShortTermIntegrity(key),
			UseCandidate(),
			PriorityAttr(priority),
			stun.Fingerprint,
		)
		if err1 != nil {
			t.Fatal(err1)
		}

		return msg
	}

	selectedCh := make(chan Candidate, 1)
	var expectNewSelectedCandidate Candidate
	err = aAgent.OnSelectedCandidatePairChange(func(_, remote Candidate) {
		selectedCh <- remote
	})
	require.NoError(t, err)
	var bcandidates []Candidate
	bcandidates, err = bAgent.GetLocalCandidates()
	require.NoError(t, err)

	for _, c := range bcandidates {
		if c != bAgent.getSelectedPair().Local {
			if expectNewSelectedCandidate == nil {
			incr_priority:
				for _, candidates := range aAgent.remoteCandidates {
					for _, candidate := range candidates {
						if candidate.Equal(c) {
							candidate.(*CandidateHost).priorityOverride += 1000 //nolint:forcetypeassert
							break incr_priority
						}
					}
				}
				expectNewSelectedCandidate = c
			}
			_, err = c.writeTo(buildMsg(stun.ClassRequest, aAgent.localUfrag+":"+aAgent.remoteUfrag, aAgent.localPwd, c.Priority()).Raw, bAgent.getSelectedPair().Remote)
			require.NoError(t, err)
		}
	}

	time.Sleep(1 * time.Second)
	select {
	case selected := <-selectedCh:
		assert.True(t, selected.Equal(expectNewSelectedCandidate))
	default:
		t.Fatal("No selected candidate pair")
	}

	assert.NoError(t, wan.Stop())
	if !closePipe(t, aConn, bConn) {
		return
	}
}
