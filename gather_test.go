// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"context"
	"net"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/pion/stun"
	"github.com/pion/transport/v2/test"
	"github.com/pion/turn/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListenUDP(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	agent, err := NewAgent(&AgentConfig{})
	require.NoError(err)

	localIPs, err := localInterfaces(agent.net, agent.interfaceFilter, agent.ipFilter, []NetworkType{NetworkTypeUDP4}, false)
	assert.NotEqual(len(localIPs), 0, "localInterfaces found no interfaces, unable to test")
	assert.NoError(err)

	ip := localIPs[0]

	conn, err := listenUDPInPortRange(agent.net, agent.log, 0, 0, udp, &net.UDPAddr{IP: ip, Port: 0})
	assert.NoError(err, "listenUDPInPortRange error with no port restriction")
	assert.NotNil(conn, "listenUDPInPortRange error with no port restriction return a nil conn")

	_, err = listenUDPInPortRange(agent.net, agent.log, 4999, 5000, udp, &net.UDPAddr{IP: ip, Port: 0})
	assert.ErrorIs(err, ErrPort, "listenUDPInPortRange with invalid port range did not return ErrPort")

	conn, err = listenUDPInPortRange(agent.net, agent.log, 5000, 5000, udp, &net.UDPAddr{IP: ip, Port: 0})
	assert.NoError(err, "listenUDPInPortRange error with no port restriction")
	assert.NotNil(conn, "listenUDPInPortRange error with no port restriction return a nil conn")

	_, port, err := net.SplitHostPort(conn.LocalAddr().String())
	assert.NoError(err)
	assert.Equal(port, "5000", "listenUDPInPortRange with port restriction of 5000 listened on incorrect port")

	portMin := 5100
	portMax := 5109
	total := portMax - portMin + 1
	result := make([]int, 0, total)
	portRange := make([]int, 0, total)
	for i := 0; i < total; i++ {
		conn, err = listenUDPInPortRange(agent.net, agent.log, portMax, portMin, udp, &net.UDPAddr{IP: ip, Port: 0})
		assert.NoError(err, "listenUDPInPortRange error with no port restriction")
		assert.NotNil(conn, "listenUDPInPortRange error with no port restriction return a nil conn")

		_, port, err = net.SplitHostPort(conn.LocalAddr().String())
		require.NoError(err)

		p, _ := strconv.Atoi(port)
		require.Truef(p >= portMin && p <= portMax, "listenUDPInPortRange with port restriction [%d, %d] listened on incorrect port (%s)", portMin, portMax, port)

		result = append(result, p)
		portRange = append(portRange, portMin+i)
	}

	require.Falsef(sort.IntsAreSorted(result), "listenUDPInPortRange with port restriction [%d, %d], ports result should be random", portMin, portMax)
	assert.ElementsMatch(portRange, result)

	_, err = listenUDPInPortRange(agent.net, agent.log, portMax, portMin, udp, &net.UDPAddr{IP: ip, Port: 0})
	assert.Equalf(err, ErrPort, "listenUDPInPortRange with port restriction [%d, %d], did not return ErrPort", portMin, portMax)

	assert.NoError(agent.Close())
}

// Assert that srflx candidates can be gathered from TURN servers
//
// When TURN servers are utilized, both types of candidates
// (i.e. srflx and relay) are obtained from the TURN server.
//
// https://tools.ietf.org/html/rfc5245#section-2.1
func TestTURNSrflx(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 30).Stop()

	serverPort := randomPort(t)
	serverListener, err := net.ListenPacket("udp4", "127.0.0.1:"+strconv.Itoa(serverPort))
	assert.NoError(err)

	server, err := turn.NewServer(turn.ServerConfig{
		Realm:       "pion.ly",
		AuthHandler: optimisticAuthHandler,
		PacketConnConfigs: []turn.PacketConnConfig{
			{
				PacketConn:            serverListener,
				RelayAddressGenerator: &turn.RelayAddressGeneratorNone{Address: "127.0.0.1"},
			},
		},
	})
	assert.NoError(err)

	urls := []*stun.URI{{
		Scheme:   stun.SchemeTypeTURN,
		Proto:    stun.ProtoTypeUDP,
		Host:     "127.0.0.1",
		Port:     serverPort,
		Username: "username",
		Password: "password",
	}}

	agent, err := NewAgent(&AgentConfig{
		NetworkTypes:   supportedNetworkTypes(),
		Urls:           urls,
		CandidateTypes: []CandidateType{CandidateTypeServerReflexive, CandidateTypeRelay},
	})
	require.NoError(err)

	candidateGathered, candidateGatheredFunc := context.WithCancel(context.Background())
	assert.NoError(agent.OnCandidate(func(c Candidate) {
		if c != nil && c.Type() == CandidateTypeServerReflexive {
			candidateGatheredFunc()
		}
	}))

	assert.NoError(agent.GatherCandidates())

	<-candidateGathered.Done()

	assert.NoError(agent.Close())
	assert.NoError(server.Close())
}

func TestCloseConnLog(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	agent, err := NewAgent(&AgentConfig{})
	require.NoError(err)

	closeConnAndLog(nil, agent.log, "normal nil")

	var nc *net.UDPConn
	closeConnAndLog(nc, agent.log, "nil ptr")

	assert.NoError(agent.Close())
}
