// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"fmt"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/transport/v3/stdnet"
	"github.com/pion/transport/v3/test"
	"github.com/stretchr/testify/require"
)

func getLocalIPAddress(t *testing.T, networkType NetworkType) netip.Addr {
	net, err := stdnet.NewNet()
	require.NoError(t, err)
	_, localAddrs, err := localInterfaces(net, problematicNetworkInterfaces, nil, []NetworkType{networkType}, false)
	require.NoError(t, err)
	require.NotEmpty(t, localAddrs)
	return localAddrs[0]
}

func ipv6Available(t *testing.T) bool {
	net, err := stdnet.NewNet()
	require.NoError(t, err)
	_, localAddrs, err := localInterfaces(net, problematicNetworkInterfaces, nil, []NetworkType{NetworkTypeTCP6}, false)
	require.NoError(t, err)
	return len(localAddrs) > 0
}

func TestActiveTCP(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 5).Stop()

	const listenPort = 7686
	type testCase struct {
		name                    string
		networkTypes            []NetworkType
		listenIPAddress         netip.Addr
		selectedPairNetworkType string
		useMDNS                 bool
	}

	testCases := []testCase{
		{
			name:                    "TCP4 connection",
			networkTypes:            []NetworkType{NetworkTypeTCP4},
			listenIPAddress:         getLocalIPAddress(t, NetworkTypeTCP4),
			selectedPairNetworkType: tcp,
		},
		{
			name:                    "UDP is preferred over TCP4", // This fails some time
			networkTypes:            supportedNetworkTypes(),
			listenIPAddress:         getLocalIPAddress(t, NetworkTypeTCP4),
			selectedPairNetworkType: udp,
		},
	}

	if ipv6Available(t) {
		testCases = append(testCases,
			testCase{
				name:                    "TCP6 connection",
				networkTypes:            []NetworkType{NetworkTypeTCP6},
				listenIPAddress:         getLocalIPAddress(t, NetworkTypeTCP6),
				selectedPairNetworkType: tcp,
				// if we don't use mDNS, we will very likely be filtering out location tracked ips.
				useMDNS: true,
			},
			testCase{
				name:                    "UDP is preferred over TCP6",
				networkTypes:            supportedNetworkTypes(),
				listenIPAddress:         getLocalIPAddress(t, NetworkTypeTCP6),
				selectedPairNetworkType: udp,
				// if we don't use mDNS, we will very likely be filtering out location tracked ips.
				useMDNS: true,
			},
		)
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			r := require.New(t)

			listener, err := net.ListenTCP("tcp", &net.TCPAddr{
				IP:   testCase.listenIPAddress.AsSlice(),
				Port: listenPort,
				Zone: testCase.listenIPAddress.Zone(),
			})
			r.NoError(err)
			defer func() {
				_ = listener.Close()
			}()

			loggerFactory := logging.NewDefaultLoggerFactory()

			tcpMux := NewTCPMuxDefault(TCPMuxParams{
				Listener:       listener,
				Logger:         loggerFactory.NewLogger("passive-ice-tcp-mux"),
				ReadBufferSize: 20,
			})

			defer func() {
				_ = tcpMux.Close()
			}()

			r.NotNil(tcpMux.LocalAddr(), "tcpMux.LocalAddr() is nil")

			hostAcceptanceMinWait := 100 * time.Millisecond
			cfg := &AgentConfig{
				TCPMux:                tcpMux,
				CandidateTypes:        []CandidateType{CandidateTypeHost},
				NetworkTypes:          testCase.networkTypes,
				LoggerFactory:         loggerFactory,
				HostAcceptanceMinWait: &hostAcceptanceMinWait,
				InterfaceFilter:       problematicNetworkInterfaces,
			}
			if testCase.useMDNS {
				cfg.MulticastDNSMode = MulticastDNSModeQueryAndGather
			}
			passiveAgent, err := NewAgent(cfg)
			r.NoError(err)
			r.NotNil(passiveAgent)

			activeAgent, err := NewAgent(&AgentConfig{
				CandidateTypes:        []CandidateType{CandidateTypeHost},
				NetworkTypes:          testCase.networkTypes,
				LoggerFactory:         loggerFactory,
				HostAcceptanceMinWait: &hostAcceptanceMinWait,
				InterfaceFilter:       problematicNetworkInterfaces,
			})
			r.NoError(err)
			r.NotNil(activeAgent)

			passiveAgentConn, activeAgenConn := connect(passiveAgent, activeAgent)
			r.NotNil(passiveAgentConn)
			r.NotNil(activeAgenConn)

			defer func() {
				r.NoError(activeAgenConn.Close())
				r.NoError(passiveAgentConn.Close())
			}()

			pair := passiveAgent.getSelectedPair()
			r.NotNil(pair)
			r.Equal(testCase.selectedPairNetworkType, pair.Local.NetworkType().NetworkShort())

			foo := []byte("foo")
			_, err = passiveAgentConn.Write(foo)
			r.NoError(err)

			buffer := make([]byte, 1024)
			n, err := activeAgenConn.Read(buffer)
			r.NoError(err)
			r.Equal(foo, buffer[:n])

			bar := []byte("bar")
			_, err = activeAgenConn.Write(bar)
			r.NoError(err)

			n, err = passiveAgentConn.Read(buffer)
			r.NoError(err)
			r.Equal(bar, buffer[:n])
		})
	}
}

