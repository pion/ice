// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"regexp"
	"testing"
	"time"

	"github.com/pion/transport/v2/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMulticastDNSOnlyConnection(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 30).Stop()

	cfg := &AgentConfig{
		NetworkTypes:     []NetworkType{NetworkTypeUDP4},
		CandidateTypes:   []CandidateType{CandidateTypeHost},
		MulticastDNSMode: MulticastDNSModeQueryAndGather,
	}

	aAgent, err := NewAgent(cfg)
	require.NoError(err)

	aNotifier, aConnected := onConnectionStateChangedNotifier(ConnectionStateConnected)
	require.NoError(aAgent.OnConnectionStateChange(aNotifier))

	bAgent, err := NewAgent(cfg)
	require.NoError(err)

	bNotifier, bConnected := onConnectionStateChangedNotifier(ConnectionStateConnected)
	require.NoError(bAgent.OnConnectionStateChange(bNotifier))

	connect(t, aAgent, bAgent)
	<-aConnected
	<-bConnected

	assert.NoError(aAgent.Close())
	assert.NoError(bAgent.Close())
}

func TestMulticastDNSMixedConnection(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 30).Stop()

	aAgent, err := NewAgent(&AgentConfig{
		NetworkTypes:     []NetworkType{NetworkTypeUDP4},
		CandidateTypes:   []CandidateType{CandidateTypeHost},
		MulticastDNSMode: MulticastDNSModeQueryAndGather,
	})
	require.NoError(err)

	aNotifier, aConnected := onConnectionStateChangedNotifier(ConnectionStateConnected)
	require.NoError(aAgent.OnConnectionStateChange(aNotifier))

	bAgent, err := NewAgent(&AgentConfig{
		NetworkTypes:     []NetworkType{NetworkTypeUDP4},
		CandidateTypes:   []CandidateType{CandidateTypeHost},
		MulticastDNSMode: MulticastDNSModeQueryOnly,
	})
	require.NoError(err)

	bNotifier, bConnected := onConnectionStateChangedNotifier(ConnectionStateConnected)
	require.NoError(bAgent.OnConnectionStateChange(bNotifier))

	connect(t, aAgent, bAgent)
	<-aConnected
	<-bConnected

	assert.NoError(aAgent.Close())
	assert.NoError(bAgent.Close())
}

func TestMulticastDNSStaticHostName(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 30).Stop()

	_, err := NewAgent(&AgentConfig{
		NetworkTypes:         []NetworkType{NetworkTypeUDP4},
		CandidateTypes:       []CandidateType{CandidateTypeHost},
		MulticastDNSMode:     MulticastDNSModeQueryAndGather,
		MulticastDNSHostName: "invalidHostName",
	})
	assert.Equal(err, ErrInvalidMulticastDNSHostName)

	agent, err := NewAgent(&AgentConfig{
		NetworkTypes:         []NetworkType{NetworkTypeUDP4},
		CandidateTypes:       []CandidateType{CandidateTypeHost},
		MulticastDNSMode:     MulticastDNSModeQueryAndGather,
		MulticastDNSHostName: "validName.local",
	})
	require.NoError(err)

	candidateNotifier, candidates := onCandidateNotifier()
	require.NoError(agent.OnCandidate(candidateNotifier))

	assert.NoError(agent.GatherCandidates())

	var foundMDNSCandidates int
	for c := range candidates {
		if c.Address() == "validName.local" {
			foundMDNSCandidates++
		}
	}

	assert.True(foundMDNSCandidates > 0)

	assert.NoError(agent.Close())
}

func TestGenerateMulticastDNSName(t *testing.T) {
	require := require.New(t)

	name, err := generateMulticastDNSName()
	require.NoError(err)

	isMDNSName := regexp.MustCompile(
		`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-4[0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}.local+$`,
	).MatchString

	require.True(isMDNSName(name), "mDNS name must be UUID v4 + \".local\" suffix")
}
