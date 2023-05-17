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

func optimisticAuthHandler(string, string, net.Addr) (key []byte, ok bool) {
	return turn.GenerateAuthKey("username", "pion.ly", "password"), true
}

func TestRelayOnlyConnection(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 30).Stop()

	serverPort := randomPort(t)
	serverListener, err := net.ListenPacket("udp", "127.0.0.1:"+strconv.Itoa(serverPort))
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
		NetworkTypes: supportedNetworkTypes(),
		Urls: []*stun.URI{
			{
				Scheme:   stun.SchemeTypeTURN,
				Host:     "127.0.0.1",
				Username: "username",
				Password: "password",
				Port:     serverPort,
				Proto:    stun.ProtoTypeUDP,
			},
		},
		CandidateTypes: []CandidateType{CandidateTypeRelay},
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
	assert.NoError(server.Close())
}
