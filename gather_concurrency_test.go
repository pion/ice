// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"context"
	"crypto/tls"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/dtls/v2"
	"github.com/pion/dtls/v2/pkg/crypto/selfsign"
	"github.com/pion/logging"
	"github.com/pion/stun"
	"github.com/pion/transport/v2/test"
	"github.com/pion/turn/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGatherConcurrency(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 30).Stop()

	agent, err := NewAgent(&AgentConfig{
		NetworkTypes:    []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
		IncludeLoopback: true,
	})
	require.NoError(err)

	candidateGathered, candidateGatheredFunc := context.WithCancel(context.Background())
	assert.NoError(agent.OnCandidate(func(c Candidate) {
		candidateGatheredFunc()
	}))

	// Testing for panic
	for i := 0; i < 10; i++ {
		_ = agent.GatherCandidates()
	}

	<-candidateGathered.Done()

	assert.NoError(agent.Close())
}

func TestLoopbackCandidate(t *testing.T) {
	assert := assert.New(t)

	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 30).Stop()

	type testCase struct {
		name        string
		agentConfig *AgentConfig
		loExpected  bool
	}

	mux, err := NewMultiUDPMuxFromPort(12500)
	assert.NoError(err)

	muxWithLo, errLo := NewMultiUDPMuxFromPort(12501, UDPMuxFromPortWithLoopback())
	assert.NoError(errLo)

	testCases := []testCase{
		{
			name: "mux should not have loopback candidate",
			agentConfig: &AgentConfig{
				NetworkTypes: []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
				UDPMux:       mux,
			},
			loExpected: false,
		},
		{
			name: "mux with loopback should not have loopback candidate",
			agentConfig: &AgentConfig{
				NetworkTypes: []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
				UDPMux:       muxWithLo,
			},
			loExpected: true,
		},
		{
			name: "include loopback enabled",
			agentConfig: &AgentConfig{
				NetworkTypes:    []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
				IncludeLoopback: true,
			},
			loExpected: true,
		},
		{
			name: "include loopback disabled",
			agentConfig: &AgentConfig{
				NetworkTypes:    []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
				IncludeLoopback: false,
			},
			loExpected: false,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)

			agent, err := NewAgent(tc.agentConfig)
			require.NoError(err)

			candidateGathered, candidateGatheredFunc := context.WithCancel(context.Background())
			var loopback int32
			assert.NoError(agent.OnCandidate(func(c Candidate) {
				if c != nil {
					if net.ParseIP(c.Address()).IsLoopback() {
						atomic.StoreInt32(&loopback, 1)
					}
				} else {
					candidateGatheredFunc()
					return
				}
				t.Log(c.NetworkType(), c.Priority(), c)
			}))
			assert.NoError(agent.GatherCandidates())

			<-candidateGathered.Done()

			assert.NoError(agent.Close())
			assert.Equal(tc.loExpected, atomic.LoadInt32(&loopback) == 1)
		})
	}

	assert.NoError(mux.Close())
	assert.NoError(muxWithLo.Close())
}

// Assert that STUN gathering is done concurrently
func TestSTUNConcurrency(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 30).Stop()

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

	urls := []*stun.URI{}
	for i := 0; i <= 10; i++ {
		urls = append(urls, &stun.URI{
			Scheme: stun.SchemeTypeSTUN,
			Host:   "127.0.0.1",
			Port:   serverPort + 1,
		})
	}
	urls = append(urls, &stun.URI{
		Scheme: stun.SchemeTypeSTUN,
		Host:   "127.0.0.1",
		Port:   serverPort,
	})

	listener, err := net.ListenTCP("tcp", &net.TCPAddr{
		IP: net.IP{127, 0, 0, 1},
	})
	require.NoError(err)
	defer func() {
		_ = listener.Close()
	}()

	agent, err := NewAgent(&AgentConfig{
		NetworkTypes:   supportedNetworkTypes(),
		Urls:           urls,
		CandidateTypes: []CandidateType{CandidateTypeHost, CandidateTypeServerReflexive},
		TCPMux: NewTCPMuxDefault(
			TCPMuxParams{
				Listener:       listener,
				Logger:         logging.NewDefaultLoggerFactory().NewLogger("ice"),
				ReadBufferSize: 8,
			},
		),
	})
	require.NoError(err)

	candidateGathered, candidateGatheredFunc := context.WithCancel(context.Background())
	assert.NoError(agent.OnCandidate(func(c Candidate) {
		if c == nil {
			candidateGatheredFunc()
			return
		}
		t.Log(c.NetworkType(), c.Priority(), c)
	}))
	assert.NoError(agent.GatherCandidates())

	<-candidateGathered.Done()

	assert.NoError(agent.Close())
	assert.NoError(server.Close())
}

