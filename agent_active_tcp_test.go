// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"net"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/transport/v2/stdnet"
	"github.com/pion/transport/v2/test"
	"github.com/stretchr/testify/require"
)

func getLocalIPAddress(t *testing.T, networkType NetworkType) net.IP {
	require := require.New(t)

	net, err := stdnet.NewNet()
	require.NoError(err)

	localIPs, err := localInterfaces(net, nil, nil, []NetworkType{networkType}, false)
	require.NoError(err)
	require.NotEmpty(localIPs)

	return localIPs[0]
}

func ipv6Available(t *testing.T) bool {
	require := require.New(t)

	net, err := stdnet.NewNet()
	require.NoError(err)

	localIPs, err := localInterfaces(net, nil, nil, []NetworkType{NetworkTypeTCP6}, false)
	require.NoError(err)

	return len(localIPs) > 0
}

func TestAgentActiveTCP(t *testing.T) {
	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 5).Stop()

	const listenPort = 7686
	type testCase struct {
		name                    string
		networkTypes            []NetworkType
		listenIPAddress         net.IP
		selectedPairNetworkType string
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
			},
			testCase{
				name:                    "UDP is preferred over TCP6", // This fails some time
				networkTypes:            supportedNetworkTypes(),
				listenIPAddress:         getLocalIPAddress(t, NetworkTypeTCP6),
				selectedPairNetworkType: udp,
			},
		)
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			require := require.New(t)

			listener, err := net.ListenTCP("tcp", &net.TCPAddr{
				IP:   testCase.listenIPAddress,
				Port: listenPort,
			})
			require.NoError(err)
			defer func() {
				_ = listener.Close()
			}()

			loggerFactory := logging.NewDefaultLoggerFactory()
			loggerFactory.DefaultLogLevel.Set(logging.LogLevelTrace)

			tcpMux := NewTCPMuxDefault(TCPMuxParams{
				Listener:       listener,
				Logger:         loggerFactory.NewLogger("passive-ice-tcp-mux"),
				ReadBufferSize: 20,
			})

			defer func() {
				_ = tcpMux.Close()
			}()

			require.NotNil(tcpMux.LocalAddr())

			hostAcceptanceMinWait := 100 * time.Millisecond

			passiveAgent, err := NewAgent(&AgentConfig{
				TCPMux:                tcpMux,
				CandidateTypes:        []CandidateType{CandidateTypeHost},
				NetworkTypes:          testCase.networkTypes,
				LoggerFactory:         loggerFactory,
				IncludeLoopback:       true,
				HostAcceptanceMinWait: &hostAcceptanceMinWait,
			})
			require.NoError(err)
			require.NotNil(passiveAgent)

			activeAgent, err := NewAgent(&AgentConfig{
				CandidateTypes:        []CandidateType{CandidateTypeHost},
				NetworkTypes:          testCase.networkTypes,
				LoggerFactory:         loggerFactory,
				HostAcceptanceMinWait: &hostAcceptanceMinWait,
			})
			require.NoError(err)
			require.NotNil(activeAgent)

			passiveAgentConn, activeAgentConn := connect(passiveAgent, activeAgent)
			require.NotNil(passiveAgentConn)
			require.NotNil(activeAgentConn)

			pair := passiveAgent.getSelectedPair()
			require.NotNil(pair)
			require.Equal(testCase.selectedPairNetworkType, pair.Local.NetworkType().NetworkShort())

			foo := []byte("foo")
			_, err = passiveAgentConn.Write(foo)
			require.NoError(err)

			buffer := make([]byte, 1024)
			n, err := activeAgentConn.Read(buffer)
			require.NoError(err)
			require.Equal(foo, buffer[:n])

			bar := []byte("bar")
			_, err = activeAgentConn.Write(bar)
			require.NoError(err)

			n, err = passiveAgentConn.Read(buffer)
			require.NoError(err)
			require.Equal(bar, buffer[:n])

			require.NoError(activeAgentConn.Close())
			require.NoError(passiveAgentConn.Close())
		})
	}
}
