// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"context"
	"testing"
	"time"

	"github.com/pion/stun"
	"github.com/pion/transport/v2/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Assert that Agent emits Connecting/Connected/Disconnected/Failed/Closed messages
func TestOnConnectionStateChangeCallback(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	lim := test.TimeOut(time.Second * 5)
	defer lim.Stop()

	disconnectedDuration := time.Second
	failedDuration := time.Second
	KeepaliveInterval := time.Duration(0)

	cfg := &AgentConfig{
		Urls:                []*stun.URI{},
		NetworkTypes:        supportedNetworkTypes(),
		DisconnectedTimeout: &disconnectedDuration,
		FailedTimeout:       &failedDuration,
		KeepaliveInterval:   &KeepaliveInterval,
	}

	aAgent, err := NewAgent(cfg)
	if err != nil {
		t.Error(err)
	}

	bAgent, err := NewAgent(cfg)
	if err != nil {
		t.Error(err)
	}

	isChecking := make(chan interface{})
	isConnected := make(chan interface{})
	isDisconnected := make(chan interface{})
	isFailed := make(chan interface{})
	isClosed := make(chan interface{})
	err = aAgent.OnConnectionStateChange(func(c ConnectionState) {
		switch c {
		case ConnectionStateChecking:
			close(isChecking)
		case ConnectionStateConnected:
			close(isConnected)
		case ConnectionStateDisconnected:
			close(isDisconnected)
		case ConnectionStateFailed:
			close(isFailed)
		case ConnectionStateClosed:
			close(isClosed)
		default:
		}
	})
	if err != nil {
		t.Error(err)
	}

	connect(aAgent, bAgent)

	<-isChecking
	<-isConnected
	<-isDisconnected
	<-isFailed

	assert.NoError(t, aAgent.Close())
	assert.NoError(t, bAgent.Close())

	<-isClosed
}

func TestOnSelectedCandidatePairChange(t *testing.T) {
	agent, candidatePair := fixtureTestOnSelectedCandidatePairChange(t)

	callbackCalled := make(chan struct{}, 1)
	err := agent.OnSelectedCandidatePairChange(func(local, remote Candidate) {
		close(callbackCalled)
	})
	require.NoError(t, err)

	err = agent.run(context.Background(), func(ctx context.Context, agent *Agent) {
		agent.setSelectedPair(candidatePair)
	})
	require.NoError(t, err)

	<-callbackCalled
	require.NoError(t, agent.Close())
}

func fixtureTestOnSelectedCandidatePairChange(t *testing.T) (*Agent, *CandidatePair) {
	agent, err := NewAgent(&AgentConfig{})
	require.NoError(t, err)

	candidatePair := makeCandidatePair(t)
	return agent, candidatePair
}

func makeCandidatePair(t *testing.T) *CandidatePair {
	hostLocal := newHostLocal(t)
	relayRemote := newRelayRemote(t)

	candidatePair := newCandidatePair(hostLocal, relayRemote, false)
	return candidatePair
}

func TestCloseInConnectionStateCallback(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	lim := test.TimeOut(time.Second * 5)
	defer lim.Stop()

	disconnectedDuration := time.Second
	failedDuration := time.Second
	KeepaliveInterval := time.Duration(0)
	CheckInterval := 500 * time.Millisecond

	cfg := &AgentConfig{
		Urls:                []*stun.URI{},
		NetworkTypes:        supportedNetworkTypes(),
		DisconnectedTimeout: &disconnectedDuration,
		FailedTimeout:       &failedDuration,
		KeepaliveInterval:   &KeepaliveInterval,
		CheckInterval:       &CheckInterval,
	}

	aAgent, err := NewAgent(cfg)
	if err != nil {
		t.Error(err)
	}

	bAgent, err := NewAgent(cfg)
	if err != nil {
		t.Error(err)
	}

	isClosed := make(chan interface{})
	isConnected := make(chan interface{})
	err = aAgent.OnConnectionStateChange(func(c ConnectionState) {
		switch c {
		case ConnectionStateConnected:
			<-isConnected
			assert.NoError(t, aAgent.Close())
		case ConnectionStateClosed:
			close(isClosed)
		default:
		}
	})
	if err != nil {
		t.Error(err)
	}

	connect(aAgent, bAgent)
	close(isConnected)

	<-isClosed
	assert.NoError(t, bAgent.Close())
}

func TestRunTaskInConnectionStateCallback(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	lim := test.TimeOut(time.Second * 5)
	defer lim.Stop()

	oneSecond := time.Second
	KeepaliveInterval := time.Duration(0)
	CheckInterval := 50 * time.Millisecond

	cfg := &AgentConfig{
		Urls:                []*stun.URI{},
		NetworkTypes:        supportedNetworkTypes(),
		DisconnectedTimeout: &oneSecond,
		FailedTimeout:       &oneSecond,
		KeepaliveInterval:   &KeepaliveInterval,
		CheckInterval:       &CheckInterval,
	}

	aAgent, err := NewAgent(cfg)
	check(err)
	bAgent, err := NewAgent(cfg)
	check(err)

	isComplete := make(chan interface{})
	err = aAgent.OnConnectionStateChange(func(c ConnectionState) {
		if c == ConnectionStateConnected {
			_, _, errCred := aAgent.GetLocalUserCredentials()
			assert.NoError(t, errCred)
			assert.NoError(t, aAgent.Restart("", ""))
			close(isComplete)
		}
	})
	if err != nil {
		t.Error(err)
	}

	connect(aAgent, bAgent)

	<-isComplete
	assert.NoError(t, aAgent.Close())
	assert.NoError(t, bAgent.Close())
}

func TestRunTaskInSelectedCandidatePairChangeCallback(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	lim := test.TimeOut(time.Second * 5)
	defer lim.Stop()

	oneSecond := time.Second
	KeepaliveInterval := time.Duration(0)
	CheckInterval := 50 * time.Millisecond

	cfg := &AgentConfig{
		Urls:                []*stun.URI{},
		NetworkTypes:        supportedNetworkTypes(),
		DisconnectedTimeout: &oneSecond,
		FailedTimeout:       &oneSecond,
		KeepaliveInterval:   &KeepaliveInterval,
		CheckInterval:       &CheckInterval,
	}

	aAgent, err := NewAgent(cfg)
	check(err)
	bAgent, err := NewAgent(cfg)
	check(err)

	isComplete := make(chan interface{})
	isTested := make(chan interface{})
	if err = aAgent.OnSelectedCandidatePairChange(func(Candidate, Candidate) {
		go func() {
			_, _, errCred := aAgent.GetLocalUserCredentials()
			assert.NoError(t, errCred)
			close(isTested)
		}()
	}); err != nil {
		t.Error(err)
	}
	if err = aAgent.OnConnectionStateChange(func(c ConnectionState) {
		if c == ConnectionStateConnected {
			close(isComplete)
		}
	}); err != nil {
		t.Error(err)
	}

	connect(aAgent, bAgent)

	<-isComplete
	<-isTested
	assert.NoError(t, aAgent.Close())
	assert.NoError(t, bAgent.Close())
}
