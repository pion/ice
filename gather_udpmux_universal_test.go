// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pion/stun"
	"github.com/pion/transport/v2/test"
	"github.com/stretchr/testify/assert"
)

// Assert that UniversalUDPMux is used while gathering when configured in the Agent
func TestUniversalUDPMuxUsage(t *testing.T) {
	assert := assert.New(t)
	report := test.CheckRoutines(t)
	defer report()

	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IP{127, 0, 0, 1}, Port: randomPort(t)})
	assert.NoError(err)
	defer func() {
		_ = conn.Close()
	}()

	udpMuxSrflx := &universalUDPMuxMock{
		conn: conn,
	}

	numSTUNS := 3
	urls := []*stun.URI{}
	for i := 0; i < numSTUNS; i++ {
		urls = append(urls, &stun.URI{
			Scheme: SchemeTypeSTUN,
			Host:   "127.0.0.1",
			Port:   3478 + i,
		})
	}

	a, err := NewAgent(&AgentConfig{
		NetworkTypes:   supportedNetworkTypes(),
		Urls:           urls,
		CandidateTypes: []CandidateType{CandidateTypeServerReflexive},
		UDPMuxSrflx:    udpMuxSrflx,
	})
	assert.NoError(err)

	candidateGathered, candidateGatheredFunc := context.WithCancel(context.Background())
	assert.NoError(a.OnCandidate(func(c Candidate) {
		if c == nil {
			candidateGatheredFunc()
			return
		}
		t.Log(c.NetworkType(), c.Priority(), c)
	}))
	assert.NoError(a.GatherCandidates())

	<-candidateGathered.Done()

	assert.NoError(a.Close())
	// Twice because of 2 STUN servers configured
	assert.Equal(numSTUNS, udpMuxSrflx.getXORMappedAddrUsedTimes, "Expected times that GetXORMappedAddr should be called")

	// One for Restart() when agent has been initialized and one time when Close() the agent
	assert.Equal(2, udpMuxSrflx.removeConnByUfragTimes, "Expected times that RemoveConnByUfrag should be called")

	// Twice because of 2 STUN servers configured
	assert.Equal(numSTUNS, udpMuxSrflx.getConnForURLTimes, "Expected times that GetConnForURL should be called")
}

type universalUDPMuxMock struct {
	UDPMux
	getXORMappedAddrUsedTimes int
	removeConnByUfragTimes    int
	getConnForURLTimes        int
	mu                        sync.Mutex
	conn                      *net.UDPConn
}

func (m *universalUDPMuxMock) GetRelayedAddr(net.Addr, time.Duration) (*net.Addr, error) {
	return nil, errNotImplemented
}

func (m *universalUDPMuxMock) GetConnForURL(string, string, net.Addr) (net.PacketConn, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getConnForURLTimes++
	return m.conn, nil
}

func (m *universalUDPMuxMock) GetXORMappedAddr(net.Addr, time.Duration) (*stun.XORMappedAddress, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getXORMappedAddrUsedTimes++
	return &stun.XORMappedAddress{IP: net.IP{100, 64, 0, 1}, Port: 77878}, nil
}

func (m *universalUDPMuxMock) RemoveConnByUfrag(string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeConnByUfragTimes++
}

func (m *universalUDPMuxMock) GetListenAddresses() []net.Addr {
	return []net.Addr{m.conn.LocalAddr()}
}
