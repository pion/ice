// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"context"
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

	// Limit runtime in case of deadlocks
	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	cfg := &AgentConfig{
		NetworkTypes:     []NetworkType{NetworkTypeUDP4},
		CandidateTypes:   []CandidateType{CandidateTypeHost},
		MulticastDNSMode: MulticastDNSModeQueryAndGather,
	}

	aAgent, err := NewAgent(cfg)
	require.NoError(err)

	aNotifier, aConnected := onConnected()
	err = aAgent.OnConnectionStateChange(aNotifier)
	require.NoError(err)

	bAgent, err := NewAgent(cfg)
	require.NoError(err)

	bNotifier, bConnected := onConnected()
	err = bAgent.OnConnectionStateChange(bNotifier)
	require.NoError(err)

	connect(aAgent, bAgent)
	<-aConnected
	<-bConnected

	assert.NoError(aAgent.Close())
	assert.NoError(bAgent.Close())
}

func TestMulticastDNSMixedConnection(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	defer test.CheckRoutines(t)()

	// Limit runtime in case of deadlocks
	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	aAgent, err := NewAgent(&AgentConfig{
		NetworkTypes:     []NetworkType{NetworkTypeUDP4},
		CandidateTypes:   []CandidateType{CandidateTypeHost},
		MulticastDNSMode: MulticastDNSModeQueryAndGather,
	})
	require.NoError(err)

	aNotifier, aConnected := onConnected()
	err = aAgent.OnConnectionStateChange(aNotifier)
	require.NoError(err)

	bAgent, err := NewAgent(&AgentConfig{
		NetworkTypes:     []NetworkType{NetworkTypeUDP4},
		CandidateTypes:   []CandidateType{CandidateTypeHost},
		MulticastDNSMode: MulticastDNSModeQueryOnly,
	})
	require.NoError(err)

	bNotifier, bConnected := onConnected()
	err = bAgent.OnConnectionStateChange(bNotifier)
	require.NoError(err)

	connect(aAgent, bAgent)
	<-aConnected
	<-bConnected

	assert.NoError(aAgent.Close())
	assert.NoError(bAgent.Close())
}

func TestMulticastDNSStaticHostName(t *testing.T) {
	assert := assert.New(t)

	defer test.CheckRoutines(t)()

	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

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
	assert.NoError(err)

	correctHostName, resolveFunc := context.WithCancel(context.Background())
	assert.NoError(agent.OnCandidate(func(c Candidate) {
		if c != nil && c.Address() == "validName.local" {
			resolveFunc()
		}
	}))

	assert.NoError(agent.GatherCandidates())
	<-correctHostName.Done()
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
