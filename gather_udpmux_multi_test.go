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

// Assert that candidates are given for each mux in a MultiUDPMux
func TestMultiUDPMuxUsage(t *testing.T) {
	assert := assert.New(t)
	report := test.CheckRoutines(t)
	defer report()

	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	var expectedPorts []int
	var udpMuxInstances []UDPMux
	for i := 0; i < 3; i++ {
		port := randomPort(t)
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IP{127, 0, 0, 1}, Port: port})
		assert.NoError(err)
		defer func() {
			_ = conn.Close()
		}()

		expectedPorts = append(expectedPorts, port)
		muxDefault := NewUDPMuxDefault(UDPMuxParams{UDPConn: conn})
		udpMuxInstances = append(udpMuxInstances, muxDefault)
		idx := i
		defer func() {
			_ = udpMuxInstances[idx].Close()
		}()
	}

	a, err := NewAgent(&AgentConfig{
		NetworkTypes:   []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
		CandidateTypes: []CandidateType{CandidateTypeHost},
		UDPMux:         NewMultiUDPMuxDefault(udpMuxInstances...),
	})
	assert.NoError(err)

	candidateCh := make(chan Candidate)
	assert.NoError(a.OnCandidate(func(c Candidate) {
		if c == nil {
			close(candidateCh)
			return
		}
		candidateCh <- c
	}))
	assert.NoError(a.GatherCandidates())

	portFound := make(map[int]bool)
	for c := range candidateCh {
		portFound[c.Port()] = true
		assert.True(c.NetworkType().IsUDP(), "All candidates should be UDP")
	}
	assert.Len(portFound, len(expectedPorts))
	for _, port := range expectedPorts {
		assert.True(portFound[port], "There should be a candidate for each UDP mux port")
	}

	assert.NoError(a.Close())
}
