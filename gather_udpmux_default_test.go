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
)

// TestUDPMuxDefaultWithNAT1To1IPsUsage asserts that candidates
// are given and connections are valid when using UDPMuxDefault and NAT1To1IPs.
func TestUDPMuxDefaultWithNAT1To1IPsUsage(t *testing.T) {
	assert := assert.New(t)
	report := test.CheckRoutines(t)
	defer report()

	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	conn, err := net.ListenPacket("udp4", ":0")
	assert.NoError(err)
	defer func() {
		_ = conn.Close()
	}()

	mux := NewUDPMuxDefault(UDPMuxParams{
		UDPConn: conn,
	})
	defer func() {
		_ = mux.Close()
	}()

	a, err := NewAgent(&AgentConfig{
		NAT1To1IPs:             []string{"1.2.3.4"},
		NAT1To1IPCandidateType: CandidateTypeHost,
		UDPMux:                 mux,
	})
	assert.NoError(err)

	gatherCandidateDone := make(chan struct{})
	assert.NoError(a.OnCandidate(func(c Candidate) {
		if c == nil {
			close(gatherCandidateDone)
		} else {
			assert.Equal("1.2.3.4", c.Address())
		}
	}))
	assert.NoError(a.GatherCandidates())
	<-gatherCandidateDone

	assert.NotEqual(0, len(mux.connsIPv4))

	assert.NoError(a.Close())
}
