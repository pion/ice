// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package ice

import (
	"context"
	"regexp"
	"runtime"
	"testing"
	"time"

	"github.com/pion/transport/v5/test"
	"github.com/stretchr/testify/require"
)

func skipMulticastDNSOnDarwin(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "darwin" {
		t.Skip("mDNS multicast bind is unreliable on the macOS CI runner")
	}
}

func TestMulticastDNSOnlyConnection(t *testing.T) {
	skipMulticastDNSOnDarwin(t)

	defer test.CheckRoutines(t)()

	// Limit runtime in case of deadlocks
	defer test.TimeOut(time.Second * 30).Stop()

	type testCase struct {
		Name         string
		NetworkTypes []NetworkType
	}

	testCases := []testCase{{Name: "UDP4", NetworkTypes: []NetworkType{NetworkTypeUDP4}}}

	if ipv6Available(t) {
		testCases = append(testCases, testCase{Name: "UDP6", NetworkTypes: []NetworkType{NetworkTypeUDP6}}, testCase{Name: "UDP46", NetworkTypes: []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6}})
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			cfgGatherOptions := []GatherOption{WithNetworkTypes(tc.NetworkTypes), WithCandidateTypes([]CandidateType{CandidateTypeHost})}
			cfg := []AgentOption{WithMulticastDNSMode(MulticastDNSModeQueryAndGather), WithInterfaceFilter(problematicNetworkInterfaces)}

			aAgent, err := NewAgent(cfg...)
			require.NoError(t, err)
			defer func() {
				require.NoError(t, aAgent.Close())
			}()

			aNotifier, aConnected := onConnected()
			require.NoError(t, aAgent.OnConnectionStateChange(aNotifier))

			bAgent, err := NewAgent(cfg...)
			require.NoError(t, err)
			defer func() {
				require.NoError(t, bAgent.Close())
			}()

			bNotifier, bConnected := onConnected()
			require.NoError(t, bAgent.OnConnectionStateChange(bNotifier))

			connect(t, aAgent, bAgent, cfgGatherOptions, cfgGatherOptions)
			<-aConnected
			<-bConnected
		})
	}
}

func TestMulticastDNSMixedConnection(t *testing.T) {
	defer test.CheckRoutines(t)()

	// Limit runtime in case of deadlocks
	defer test.TimeOut(time.Second * 30).Stop()

	type testCase struct {
		Name         string
		NetworkTypes []NetworkType
	}

	testCases := []testCase{{Name: "UDP4", NetworkTypes: []NetworkType{NetworkTypeUDP4}}}

	if ipv6Available(t) {
		testCases = append(testCases, testCase{Name: "UDP6", NetworkTypes: []NetworkType{NetworkTypeUDP6}}, testCase{Name: "UDP46", NetworkTypes: []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6}})
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			aAgentGatherOptions := []GatherOption{WithNetworkTypes(tc.NetworkTypes), WithCandidateTypes([]CandidateType{CandidateTypeHost})}
			aAgent, err := NewAgent(WithMulticastDNSMode(MulticastDNSModeQueryAndGather), WithInterfaceFilter(problematicNetworkInterfaces))
			require.NoError(t, err)
			defer func() {
				require.NoError(t, aAgent.Close())
			}()

			aNotifier, aConnected := onConnected()
			require.NoError(t, aAgent.OnConnectionStateChange(aNotifier))
			bAgentGatherOptions := []GatherOption{WithNetworkTypes(tc.NetworkTypes), WithCandidateTypes([]CandidateType{CandidateTypeHost})}
			bAgent, err := NewAgent(WithMulticastDNSMode(MulticastDNSModeQueryOnly), WithInterfaceFilter(problematicNetworkInterfaces))
			require.NoError(t, err)
			defer func() {
				require.NoError(t, bAgent.Close())
			}()

			bNotifier, bConnected := onConnected()
			require.NoError(t, bAgent.OnConnectionStateChange(bNotifier))

			connect(t, aAgent, bAgent, aAgentGatherOptions, bAgentGatherOptions)
			<-aConnected
			<-bConnected
		})
	}
}

