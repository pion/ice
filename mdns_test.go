// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/pion/transport/v4/test"
	"github.com/stretchr/testify/require"
)

func TestMulticastDNSOnlyConnection(t *testing.T) {
	defer test.CheckRoutines(t)()

	// Limit runtime in case of deadlocks
	defer test.TimeOut(time.Second * 30).Stop()

	type testCase struct {
		Name         string
		NetworkTypes []NetworkType
	}

	testCases := []testCase{
		{Name: "UDP4", NetworkTypes: []NetworkType{NetworkTypeUDP4}},
	}

	if ipv6Available(t) {
		testCases = append(testCases,
			testCase{Name: "UDP6", NetworkTypes: []NetworkType{NetworkTypeUDP6}},
			testCase{Name: "UDP46", NetworkTypes: []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6}},
		)
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			cfg := &AgentConfig{
				NetworkTypes:     tc.NetworkTypes,
				CandidateTypes:   []CandidateType{CandidateTypeHost},
				MulticastDNSMode: MulticastDNSModeQueryAndGather,
				InterfaceFilter:  problematicNetworkInterfaces,
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

			connect(t, aAgent, bAgent)
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

	testCases := []testCase{
		{Name: "UDP4", NetworkTypes: []NetworkType{NetworkTypeUDP4}},
	}

	if ipv6Available(t) {
		testCases = append(testCases,
			testCase{Name: "UDP6", NetworkTypes: []NetworkType{NetworkTypeUDP6}},
			testCase{Name: "UDP46", NetworkTypes: []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6}},
		)
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			aAgent, err := NewAgent(&AgentConfig{
				NetworkTypes:     tc.NetworkTypes,
				CandidateTypes:   []CandidateType{CandidateTypeHost},
				MulticastDNSMode: MulticastDNSModeQueryAndGather,
				InterfaceFilter:  problematicNetworkInterfaces,
			})
			require.NoError(t, err)
			defer func() {
				require.NoError(t, aAgent.Close())
			}()

			aNotifier, aConnected := onConnected()
			require.NoError(t, aAgent.OnConnectionStateChange(aNotifier))

			bAgent, err := NewAgent(&AgentConfig{
				NetworkTypes:     tc.NetworkTypes,
				CandidateTypes:   []CandidateType{CandidateTypeHost},
				MulticastDNSMode: MulticastDNSModeQueryOnly,
				InterfaceFilter:  problematicNetworkInterfaces,
			})
			require.NoError(t, err)
			defer func() {
				require.NoError(t, bAgent.Close())
			}()

			bNotifier, bConnected := onConnected()
			require.NoError(t, bAgent.OnConnectionStateChange(bNotifier))

			connect(t, aAgent, bAgent)
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

	testCases := []testCase{
		{Name: "UDP4", NetworkTypes: []NetworkType{NetworkTypeUDP4}},
	}

	if ipv6Available(t) {
		testCases = append(testCases,
			testCase{Name: "UDP6", NetworkTypes: []NetworkType{NetworkTypeUDP6}},
			testCase{Name: "UDP46", NetworkTypes: []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6}},
		)
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			_, err := NewAgent(&AgentConfig{
				NetworkTypes:         tc.NetworkTypes,
				CandidateTypes:       []CandidateType{CandidateTypeHost},
				MulticastDNSMode:     MulticastDNSModeQueryAndGather,
				MulticastDNSHostName: "invalidHostName",
				InterfaceFilter:      problematicNetworkInterfaces,
			})
			require.Equal(t, err, ErrInvalidMulticastDNSHostName)

			agent, err := NewAgent(&AgentConfig{
				NetworkTypes:         tc.NetworkTypes,
				CandidateTypes:       []CandidateType{CandidateTypeHost},
				MulticastDNSMode:     MulticastDNSModeQueryAndGather,
				MulticastDNSHostName: "validName.local",
				InterfaceFilter:      problematicNetworkInterfaces,
			})
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

			require.NoError(t, agent.GatherCandidates())
			<-correctHostName.Done()
		})
	}
}

func TestGenerateMulticastDNSName(t *testing.T) {
	name, err := generateMulticastDNSName()
	require.NoError(t, err)
	isMDNSName := regexp.MustCompile(
		`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-4[0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}.local+$`,
	).MatchString

	require.True(t, isMDNSName(name))
}