// Assert that TURN gathering is done concurrently
func TestTURNConcurrency(t *testing.T) {
	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 30).Stop()

	runTest := func(t *testing.T, protocol stun.ProtoType, scheme stun.SchemeType, packetConn net.PacketConn, listener net.Listener, serverPort int) {
		assert := assert.New(t)
		require := require.New(t)

		packetConnConfigs := []turn.PacketConnConfig{}
		if packetConn != nil {
			packetConnConfigs = append(packetConnConfigs, turn.PacketConnConfig{
				PacketConn:            packetConn,
				RelayAddressGenerator: &turn.RelayAddressGeneratorNone{Address: "127.0.0.1"},
			})
		}

		listenerConfigs := []turn.ListenerConfig{}
		if listener != nil {
			listenerConfigs = append(listenerConfigs, turn.ListenerConfig{
				Listener:              listener,
				RelayAddressGenerator: &turn.RelayAddressGeneratorNone{Address: "127.0.0.1"},
			})
		}

		server, err := turn.NewServer(turn.ServerConfig{
			Realm:             "pion.ly",
			AuthHandler:       optimisticAuthHandler,
			PacketConnConfigs: packetConnConfigs,
			ListenerConfigs:   listenerConfigs,
		})
		assert.NoError(err)

		urls := []*stun.URI{}
		for i := 0; i <= 10; i++ {
			urls = append(urls, &stun.URI{
				Scheme:   scheme,
				Host:     "127.0.0.1",
				Username: "username",
				Password: "password",
				Proto:    protocol,
				Port:     serverPort + 1 + i,
			})
		}
		urls = append(urls, &stun.URI{
			Scheme:   scheme,
			Host:     "127.0.0.1",
			Username: "username",
			Password: "password",
			Proto:    protocol,
			Port:     serverPort,
		})

		agent, err := NewAgent(&AgentConfig{
			CandidateTypes:     []CandidateType{CandidateTypeRelay},
			InsecureSkipVerify: true,
			NetworkTypes:       supportedNetworkTypes(),
			Urls:               urls,
		})
		require.NoError(err)

		candidateGathered, candidateGatheredFunc := context.WithCancel(context.Background())
		assert.NoError(agent.OnCandidate(func(c Candidate) {
			if c != nil {
				candidateGatheredFunc()
			}
		}))
		assert.NoError(agent.GatherCandidates())

		<-candidateGathered.Done()

		assert.NoError(agent.Close())
		assert.NoError(server.Close())
	}

	t.Run("UDP Relay", func(t *testing.T) {
		serverPort := randomPort(t)
		serverListener, err := net.ListenPacket("udp", "127.0.0.1:"+strconv.Itoa(serverPort))
		assert.NoError(t, err)

		runTest(t, stun.ProtoTypeUDP, stun.SchemeTypeTURN, serverListener, nil, serverPort)
	})

	t.Run("TCP Relay", func(t *testing.T) {
		serverPort := randomPort(t)
		serverListener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(serverPort))
		assert.NoError(t, err)

		runTest(t, stun.ProtoTypeTCP, stun.SchemeTypeTURN, nil, serverListener, serverPort)
	})

	t.Run("TLS Relay", func(t *testing.T) {
		certificate, genErr := selfsign.GenerateSelfSigned()
		assert.NoError(t, genErr)

		serverPort := randomPort(t)
		serverListener, err := tls.Listen("tcp", "127.0.0.1:"+strconv.Itoa(serverPort), &tls.Config{ //nolint:gosec
			Certificates: []tls.Certificate{certificate},
		})
		assert.NoError(t, err)

		runTest(t, stun.ProtoTypeTCP, stun.SchemeTypeTURNS, nil, serverListener, serverPort)
	})

	t.Run("DTLS Relay", func(t *testing.T) {
		certificate, genErr := selfsign.GenerateSelfSigned()
		assert.NoError(t, genErr)

		serverPort := randomPort(t)
		serverListener, err := dtls.Listen("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: serverPort}, &dtls.Config{
			Certificates: []tls.Certificate{certificate},
		})
		assert.NoError(t, err)

		runTest(t, stun.ProtoTypeUDP, stun.SchemeTypeTURNS, nil, serverListener, serverPort)
	})
}

// Assert that STUN and TURN gathering are done concurrently
func TestSTUNTURNConcurrency(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 8).Stop()

	serverPort := randomPort(t)
	serverListener, err := net.ListenPacket("udp4", "127.0.0.1:"+strconv.Itoa(serverPort))
	require.NoError(err)

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
	require.NoError(err)

	urls := []*stun.URI{}
	for i := 0; i <= 10; i++ {
		urls = append(urls, &stun.URI{
			Scheme: stun.SchemeTypeSTUN,
			Host:   "127.0.0.1",
			Port:   serverPort + 1,
		})
	}
	urls = append(urls, &stun.URI{
		Scheme:   stun.SchemeTypeTURN,
		Proto:    stun.ProtoTypeUDP,
		Host:     "127.0.0.1",
		Port:     serverPort,
		Username: "username",
		Password: "password",
	})

	agent, err := NewAgent(&AgentConfig{
		NetworkTypes:   supportedNetworkTypes(),
		Urls:           urls,
		CandidateTypes: []CandidateType{CandidateTypeServerReflexive, CandidateTypeRelay},
	})
	require.NoError(err)

	{
		gatherTimeOut := test.TimeOut(time.Second * 3) // As TURN and STUN should be checked in parallel, this should complete before the default STUN timeout (5s)
		candidateGathered, candidateGatheredFunc := context.WithCancel(context.Background())
		require.NoError(agent.OnCandidate(func(c Candidate) {
			if c != nil {
				candidateGatheredFunc()
			}
		}))
		require.NoError(agent.GatherCandidates())

		<-candidateGathered.Done()

		gatherTimeOut.Stop()
	}

	assert.NoError(agent.Close())
	assert.NoError(server.Close())
}
