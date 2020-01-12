// +build !js

package ice

import (
	"context"
	"testing"
	"time"

	"github.com/pion/transport/test"
	"github.com/stretchr/testify/assert"
)

func TestMulticastDNSOnlyConnection(t *testing.T) {
	// Limit runtime in case of deadlocks
	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	report := test.CheckRoutines(t)
	defer report()

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

	assert.NoError(t, aAgent.Close())
	assert.NoError(t, bAgent.Close())
}

func TestMulticastDNSMixedConnection(t *testing.T) {
	// Limit runtime in case of deadlocks
	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	report := test.CheckRoutines(t)
	defer report()

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

	assert.NoError(t, aAgent.Close())
	assert.NoError(t, bAgent.Close())
}

func TestMulticastDNSStaticHostName(t *testing.T) {
	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	report := test.CheckRoutines(t)
	defer report()

	_, err := NewAgent(&AgentConfig{
		NetworkTypes:         []NetworkType{NetworkTypeUDP4},
		CandidateTypes:       []CandidateType{CandidateTypeHost},
		MulticastDNSMode:     MulticastDNSModeQueryAndGather,
		MulticastDNSHostName: "invalidHostName",
	})
	assert.Equal(t, err, ErrInvalidMulticastDNSHostName)

	agent, err := NewAgent(&AgentConfig{
		Trickle:              true,
		NetworkTypes:         []NetworkType{NetworkTypeUDP4},
		CandidateTypes:       []CandidateType{CandidateTypeHost},
		MulticastDNSMode:     MulticastDNSModeQueryAndGather,
		MulticastDNSHostName: "validName.local",
	})
	assert.NoError(t, err)

	correctHostName, resolveFunc := context.WithCancel(context.Background())
	assert.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c != nil && c.Address() == "validName.local" {
			resolveFunc()
		}
	}))

	assert.NoError(t, agent.GatherCandidates())
	<-correctHostName.Done()
	assert.NoError(t, agent.Close())
}