func TestMulticastDNSStaticHostName(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	type testCase struct {
		Name         string
		NetworkTypes []NetworkType
	}

	testCases := []testCase{{Name: "UDP4", NetworkTypes: []NetworkType{NetworkTypeUDP4}}}

	if ipv6Available(t) {
		testCases = append(testCases, testCase{Name: "UDP6", NetworkTypes: []NetworkType{NetworkTypeUDP6}}, testCase{Name: "UDP46", NetworkTypes: []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6}})
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			_, err := NewAgent(WithMulticastDNSMode(MulticastDNSModeQueryAndGather), WithMulticastDNSHostName("invalidHostName"), WithInterfaceFilter(problematicNetworkInterfaces))
			require.Equal(t, err, ErrInvalidMulticastDNSHostName)
			agentGatherOptions := []GatherOption{WithNetworkTypes(tc.NetworkTypes), WithCandidateTypes([]CandidateType{CandidateTypeHost})}
			agent, err := NewAgent(WithMulticastDNSMode(MulticastDNSModeQueryAndGather), WithMulticastDNSHostName("validName.local"), WithInterfaceFilter(problematicNetworkInterfaces))
			require.NoError(t, err)
			defer func() {
				require.NoError(t, agent.Close())
			}()

			correctHostName, resolveFunc := context.WithCancel(context.Background())
			require.NoError(t, agent.OnCandidate(func(c Candidate) {
				if c != nil && c.Address() == "validName.local" {
					resolveFunc()
				}
			}))

			require.NoError(t, agent.Gather(agentGatherOptions...))
			<-correctHostName.Done()
		})
	}
}

func TestGenerateMulticastDNSName(t *testing.T) {
	name, err := generateMulticastDNSName()
	require.NoError(t, err)
	isMDNSName := regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-4[0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}.local+$`).MatchString

	require.True(t, isMDNSName(name))
}

func TestMulticastDNSNetworkChanges(t *testing.T) {
	skipMulticastDNSOnDarwin(t)
	defer test.CheckRoutines(t)()
	defer test.TimeOut(15 * time.Second).Stop()

	allowInterfaces := true
	agent, err := NewAgent(WithIncludeLoopback(), WithInterfaceFilter(func(name string) bool {
		return allowInterfaces && problematicNetworkInterfaces(name)
	}))
	require.NoError(t, err)
	defer func() { require.NoError(t, agent.Close()) }()
	require.NotNil(t, agent.mDNSConn)
	complete := make(chan struct{}, 1)
	require.NoError(t, agent.OnCandidate(func(candidate Candidate) {
		if candidate == nil {
			complete <- struct{}{}
		}
	}))
	gather := func(networkTypes ...NetworkType) {
		t.Helper()
		require.NoError(t, agent.Gather(WithNetworkTypes(networkTypes),
			WithCandidateTypes([]CandidateType{CandidateTypeHost})))
		<-complete
	}

	initial := agent.mDNSConn
	gather()
	require.Same(t, initial, agent.mDNSConn)
	gather(NetworkTypeUDP4, NetworkTypeUDP6)
	require.Same(t, initial, agent.mDNSConn)
	gather(NetworkTypeTCP6, NetworkTypeTCP4)
	require.Same(t, initial, agent.mDNSConn)
	gather(NetworkTypeUDP4)
	require.NotNil(t, agent.mDNSConn)
	require.NotSame(t, initial, agent.mDNSConn)

	allowInterfaces = false
	gather(NetworkTypeUDP4)
	require.Nil(t, agent.mDNSConn)
	require.Equal(t, MulticastDNSModeQueryOnly, agent.mDNSMode)

	allowInterfaces = true
	gather(NetworkTypeUDP4)
	require.NotNil(t, agent.mDNSConn)
	current := agent.mDNSConn
	gather(NetworkTypeUDP4)
	require.Same(t, current, agent.mDNSConn)
}
