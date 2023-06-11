// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/transport/v2/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test1234(t *testing.T) {
	bc := &bufferedConn{
		Conn: nil,
	}
	bc.closed = 3
	r := require.New(t)
	r.NotNil(bc)
	r.Equal(int32(3), bc.closed)
}

// Assert that Active TCP connectivity isn't established inside
// the main thread of the Agent
func TestAgentActive_NonBlocking(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	lim := test.TimeOut(time.Second * 5)
	defer lim.Stop()

	loggerFactory := logging.NewDefaultLoggerFactory()
	loggerFactory.DefaultLogLevel.Set(logging.LogLevelTrace)

	cfg2 := &AgentConfig{
		NetworkTypes:    supportedNetworkTypes(),
		LoggerFactory:   loggerFactory,
		EnableActiveTCP: true,
	}

	cfg1 := &AgentConfig{
		NetworkTypes:    supportedNetworkTypes(),
		LoggerFactory:   loggerFactory,
		EnableActiveTCP: true,
	}

	aAgent, err := NewAgent(cfg1)
	if err != nil {
		t.Error(err)
	}

	bAgent, err := NewAgent(cfg2)
	if err != nil {
		t.Error(err)
	}

	isConnected := make(chan interface{})
	err = aAgent.OnConnectionStateChange(func(c ConnectionState) {
		if c == ConnectionStateConnected {
			close(isConnected)
		}
	})
	if err != nil {
		t.Error(err)
	}

	// Add a invalid ice-tcp candidate to each
	invalidCandidate, err := UnmarshalCandidate("1052353102 1 tcp 1675624447 192.0.2.1 8080 typ host tcptype passive")
	if err != nil {
		t.Fatal(err)
	}
	aAgent.AddRemoteCandidate(invalidCandidate)
	bAgent.AddRemoteCandidate(invalidCandidate)

	connect(aAgent, bAgent)

	<-isConnected
	assert.NoError(t, aAgent.Close())
	assert.NoError(t, bAgent.Close())
}
