// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/pion/stun"
	"github.com/pion/transport/v2/test"
	"github.com/pion/turn/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServerReflexiveOnlyConnection(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	report := test.CheckRoutines(t)
	defer report()

	// Limit runtime in case of deadlocks
	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	serverPort := randomPort(t)
	serverListener, err := net.ListenPacket("udp4", "127.0.0.1:"+strconv.Itoa(serverPort))
	assert.NoError(err)

	server, err := turn.NewServer(turn.ServerConfig{
		Realm:       "pion.ly",
		AuthHandler: optimisticAuthHandler,
		PacketConnConfigs: []turn.PacketConnConfig{
			{
				PacketConn:            serverListener,
				RelayAddressGenerator: &turn.RelayAddressGeneratorNone{Address: "127.0.0.1"},
			},
		},
	})
	assert.NoError(err)

	cfg := &AgentConfig{
		NetworkTypes: []NetworkType{NetworkTypeUDP4},
		Urls: []*stun.URI{
			{
				Scheme: SchemeTypeSTUN,
				Host:   "127.0.0.1",
				Port:   serverPort,
			},
		},
		CandidateTypes: []CandidateType{CandidateTypeServerReflexive},
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
	assert.NoError(server.Close())
}
