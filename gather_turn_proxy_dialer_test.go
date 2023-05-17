// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"context"
	"io"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/pion/stun"
	"github.com/pion/transport/v2/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/proxy"
)

type mockProxy struct {
	proxyWasDialed func()
}

type mockConn struct{}

func (m *mockConn) Read([]byte) (n int, err error)   { return 0, io.EOF }
func (m *mockConn) Write([]byte) (int, error)        { return 0, io.EOF }
func (m *mockConn) Close() error                     { return io.EOF }
func (m *mockConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (m *mockConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (m *mockConn) SetDeadline(time.Time) error      { return io.EOF }
func (m *mockConn) SetReadDeadline(time.Time) error  { return io.EOF }
func (m *mockConn) SetWriteDeadline(time.Time) error { return io.EOF }

func (m *mockProxy) Dial(string, string) (net.Conn, error) {
	m.proxyWasDialed()
	return &mockConn{}, nil
}

func TestTURNProxyDialer(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 30).Stop()

	proxyWasDialed, proxyWasDialedFunc := context.WithCancel(context.Background())
	proxy.RegisterDialerType("tcp", func(*url.URL, proxy.Dialer) (proxy.Dialer, error) {
		return &mockProxy{proxyWasDialedFunc}, nil
	})

	tcpProxyURI, err := url.Parse("tcp://fakeproxy:3128")
	require.NoError(err)

	proxyDialer, err := proxy.FromURL(tcpProxyURI, proxy.Direct)
	require.NoError(err)

	agent, err := NewAgent(&AgentConfig{
		CandidateTypes: []CandidateType{CandidateTypeRelay},
		NetworkTypes:   supportedNetworkTypes(),
		Urls: []*stun.URI{
			{
				Scheme:   stun.SchemeTypeTURN,
				Host:     "127.0.0.1",
				Username: "username",
				Password: "password",
				Proto:    stun.ProtoTypeTCP,
				Port:     5000,
			},
		},
		ProxyDialer: proxyDialer,
	})
	require.NoError(err)

	candidateGatherFinish, candidateGatherFinishFunc := context.WithCancel(context.Background())
	assert.NoError(agent.OnCandidate(func(c Candidate) {
		if c == nil {
			candidateGatherFinishFunc()
		}
	}))

	assert.NoError(agent.GatherCandidates())
	<-candidateGatherFinish.Done()
	<-proxyWasDialed.Done()

	assert.NoError(agent.Close())
}

// Assert that candidates are given for each mux in a MultiTCPMux
func TestMultiTCPMuxUsage(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 30).Stop()

	var expectedPorts []int
	var tcpMuxInstances []TCPMux
	for i := 0; i < 3; i++ {
		port := randomPort(t)
		listener, err := net.ListenTCP("tcp", &net.TCPAddr{
			IP:   net.IP{127, 0, 0, 1},
			Port: port,
		})
		assert.NoError(err)
		defer func() {
			_ = listener.Close()
		}()

		expectedPorts = append(expectedPorts, port)
		tcpMuxInstances = append(tcpMuxInstances, NewTCPMuxDefault(TCPMuxParams{
			Listener:       listener,
			ReadBufferSize: 8,
		}))
	}

	agent, err := NewAgent(&AgentConfig{
		NetworkTypes:   supportedNetworkTypes(),
		CandidateTypes: []CandidateType{CandidateTypeHost},
		TCPMux:         NewMultiTCPMuxDefault(tcpMuxInstances...),
	})
	require.NoError(err)

	candidateCh := make(chan Candidate)
	assert.NoError(agent.OnCandidate(func(c Candidate) {
		if c == nil {
			close(candidateCh)
			return
		}
		candidateCh <- c
	}))
	assert.NoError(agent.GatherCandidates())

	portFound := make(map[int]bool)
	for c := range candidateCh {
		activeCandidate := c.Port() == 0
		if c.NetworkType().IsTCP() && !activeCandidate {
			portFound[c.Port()] = true
		}
	}
	assert.Len(portFound, len(expectedPorts))
	for _, port := range expectedPorts {
		assert.True(portFound[port], "There should be a candidate for each TCP mux port")
	}

	assert.NoError(agent.Close())
}
