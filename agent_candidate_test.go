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
	assert := assert.New(t)

	a, err := NewAgent(&AgentConfig{})
	assert.NoError(err)

	assert.NoError(a.AddRemoteCandidate(nil))
	assert.NoError(a.Close())
}

func TestNilCandidatePair(t *testing.T) {
	assert := assert.New(t)

	a, err := NewAgent(&AgentConfig{})
	assert.NoError(err)

	a.setSelectedPair(nil)
	assert.NoError(a.Close())
}

func TestGetSelectedCandidatePair(t *testing.T) {
	assert := assert.New(t)
	report := test.CheckRoutines(t)
	defer report()

	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	wan, err := vnet.NewRouter(&vnet.RouterConfig{
		CIDR:          "0.0.0.0/0",
		LoggerFactory: logging.NewDefaultLoggerFactory(),
	})
	assert.NoError(err)

	net, err := vnet.NewNet(&vnet.NetConfig{
		StaticIPs: []string{"192.168.0.1"},
	})
	assert.NoError(err)
	assert.NoError(wan.AddNet(net))

	assert.NoError(wan.Start())

	cfg := &AgentConfig{
		NetworkTypes: supportedNetworkTypes(),
		Net:          net,
	}

	aAgent, err := NewAgent(cfg)
	assert.NoError(err)

	bAgent, err := NewAgent(cfg)
	assert.NoError(err)

	aAgentPair, err := aAgent.GetSelectedCandidatePair()
	assert.NoError(err)
	assert.Nil(aAgentPair)

	bAgentPair, err := bAgent.GetSelectedCandidatePair()
	assert.NoError(err)
	assert.Nil(bAgentPair)

	connect(aAgent, bAgent)

	aAgentPair, err = aAgent.GetSelectedCandidatePair()
	assert.NoError(err)
	assert.NotNil(aAgentPair)

	bAgentPair, err = bAgent.GetSelectedCandidatePair()
	assert.NoError(err)
	assert.NotNil(bAgentPair)

	assert.True(bAgentPair.Local.Equal(aAgentPair.Remote))
	assert.True(bAgentPair.Remote.Equal(aAgentPair.Local))

	assert.NoError(wan.Stop())
	assert.NoError(aAgent.Close())
	assert.NoError(bAgent.Close())
}
