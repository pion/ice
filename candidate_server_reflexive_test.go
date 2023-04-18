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

	"github.com/pion/transport/v2/test"
	"github.com/pion/turn/v2"
	"github.com/stretchr/testify/assert"
)

func TestServerReflexiveOnlyConnection(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	// Limit runtime in case of deadlocks
	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	serverPort := randomPort(t)
	serverListener, err := net.ListenPacket("udp4", "127.0.0.1:"+strconv.Itoa(serverPort))
	assert.NoError(t, err)

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
	assert.NoError(t, err)

	cfg := &AgentConfig{
		NetworkTypes: []NetworkType{NetworkTypeUDP4},
		Urls: []*URL{
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

	assert.NoError(t, aAgent.Close())
	assert.NoError(t, bAgent.Close())
	assert.NoError(t, server.Close())
}
