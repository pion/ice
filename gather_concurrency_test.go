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

	"github.com/pion/dtls"
	"github.com/pion/dtls/v2/pkg/crypto/selfsign"
	"github.com/pion/logging"
	"github.com/pion/stun"
	"github.com/pion/transport/v2/test"
	"github.com/pion/turn/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGatherConcurrency(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	a, err := NewAgent(&AgentConfig{
		NetworkTypes:    []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
		IncludeLoopback: true,
	})
	assert.NoError(t, err)

	candidateGathered, candidateGatheredFunc := context.WithCancel(context.Background())
	assert.NoError(t, a.OnCandidate(func(c Candidate) {
		candidateGatheredFunc()
	}))

	// Testing for panic
	for i := 0; i < 10; i++ {
		_ = a.GatherCandidates()
	}

	<-candidateGathered.Done()

	assert.NoError(t, a.Close())
}

func TestLoopbackCandidate(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()
	type testCase struct {
		name        string
		agentConfig *AgentConfig
		loExpected  bool
	}
	mux, err := NewMultiUDPMuxFromPort(12500)
	assert.NoError(t, err)
	muxWithLo, errlo := NewMultiUDPMuxFromPort(12501, UDPMuxFromPortWithLoopback())
	assert.NoError(t, errlo)
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
			name: "includeloopback enabled",
			agentConfig: &AgentConfig{
				NetworkTypes:    []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
				IncludeLoopback: true,
			},
			loExpected: true,
		},
		{
			name: "includeloopback disabled",
			agentConfig: &AgentConfig{
				NetworkTypes:    []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
				IncludeLoopback: false,
			},
			loExpected: false,
		},
	}

	for _, tc := range testCases {
		tcase := tc
		t.Run(tcase.name, func(t *testing.T) {
			a, err := NewAgent(tc.agentConfig)
			assert.NoError(t, err)

			candidateGathered, candidateGatheredFunc := context.WithCancel(context.Background())
			var loopback int32
			assert.NoError(t, a.OnCandidate(func(c Candidate) {
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
			assert.NoError(t, a.GatherCandidates())

			<-candidateGathered.Done()

			assert.NoError(t, a.Close())
			assert.Equal(t, tcase.loExpected, atomic.LoadInt32(&loopback) == 1)
		})
	}

	assert.NoError(t, mux.Close())
	assert.NoError(t, muxWithLo.Close())
}

// Assert that STUN gathering is done concurrently
func TestSTUNConcurrency(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

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
	require.NoError(t, err)
	defer func() {
		_ = listener.Close()
	}()

	a, err := NewAgent(&AgentConfig{
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
	assert.NoError(t, err)

	candidateGathered, candidateGatheredFunc := context.WithCancel(context.Background())
	assert.NoError(t, a.OnCandidate(func(c Candidate) {
		if c == nil {
			candidateGatheredFunc()
			return
		}
		t.Log(c.NetworkType(), c.Priority(), c)
	}))
	assert.NoError(t, a.GatherCandidates())

	<-candidateGathered.Done()

	assert.NoError(t, a.Close())
	assert.NoError(t, server.Close())
}

// Assert that TURN gathering is done concurrently
func TestTURNConcurrency(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	runTest := func(protocol stun.ProtoType, scheme stun.SchemeType, packetConn net.PacketConn, listener net.Listener, serverPort int) {
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
		assert.NoError(t, err)

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

		a, err := NewAgent(&AgentConfig{
			CandidateTypes:     []CandidateType{CandidateTypeRelay},
			InsecureSkipVerify: true,
			NetworkTypes:       supportedNetworkTypes(),
			Urls:               urls,
		})
		assert.NoError(t, err)

		candidateGathered, candidateGatheredFunc := context.WithCancel(context.Background())
		assert.NoError(t, a.OnCandidate(func(c Candidate) {
			if c != nil {
				candidateGatheredFunc()
			}
		}))
		assert.NoError(t, a.GatherCandidates())

		<-candidateGathered.Done()

		assert.NoError(t, a.Close())
		assert.NoError(t, server.Close())
	}

	t.Run("UDP Relay", func(t *testing.T) {
		serverPort := randomPort(t)
		serverListener, err := net.ListenPacket("udp", "127.0.0.1:"+strconv.Itoa(serverPort))
		assert.NoError(t, err)

		runTest(stun.ProtoTypeUDP, stun.SchemeTypeTURN, serverListener, nil, serverPort)
	})

	t.Run("TCP Relay", func(t *testing.T) {
		serverPort := randomPort(t)
		serverListener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(serverPort))
		assert.NoError(t, err)

		runTest(stun.ProtoTypeTCP, stun.SchemeTypeTURN, nil, serverListener, serverPort)
	})

	t.Run("TLS Relay", func(t *testing.T) {
		certificate, genErr := selfsign.GenerateSelfSigned()
		assert.NoError(t, genErr)

		serverPort := randomPort(t)
		serverListener, err := tls.Listen("tcp", "127.0.0.1:"+strconv.Itoa(serverPort), &tls.Config{ //nolint:gosec
			Certificates: []tls.Certificate{certificate},
		})
		assert.NoError(t, err)

		runTest(stun.ProtoTypeTCP, stun.SchemeTypeTURNS, nil, serverListener, serverPort)
	})

	t.Run("DTLS Relay", func(t *testing.T) {
		certificate, genErr := selfsign.GenerateSelfSigned()
		assert.NoError(t, genErr)

		serverPort := randomPort(t)
		serverListener, err := dtls.Listen("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: serverPort}, &dtls.Config{
			Certificates: []tls.Certificate{certificate},
		})
		assert.NoError(t, err)

		runTest(stun.ProtoTypeUDP, stun.SchemeTypeTURNS, nil, serverListener, serverPort)
	})
}

// Assert that STUN and TURN gathering are done concurrently
func TestSTUNTURNConcurrency(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	lim := test.TimeOut(time.Second * 8)
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

	a, err := NewAgent(&AgentConfig{
		NetworkTypes:   supportedNetworkTypes(),
		Urls:           urls,
		CandidateTypes: []CandidateType{CandidateTypeServerReflexive, CandidateTypeRelay},
	})
	assert.NoError(t, err)

	{
		gatherLim := test.TimeOut(time.Second * 3) // As TURN and STUN should be checked in parallel, this should complete before the default STUN timeout (5s)
		candidateGathered, candidateGatheredFunc := context.WithCancel(context.Background())
		assert.NoError(t, a.OnCandidate(func(c Candidate) {
			if c != nil {
				candidateGatheredFunc()
			}
		}))
		assert.NoError(t, a.GatherCandidates())

		<-candidateGathered.Done()

		gatherLim.Stop()
	}

	assert.NoError(t, a.Close())
	assert.NoError(t, server.Close())
}
