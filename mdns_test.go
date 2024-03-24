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

	"github.com/pion/transport/v3/test"
	"github.com/stretchr/testify/require"
)

func TestMulticastDNSOnlyConnection(t *testing.T) {
	defer test.CheckRoutines(t)()

	// Limit runtime in case of deadlocks
	defer test.TimeOut(time.Second * 30).Stop()

	cfg := &AgentConfig{
		NetworkTypes:     []NetworkType{NetworkTypeUDP4},
		CandidateTypes:   []CandidateType{CandidateTypeHost},
		MulticastDNSMode: MulticastDNSModeQueryAndGather,
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
}

func TestMulticastDNSMixedConnection(t *testing.T) {
	defer test.CheckRoutines(t)()

	// Limit runtime in case of deadlocks
	defer test.TimeOut(time.Second * 30).Stop()

	aAgent, err := NewAgent(&AgentConfig{
		NetworkTypes:     []NetworkType{NetworkTypeUDP4},
		CandidateTypes:   []CandidateType{CandidateTypeHost},
		MulticastDNSMode: MulticastDNSModeQueryAndGather,
	})
	if err != nil {
		t.Fatal(err)
	}

	aNotifier, aConnected := onConnected()
	if err = aAgent.OnConnectionStateChange(aNotifier); err != nil {
		t.Fatal(err)
	}

	bAgent, err := NewAgent(&AgentConfig{
		NetworkTypes:     []NetworkType{NetworkTypeUDP4},
		CandidateTypes:   []CandidateType{CandidateTypeHost},
		MulticastDNSMode: MulticastDNSModeQueryOnly,
	})
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
}

func TestMulticastDNSStaticHostName(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	_, err := NewAgent(&AgentConfig{
		NetworkTypes:         []NetworkType{NetworkTypeUDP4},
		CandidateTypes:       []CandidateType{CandidateTypeHost},
		MulticastDNSMode:     MulticastDNSModeQueryAndGather,
		MulticastDNSHostName: "invalidHostName",
	})
	require.Equal(t, err, ErrInvalidMulticastDNSHostName)

	agent, err := NewAgent(&AgentConfig{
		NetworkTypes:         []NetworkType{NetworkTypeUDP4},
		CandidateTypes:       []CandidateType{CandidateTypeHost},
		MulticastDNSMode:     MulticastDNSModeQueryAndGather,
		MulticastDNSHostName: "validName.local",
	})
	require.NoError(t, err)

	correctHostName, resolveFunc := context.WithCancel(context.Background())
	require.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c != nil && c.Address() == "validName.local" {
			resolveFunc()
		}
	}))

	require.NoError(t, agent.GatherCandidates())
	<-correctHostName.Done()
	require.NoError(t, agent.Close())
}

func TestGenerateMulticastDNSName(t *testing.T) {
	name, err := generateMulticastDNSName()
	if err != nil {
		t.Fatal(err)
	}
	isMDNSName := regexp.MustCompile(
		`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-4[0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}.local+$`,
	).MatchString

	if !isMDNSName(name) {
		t.Fatalf("mDNS name must be UUID v4 + \".local\" suffix, got %s", name)
	}
}
