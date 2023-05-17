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

// Send new USE-CANDIDATE message with higher priority to update the selected pair
func buildUseCandidateMsg(class stun.MessageClass, username, key string, priority uint32) (*stun.Message, error) {
	return stun.Build(stun.NewType(stun.MethodBinding, class), stun.TransactionID,
		stun.NewUsername(username),
		stun.NewShortTermIntegrity(key),
		UseCandidate(),
		PriorityAttr(priority),
		stun.Fingerprint,
	)
}

func TestAcceptAggressiveNomination(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 30).Stop()

	// Create a network with two interfaces
	wan, err := vnet.NewRouter(&vnet.RouterConfig{
		CIDR:          "0.0.0.0/0",
		LoggerFactory: logging.NewDefaultLoggerFactory(),
	})
	require.NoError(err)

	net0, err := vnet.NewNet(&vnet.NetConfig{
		StaticIPs: []string{"192.168.0.1"},
	})
	require.NoError(err)

	require.NoError(wan.AddNet(net0))

	net1, err := vnet.NewNet(&vnet.NetConfig{
		StaticIPs: []string{"192.168.0.2", "192.168.0.3", "192.168.0.4"},
	})
	require.NoError(err)

	require.NoError(wan.AddNet(net1))
	require.NoError(wan.Start())

	KeepaliveInterval := time.Hour
	cfg0 := &AgentConfig{
		NetworkTypes:     []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
		MulticastDNSMode: MulticastDNSModeDisabled,
		Net:              net0,

		KeepaliveInterval:          &KeepaliveInterval,
		CheckInterval:              &KeepaliveInterval,
		AcceptAggressiveNomination: true,
	}

	aAgent, err := NewAgent(cfg0)
	require.NoError(err)

	aNotifier, aConnected := onConnectionStateChangedNotifier(ConnectionStateConnected)
	require.NoError(aAgent.OnConnectionStateChange(aNotifier))

	cfg1 := &AgentConfig{
		NetworkTypes:      []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
		MulticastDNSMode:  MulticastDNSModeDisabled,
		Net:               net1,
		KeepaliveInterval: &KeepaliveInterval,
		CheckInterval:     &KeepaliveInterval,
	}

	bAgent, err := NewAgent(cfg1)
	require.NoError(err)

	bNotifier, bConnected := onConnectionStateChangedNotifier(ConnectionStateConnected)
	require.NoError(bAgent.OnConnectionStateChange(bNotifier))

	aConn, bConn := connect(t, aAgent, bAgent)

	// Ensure pair selected
	// Note: this assumes ConnectionStateConnected is thrown after selecting the final pair
	<-aConnected
	<-bConnected

	selectedNotifier, selectedCh := onCandidatePairSelectedNotifier(true)
	require.NoError(aAgent.OnSelectedCandidatePairChange(selectedNotifier))

	localCandidates, err := bAgent.GetLocalCandidates()
	require.NoError(err)

	cp := bAgent.getSelectedPair()

	var expectNewSelectedCandidate, newSelectedCandidate Candidate
	for _, localCandidate := range localCandidates {
		if localCandidate == cp.Local {
			continue
		}

		if expectNewSelectedCandidate == nil {
		incr_priority:
			for _, remoteCandidates := range aAgent.remoteCandidates {
				for _, remoteCandidate := range remoteCandidates {
					if remoteHostCandidate, ok := remoteCandidate.(*CandidateHost); ok && remoteCandidate.Equal(localCandidate) {
						remoteHostCandidate.priorityOverride += 1000
						break incr_priority
					}
				}
			}

			expectNewSelectedCandidate = localCandidate
		}

		msg, err := buildUseCandidateMsg(stun.ClassRequest, aAgent.localUfrag+":"+aAgent.remoteUfrag, aAgent.localPwd, localCandidate.Priority())
		require.NoError(err)

		_, err = localCandidate.writeTo(msg.Raw, cp.Remote)
		require.NoError(err)
	}

	t.Log("Wait for new selected candidate")

	timeout := time.NewTimer(2 * time.Second)
	select {
	case newSelectedCandidate = <-selectedCh:
	case <-timeout.C:
		assert.Fail("Timeout")
	}

	assert.NotNil(expectNewSelectedCandidate)
	assert.NotNil(newSelectedCandidate)

	assert.Truef(newSelectedCandidate.Equal(expectNewSelectedCandidate),
		"Candidate mismatch: %s != %s",
		expectNewSelectedCandidate, newSelectedCandidate)

	assert.NoError(wan.Stop())
	assert.NoError(aConn.Close())
	assert.NoError(bConn.Close())
}
