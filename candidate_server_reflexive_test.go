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

	"github.com/pion/stun/v2"
	"github.com/pion/transport/v3/test"
	"github.com/pion/turn/v3"
	"github.com/stretchr/testify/require"
)

func TestServerReflexiveOnlyConnection(t *testing.T) {
	defer test.CheckRoutines(t)()

	// Limit runtime in case of deadlocks
	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	serverPort := randomPort(t)
	serverListener, err := net.ListenPacket("udp4", "127.0.0.1:"+strconv.Itoa(serverPort))
	require.NoError(t, err)

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
	require.NoError(t, err)

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
	if err != nil {
		t.Fatal(err)
	}

	aNotifier, aConnected := onConnected()
	if err = aAgent.OnConnectionStateChange(aNotifier); err != nil {
		t.Fatal(err)
	}

	bAgent, err := NewAgent(cfg)
	if err != nil {
		t.Fatal(err)
	}

	bNotifier, bConnected := onConnected()
	if err = bAgent.OnConnectionStateChange(bNotifier); err != nil {
		t.Fatal(err)
	}

	connect(aAgent, bAgent)
	<-aConnected
	<-bConnected

	require.NoError(t, aAgent.Close())
	require.NoError(t, bAgent.Close())
	require.NoError(t, server.Close())
}
