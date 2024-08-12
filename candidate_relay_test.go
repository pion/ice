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

	"github.com/pion/stun/v3"
	"github.com/pion/transport/v3/test"
	"github.com/pion/turn/v4"
	"github.com/stretchr/testify/require"
)

func optimisticAuthHandler(string, string, net.Addr) (key []byte, ok bool) {
	return turn.GenerateAuthKey("username", "pion.ly", "password"), true
}

func TestRelayOnlyConnection(t *testing.T) {
	// Limit runtime in case of deadlocks
	defer test.TimeOut(time.Second * 30).Stop()

	defer test.CheckRoutines(t)()

	serverPort := randomPort(t)
	serverListener, err := net.ListenPacket("udp", localhostIPStr+":"+strconv.Itoa(serverPort))
	require.NoError(t, err)

	server, err := turn.NewServer(turn.ServerConfig{
		Realm:       "pion.ly",
		AuthHandler: optimisticAuthHandler,
		PacketConnConfigs: []turn.PacketConnConfig{
			{
				PacketConn:            serverListener,
				RelayAddressGenerator: &turn.RelayAddressGeneratorNone{Address: localhostIPStr + ""},
			},
		},
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, server.Close())
	}()

	cfg := &AgentConfig{
		NetworkTypes: supportedNetworkTypes(),
		Urls: []*stun.URI{
			{
				Scheme:   stun.SchemeTypeTURN,
				Host:     localhostIPStr + "",
				Username: "username",
				Password: "password",
				Port:     serverPort,
				Proto:    stun.ProtoTypeUDP,
			},
		},
		CandidateTypes: []CandidateType{CandidateTypeRelay},
	}

	aAgent, err := NewAgent(cfg)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, aAgent.Close())
	}()

	aNotifier, aConnected := onConnected()
	require.NoError(t, aAgent.OnConnectionStateChange(aNotifier))

	bAgent, err := NewAgent(cfg)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, bAgent.Close())
	}()

	bNotifier, bConnected := onConnected()
	require.NoError(t, bAgent.OnConnectionStateChange(bNotifier))

	connect(aAgent, bAgent)
	<-aConnected
	<-bConnected
}