// Assert that Active TCP connectivity isn't established inside
// the main thread of the Agent
func TestActiveTCP_NonBlocking(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 5).Stop()

	cfg := &AgentConfig{
		NetworkTypes:    supportedNetworkTypes(),
		InterfaceFilter: problematicNetworkInterfaces,
	}

	aAgent, err := NewAgent(cfg)
	require.NoError(t, err)

	defer func() {
		require.NoError(t, aAgent.Close())
	}()

	bAgent, err := NewAgent(cfg)
	require.NoError(t, err)

	defer func() {
		require.NoError(t, bAgent.Close())
	}()

	isConnected := make(chan interface{})
	err = aAgent.OnConnectionStateChange(func(c ConnectionState) {
		if c == ConnectionStateConnected {
			close(isConnected)
		}
	})
	require.NoError(t, err)

	// Add a invalid ice-tcp candidate to each
	invalidCandidate, err := UnmarshalCandidate("1052353102 1 tcp 1675624447 192.0.2.1 8080 typ host tcptype passive")
	require.NoError(t, err)
	require.NoError(t, aAgent.AddRemoteCandidate(invalidCandidate))
	require.NoError(t, bAgent.AddRemoteCandidate(invalidCandidate))

	connect(aAgent, bAgent)

	<-isConnected
}

// Assert that we ignore remote TCP candidates when running a UDP Only Agent
func TestActiveTCP_Respect_NetworkTypes(t *testing.T) {
	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 5).Stop()

	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	_, port, err := net.SplitHostPort(tcpListener.Addr().String())
	require.NoError(t, err)

	var incomingTCPCount uint64
	go func() {
		for {
			conn, listenErr := tcpListener.Accept()
			if listenErr != nil {
				return
			}

			require.NoError(t, conn.Close())
			atomic.AddUint64(&incomingTCPCount, ^uint64(0))
		}
	}()

	cfg := &AgentConfig{
		NetworkTypes:    []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6, NetworkTypeTCP6},
		InterfaceFilter: problematicNetworkInterfaces,
		IncludeLoopback: true,
	}

	aAgent, err := NewAgent(cfg)
	require.NoError(t, err)

	defer func() {
		require.NoError(t, aAgent.Close())
	}()

	bAgent, err := NewAgent(cfg)
	require.NoError(t, err)

	defer func() {
		require.NoError(t, bAgent.Close())
	}()

	isConnected := make(chan interface{})
	err = aAgent.OnConnectionStateChange(func(c ConnectionState) {
		if c == ConnectionStateConnected {
			close(isConnected)
		}
	})
	require.NoError(t, err)

	invalidCandidate, err := UnmarshalCandidate(fmt.Sprintf("1052353102 1 tcp 1675624447 127.0.0.1 %s typ host tcptype passive", port))
	require.NoError(t, err)
	require.NoError(t, aAgent.AddRemoteCandidate(invalidCandidate))
	require.NoError(t, bAgent.AddRemoteCandidate(invalidCandidate))

	connect(aAgent, bAgent)

	<-isConnected
	require.NoError(t, tcpListener.Close())
	require.Equal(t, uint64(0), atomic.LoadUint64(&incomingTCPCount))
}
