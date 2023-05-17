// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"net"
	"testing"
	"time"

	"github.com/pion/transport/v2/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUDPMuxDefaultWithNAT1To1IPsUsage asserts that candidates
// are given and connections are valid when using UDPMuxDefault and NAT1To1IPs.
func TestUDPMuxDefaultWithNAT1To1IPsUsage(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 30).Stop()

	conn, err := net.ListenPacket("udp4", ":0")
	require.NoError(err)
	defer func() {
		_ = conn.Close()
	}()

	mux := NewUDPMuxDefault(UDPMuxParams{
		UDPConn: conn,
	})
	defer func() {
		_ = mux.Close()
	}()

	agent, err := NewAgent(&AgentConfig{
		NAT1To1IPs:             []string{"1.2.3.4"},
		NAT1To1IPCandidateType: CandidateTypeHost,
		UDPMux:                 mux,
	})
	require.NoError(err)

	gatherCandidateDone := make(chan struct{})
	assert.NoError(agent.OnCandidate(func(c Candidate) {
		if c == nil {
			close(gatherCandidateDone)
		} else {
			assert.Equal("1.2.3.4", c.Address())
		}
	}))
	assert.NoError(agent.GatherCandidates())
	<-gatherCandidateDone

	assert.NotEqual(0, len(mux.connsIPv4))

	assert.NoError(agent.Close())
}
