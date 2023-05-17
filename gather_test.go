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

	a, err := NewAgent(&AgentConfig{})
	assert.NoError(err)

	localIPs, err := localInterfaces(a.net, a.interfaceFilter, a.ipFilter, []NetworkType{NetworkTypeUDP4}, false)
	assert.NotEqual(len(localIPs), 0, "localInterfaces found no interfaces, unable to test")
	assert.NoError(err)

	ip := localIPs[0]

	conn, err := listenUDPInPortRange(a.net, a.log, 0, 0, udp, &net.UDPAddr{IP: ip, Port: 0})
	assert.NoError(err, "listenUDPInPortRange error with no port restriction")
	assert.NotNil(conn, "listenUDPInPortRange error with no port restriction return a nil conn")

	_, err = listenUDPInPortRange(a.net, a.log, 4999, 5000, udp, &net.UDPAddr{IP: ip, Port: 0})
	assert.ErrorIs(err, ErrPort, "listenUDPInPortRange with invalid port range did not return ErrPort")

	conn, err = listenUDPInPortRange(a.net, a.log, 5000, 5000, udp, &net.UDPAddr{IP: ip, Port: 0})
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
		conn, err = listenUDPInPortRange(a.net, a.log, portMax, portMin, udp, &net.UDPAddr{IP: ip, Port: 0})
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

	_, err = listenUDPInPortRange(a.net, a.log, portMax, portMin, udp, &net.UDPAddr{IP: ip, Port: 0})
	assert.Equalf(err, ErrPort, "listenUDPInPortRange with port restriction [%d, %d], did not return ErrPort", portMin, portMax)

	assert.NoError(a.Close())
}

// Assert that srflx candidates can be gathered from TURN servers
//
// When TURN servers are utilized, both types of candidates
// (i.e. srflx and relay) are obtained from the TURN server.
//
// https://tools.ietf.org/html/rfc5245#section-2.1
func TestTURNSrflx(t *testing.T) {
	assert := assert.New(t)

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

	a, err := NewAgent(&AgentConfig{
		NetworkTypes:   supportedNetworkTypes(),
		Urls:           urls,
		CandidateTypes: []CandidateType{CandidateTypeServerReflexive, CandidateTypeRelay},
	})
	assert.NoError(err)

	candidateGathered, candidateGatheredFunc := context.WithCancel(context.Background())
	assert.NoError(a.OnCandidate(func(c Candidate) {
		if c != nil && c.Type() == CandidateTypeServerReflexive {
			candidateGatheredFunc()
		}
	}))

	assert.NoError(a.GatherCandidates())

	<-candidateGathered.Done()

	assert.NoError(a.Close())
	assert.NoError(server.Close())
}

func TestCloseConnLog(t *testing.T) {
	assert := assert.New(t)

	a, err := NewAgent(&AgentConfig{})
	assert.NoError(err)

	closeConnAndLog(nil, a.log, "normal nil")

	var nc *net.UDPConn
	closeConnAndLog(nc, a.log, "nil ptr")

	assert.NoError(a.Close())
}
