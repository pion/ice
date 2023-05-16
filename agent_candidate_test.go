// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/transport/v2/test"
	"github.com/pion/transport/v2/vnet"
	"github.com/stretchr/testify/assert"
)

func TestNilCandidate(t *testing.T) {
	a, err := NewAgent(&AgentConfig{})
	assert.NoError(t, err)

	assert.NoError(t, a.AddRemoteCandidate(nil))
	assert.NoError(t, a.Close())
}

func TestNilCandidatePair(t *testing.T) {
	a, err := NewAgent(&AgentConfig{})
	assert.NoError(t, err)

	a.setSelectedPair(nil)
	assert.NoError(t, a.Close())
}

func TestGetSelectedCandidatePair(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	wan, err := vnet.NewRouter(&vnet.RouterConfig{
		CIDR:          "0.0.0.0/0",
		LoggerFactory: logging.NewDefaultLoggerFactory(),
	})
	assert.NoError(t, err)

	net, err := vnet.NewNet(&vnet.NetConfig{
		StaticIPs: []string{"192.168.0.1"},
	})
	assert.NoError(t, err)
	assert.NoError(t, wan.AddNet(net))

	assert.NoError(t, wan.Start())

	cfg := &AgentConfig{
		NetworkTypes: supportedNetworkTypes(),
		Net:          net,
	}

	aAgent, err := NewAgent(cfg)
	assert.NoError(t, err)

	bAgent, err := NewAgent(cfg)
	assert.NoError(t, err)

	aAgentPair, err := aAgent.GetSelectedCandidatePair()
	assert.NoError(t, err)
	assert.Nil(t, aAgentPair)

	bAgentPair, err := bAgent.GetSelectedCandidatePair()
	assert.NoError(t, err)
	assert.Nil(t, bAgentPair)

	connect(aAgent, bAgent)

	aAgentPair, err = aAgent.GetSelectedCandidatePair()
	assert.NoError(t, err)
	assert.NotNil(t, aAgentPair)

	bAgentPair, err = bAgent.GetSelectedCandidatePair()
	assert.NoError(t, err)
	assert.NotNil(t, bAgentPair)

	assert.True(t, bAgentPair.Local.Equal(aAgentPair.Remote))
	assert.True(t, bAgentPair.Remote.Equal(aAgentPair.Local))

	assert.NoError(t, wan.Stop())
	assert.NoError(t, aAgent.Close())
	assert.NoError(t, bAgent.Close())
}
