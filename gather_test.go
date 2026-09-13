// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package ice

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/pion/dtls/v3"
	"github.com/pion/dtls/v3/pkg/crypto/selfsign"
	"github.com/pion/logging"
	"github.com/pion/stun/v4"
	transport "github.com/pion/transport/v4"
	"github.com/pion/transport/v4/test"
	"github.com/pion/turn/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/proxy"
)

func skipOnPermission(t *testing.T, err error, action string) {
	t.Helper()

	if err == nil {
		return
	}

	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) ||
		strings.Contains(err.Error(), "permission denied") ||
		strings.Contains(err.Error(), "operation not permitted") {
		t.Skipf("skipping %s: %v", action, err)
	}
}

func TestConfiguredNetworkTypes(t *testing.T) {
	t.Run("empty returns supported network types", func(t *testing.T) {
		got := configuredNetworkTypes(nil)
		require.Equal(t, supportedNetworkTypes(), got)
	})

	t.Run("non-empty returns configured values", func(t *testing.T) {
		expected := []NetworkType{NetworkTypeUDP4, NetworkTypeTCP6}
		got := configuredNetworkTypes(expected)
		require.Equal(t, expected, got)
	})
}

func TestEffectiveURLProtoType(t *testing.T) {
	tests := []struct {
		name     string
		url      stun.URI
		expected stun.ProtoType
	}{
		{name: "stun defaults to udp", url: stun.URI{Scheme: stun.SchemeTypeSTUN, Proto: stun.ProtoTypeUnknown}, expected: stun.ProtoTypeUDP},
		{name: "turn defaults to udp", url: stun.URI{Scheme: stun.SchemeTypeTURN, Proto: stun.ProtoTypeUnknown}, expected: stun.ProtoTypeUDP},
		{name: "stuns defaults to tcp", url: stun.URI{Scheme: stun.SchemeTypeSTUNS, Proto: stun.ProtoTypeUnknown}, expected: stun.ProtoTypeTCP},
		{name: "turns defaults to tcp", url: stun.URI{Scheme: stun.SchemeTypeTURNS, Proto: stun.ProtoTypeUnknown}, expected: stun.ProtoTypeTCP},
		{name: "unknown remains unknown", url: stun.URI{Scheme: stun.SchemeTypeUnknown, Proto: stun.ProtoTypeUnknown}, expected: stun.ProtoTypeUnknown},
		{name: "explicit proto overrides scheme default", url: stun.URI{Scheme: stun.SchemeTypeTURNS, Proto: stun.ProtoTypeUDP}, expected: stun.ProtoTypeUDP},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, effectiveURLProtoType(tc.url))
		})
	}
}

func TestListenUDP(t *testing.T) {
	agent, err := NewAgent(&AgentConfig{})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	_, localAddrs, err := localInterfaces(agent.net, agent.interfaceFilter, agent.ipFilter, []NetworkType{NetworkTypeUDP4}, false)
	require.NotEqual(t, len(localAddrs), 0, "localInterfaces found no interfaces, unable to test")
	require.NoError(t, err)

	ip := localAddrs[0].addr.AsSlice()

	conn, err := listenUDPInPortRange(agent.net, agent.log, 0, 0, udp, &net.UDPAddr{IP: ip, Port: 0})
	require.NoError(t, err, "listenUDP error with no port restriction")
	require.NotNil(t, conn, "listenUDP error with no port restriction return a nil conn")

	_, err = listenUDPInPortRange(agent.net, agent.log, 4999, 5000, udp, &net.UDPAddr{IP: ip, Port: 0})
	require.Equal(t, err, ErrPort, "listenUDP with invalid port range did not return ErrPort")

	conn, err = listenUDPInPortRange(agent.net, agent.log, 5000, 5000, udp, &net.UDPAddr{IP: ip, Port: 0})
	require.NoError(t, err, "listenUDP error with no port restriction")
	require.NotNil(t, conn, "listenUDP error with no port restriction return a nil conn")

	_, port, err := net.SplitHostPort(conn.LocalAddr().String())
	require.NoError(t, err)
	require.Equal(t, port, "5000", "listenUDP with port restriction of 5000 listened on incorrect port")

	portMin := 5100
	portMax := 5109
	total := portMax - portMin + 1
	result := make([]int, 0, total)
	portRange := make([]int, 0, total)
	for i := range total {
		conn, err = listenUDPInPortRange(agent.net, agent.log, portMax, portMin, udp, &net.UDPAddr{IP: ip, Port: 0})
		require.NoError(t, err, "listenUDP error with no port restriction")
		require.NotNil(t, conn, "listenUDP error with no port restriction return a nil conn")

		_, port, err = net.SplitHostPort(conn.LocalAddr().String())
		require.NoError(t, err)

		p, _ := strconv.Atoi(port)
		require.False(t, p < portMin || p > portMax)
		result = append(result, p)
		portRange = append(portRange, portMin+i)
	}
	require.False(t, sort.IntsAreSorted(result))
	sort.Ints(result)
	require.Equal(t, result, portRange)
	_, err = listenUDPInPortRange(agent.net, agent.log, portMax, portMin, udp, &net.UDPAddr{IP: ip, Port: 0})
	require.Equal(t, err, ErrPort, "listenUDP with port restriction [%d, %d], did not return ErrPort", portMin, portMax)
}

func TestGatherConcurrency(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	agent, err := NewAgent(&AgentConfig{NetworkTypes: []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6}, IncludeLoopback: true})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	candidateGathered, candidateGatheredFunc := context.WithCancel(context.Background())
	require.NoError(t, agent.OnCandidate(func(Candidate) {
		candidateGatheredFunc()
	}))

	// Testing for panic
	for range 10 {
		_ = agent.GatherCandidates()
	}

	<-candidateGathered.Done()
}

// TestAgentRestartThenGatherRepeatedly guards a gathering-state race where
// restarting mid-gather leaves gatheringState wedged, so subsequent
// GatherCandidates calls fail with ErrMultipleGatherAttempted.
func TestAgentRestartThenGatherRepeatedly(t *testing.T) {
	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 30).Stop()

	agent, err := NewAgent(&AgentConfig{})
	require.NoError(t, err)
	defer func() { require.NoError(t, agent.Close()) }()

	require.NoError(t, agent.OnCandidate(func(Candidate) {}))
	require.NoError(t, agent.GatherCandidates())

	// Restart immediately, before the previous cycle has settled, many times.
	const restarts = 300
	for range restarts {
		require.NoError(t, agent.Restart("", ""))
		require.NoError(t, agent.GatherCandidates())
	}
}

func TestCompleteGatheringIgnoresOldGeneration(t *testing.T) {
	agent, err := NewAgent(&AgentConfig{})
	require.NoError(t, err)
	defer func() { require.NoError(t, agent.Close()) }()

	completed := make(chan struct{}, 1)
	require.NoError(t, agent.OnCandidate(func(candidate Candidate) {
		if candidate == nil {
			completed <- struct{}{}
		}
	}))

	require.NoError(t, agent.loop.Run(agent.loop, func(context.Context) {
		agent.gatherGeneration = 2
		agent.gatheringState = GatheringStateGathering
	}))
	require.NoError(t, agent.completeGathering(1))

	state, err := agent.GetGatheringState()
	require.NoError(t, err)
	require.Equal(t, GatheringStateGathering, state)
	require.Never(t, func() bool {
		select {
		case <-completed:
			return true
		default:
			return false
		}
	}, 50*time.Millisecond, time.Millisecond)
}

func TestContinualRegatherKeepsGeneration(t *testing.T) {
	agent, err := NewAgentWithOptions(WithNet(newHostGatherNet(nil)), WithNetworkTypes([]NetworkType{NetworkTypeUDP4}), WithCandidateTypes([]CandidateType{CandidateTypeHost}), WithMulticastDNSMode(MulticastDNSModeDisabled), WithNetworkMonitorInterval(time.Millisecond), WithIncludeLoopback())
	require.NoError(t, err)
	defer func() { require.NoError(t, agent.Close()) }()

	candidates := make(chan Candidate, 1)
	require.NoError(t, agent.OnCandidate(func(candidate Candidate) {
		if candidate != nil {
			candidates <- candidate
		}
	}))

	generation := agent.gatherGeneration
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		agent.startNetworkMonitoring(ctx, generation, agent.localUfrag)
	}()

	var candidate Candidate
	select {
	case candidate = <-candidates:
	case <-time.After(time.Second):
		require.FailNow(t, "timed out waiting for continual regather")
	}
	cancel()
	<-done

	extension, ok := candidate.GetExtension("generation")
	require.True(t, ok)
	require.Equal(t, strconv.FormatUint(generation, 10), extension.Value)
	require.Equal(t, generation, agent.gatherGeneration)
}

func TestLoopbackCandidate(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()
	type testCase struct {
		name        string
		agentConfig *AgentConfig
		loExpected  bool
	}
	mux, err := NewMultiUDPMuxFromPort(12500)
	require.NoError(t, err)
	muxWithLo, errlo := NewMultiUDPMuxFromPort(12501, UDPMuxFromPortWithLoopback())
	require.NoError(t, errlo)

	unspecConn, errconn := net.ListenPacket("udp", ":0") // nolint: noctx
	require.NoError(t, errconn)
	defer func() {
		_ = unspecConn.Close()
	}()
	muxUnspecDefault := NewUDPMuxDefault(UDPMuxParams{UDPConn: unspecConn})

	testCases := []testCase{
		{name: "mux should not have loopback candidate", agentConfig: &AgentConfig{NetworkTypes: []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6}, UDPMux: mux}, loExpected: false},
		{name: "mux with loopback should not have loopback candidate", agentConfig: &AgentConfig{NetworkTypes: []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6}, UDPMux: muxWithLo}, loExpected: true},
		{name: "UDPMuxDefault with unspecified IP should not have loopback candidate", agentConfig: &AgentConfig{NetworkTypes: []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6}, UDPMux: muxUnspecDefault}, loExpected: false},
		{name: "UDPMuxDefault with unspecified IP should respect agent includeloopback", agentConfig: &AgentConfig{NetworkTypes: []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6}, UDPMux: muxUnspecDefault, IncludeLoopback: true}, loExpected: true},
		{name: "includeloopback enabled", agentConfig: &AgentConfig{NetworkTypes: []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6}, IncludeLoopback: true}, loExpected: true},
		{name: "includeloopback disabled", agentConfig: &AgentConfig{NetworkTypes: []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6}, IncludeLoopback: false}, loExpected: false},
	}

	for _, tc := range testCases {
		tcase := tc
		t.Run(tcase.name, func(t *testing.T) {
			agent, err := NewAgent(tc.agentConfig)
			require.NoError(t, err)
			defer func() {
				require.NoError(t, agent.Close())
			}()

			candidateGathered, candidateGatheredFunc := context.WithCancel(context.Background())
			var loopback atomic.Int32
			require.NoError(t, agent.OnCandidate(func(c Candidate) {
				if c != nil {
					if net.ParseIP(c.Address()).IsLoopback() {
						loopback.Store(1)
					}
				} else {
					candidateGatheredFunc()

					return
				}
				t.Log(c.NetworkType(), c.Priority(), c)
			}))
			require.NoError(t, agent.GatherCandidates())

			<-candidateGathered.Done()

			require.Equal(t, tcase.loExpected, loopback.Load() == 1)
		})
	}

	require.NoError(t, mux.Close())
	require.NoError(t, muxWithLo.Close())
	require.NoError(t, muxUnspecDefault.Close())
}

// Assert that STUN gathering is done concurrently.
func TestSTUNConcurrency(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	serverListener, err := net.ListenPacket("udp4", localhostIPStr+":0") // nolint: noctx
	require.NoError(t, err)
	serverPort := portFromAddr(t, serverListener.LocalAddr())

	server, err := turn.NewServer(turn.ServerConfig{Realm: "pion.ly", AuthHandler: optimisticAuthHandler, PacketConnConfigs: []turn.PacketConnConfig{{PacketConn: serverListener, RelayAddressGenerator: &turn.RelayAddressGeneratorNone{Address: localhostIPStr}}}})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, server.Close())
	}()

	urls := []*stun.URI{}
	for i := 0; i <= 10; i++ {
		urls = append(urls, &stun.URI{Scheme: stun.SchemeTypeSTUN, Host: localhostIPStr, Port: serverPort + 1})
	}
	urls = append(urls, &stun.URI{Scheme: stun.SchemeTypeSTUN, Host: localhostIPStr, Port: serverPort})

	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IP{127, 0, 0, 1}})
	require.NoError(t, err)
	defer func() {
		_ = listener.Close()
	}()

	tcpMux := NewTCPMuxDefault(TCPMuxParams{Listener: listener, Logger: logging.NewDefaultLoggerFactory().NewLogger("ice"), ReadBufferSize: 8})
	defer func() {
		_ = tcpMux.Close()
	}()

	agent, err := NewAgent(&AgentConfig{NetworkTypes: supportedNetworkTypes(), Urls: urls, CandidateTypes: []CandidateType{CandidateTypeHost, CandidateTypeServerReflexive}, TCPMux: tcpMux})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	candidateGathered, candidateGatheredFunc := context.WithCancel(context.Background())
	require.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c == nil {
			candidateGatheredFunc()

			return
		}
		t.Log(c.NetworkType(), c.Priority(), c)
	}))
	require.NoError(t, agent.GatherCandidates())

	<-candidateGathered.Done()
}

// Assert that TURN gathering is done concurrently.
func TestTURNConcurrency(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	runTest := func(
		protocol stun.ProtoType,
		scheme stun.SchemeType,
		packetConn net.PacketConn,
		listener net.Listener,
		serverPort int,
	) {
		packetConnConfigs := []turn.PacketConnConfig{}
		if packetConn != nil {
			packetConnConfigs = append(packetConnConfigs, turn.PacketConnConfig{PacketConn: packetConn, RelayAddressGenerator: &turn.RelayAddressGeneratorNone{Address: localhostIPStr}})
		}

		listenerConfigs := []turn.ListenerConfig{}
		if listener != nil {
			listenerConfigs = append(listenerConfigs, turn.ListenerConfig{Listener: listener, RelayAddressGenerator: &turn.RelayAddressGeneratorNone{Address: localhostIPStr}})
		}

		server, err := turn.NewServer(turn.ServerConfig{Realm: "pion.ly", AuthHandler: optimisticAuthHandler, PacketConnConfigs: packetConnConfigs, ListenerConfigs: listenerConfigs})
		require.NoError(t, err)
		defer func() {
			require.NoError(t, server.Close())
		}()

		urls := []*stun.URI{}
		// avoid long delay on unreachable ports on Windows
		if runtime.GOOS != "windows" {
			for i := 0; i <= 10; i++ {
				urls = append(urls, &stun.URI{Scheme: scheme, Host: localhostIPStr, Username: "username", Password: "password", Proto: protocol, Port: serverPort + 1 + i})
			}
		}
		urls = append(urls, &stun.URI{Scheme: scheme, Host: localhostIPStr, Username: "username", Password: "password", Proto: protocol, Port: serverPort})

		agent, err := NewAgent(&AgentConfig{CandidateTypes: []CandidateType{CandidateTypeRelay}, InsecureSkipVerify: true, NetworkTypes: supportedNetworkTypes(), Urls: urls})
		require.NoError(t, err)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		candidateGathered, candidateGatheredFunc := context.WithCancel(context.Background())
		require.NoError(t, agent.OnCandidate(func(c Candidate) {
			if c != nil {
				candidateGatheredFunc()
			}
		}))
		require.NoError(t, agent.GatherCandidates())

		<-candidateGathered.Done()
	}

	t.Run("UDP Relay", func(t *testing.T) {
		serverListener, err := net.ListenPacket("udp", localhostIPStr+":0") // nolint: noctx
		require.NoError(t, err)
		serverPort := portFromAddr(t, serverListener.LocalAddr())

		runTest(stun.ProtoTypeUDP, stun.SchemeTypeTURN, serverListener, nil, serverPort)
	})

	t.Run("TCP Relay", func(t *testing.T) {
		serverListener, err := net.Listen("tcp", localhostIPStr+":0") // nolint: noctx
		require.NoError(t, err)
		serverPort := portFromAddr(t, serverListener.Addr())

		runTest(stun.ProtoTypeTCP, stun.SchemeTypeTURN, nil, serverListener, serverPort)
	})

	t.Run("TLS Relay", func(t *testing.T) {
		certificate, genErr := selfsign.GenerateSelfSigned()
		require.NoError(t, genErr)

		serverListener, err := tls.Listen("tcp", localhostIPStr+":0", &tls.Config{ //nolint:gosec
			Certificates: []tls.Certificate{certificate},
		})
		require.NoError(t, err)
		serverPort := portFromAddr(t, serverListener.Addr())

		runTest(stun.ProtoTypeTCP, stun.SchemeTypeTURNS, nil, serverListener, serverPort)
	})

	t.Run("DTLS Relay", func(t *testing.T) {
		certificate, genErr := selfsign.GenerateSelfSigned()
		require.NoError(t, genErr)

		serverListener, err := dtls.ListenWithOptions("udp", &net.UDPAddr{IP: net.ParseIP(localhostIPStr), Port: 0}, dtls.WithCertificates(certificate))
		require.NoError(t, err)
		serverPort := portFromAddr(t, serverListener.Addr())

		runTest(stun.ProtoTypeUDP, stun.SchemeTypeTURNS, nil, serverListener, serverPort)
	})
}

// Assert that STUN and TURN gathering are done concurrently.
func TestSTUNTURNConcurrency(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 8).Stop()

	serverListener, err := net.ListenPacket("udp4", localhostIPStr+":0") // nolint: noctx
	require.NoError(t, err)
	serverPort := portFromAddr(t, serverListener.LocalAddr())

	server, err := turn.NewServer(turn.ServerConfig{Realm: "pion.ly", AuthHandler: optimisticAuthHandler, PacketConnConfigs: []turn.PacketConnConfig{{PacketConn: serverListener, RelayAddressGenerator: &turn.RelayAddressGeneratorNone{Address: localhostIPStr}}}})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, server.Close())
	}()

	urls := []*stun.URI{}
	for i := 0; i <= 10; i++ {
		urls = append(urls, &stun.URI{Scheme: stun.SchemeTypeSTUN, Host: localhostIPStr, Port: serverPort + 1})
	}
	urls = append(urls, &stun.URI{Scheme: stun.SchemeTypeTURN, Proto: stun.ProtoTypeUDP, Host: localhostIPStr, Port: serverPort, Username: "username", Password: "password"})

	agent, err := NewAgent(&AgentConfig{NetworkTypes: supportedNetworkTypes(), Urls: urls, CandidateTypes: []CandidateType{CandidateTypeServerReflexive, CandidateTypeRelay}})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	{
		// As TURN and STUN should be checked in parallel, this should complete before the default STUN timeout (5s)
		gatherLim := test.TimeOut(time.Second * 3)
		candidateGathered, candidateGatheredFunc := context.WithCancel(context.Background())
		require.NoError(t, agent.OnCandidate(func(c Candidate) {
			if c != nil {
				candidateGatheredFunc()
			}
		}))
		require.NoError(t, agent.GatherCandidates())

		<-candidateGathered.Done()
		gatherLim.Stop()
	}
}

// Assert that srflx candidates can be gathered from TURN servers
//
// When TURN servers are utilized, both types of candidates
// (i.e. srflx and relay) are obtained from the TURN server.
//
// https://tools.ietf.org/html/rfc5245#section-2.1
func TestTURNSrflx(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	serverListener, err := net.ListenPacket("udp4", localhostIPStr+":0") // nolint: noctx
	require.NoError(t, err)
	serverPort := portFromAddr(t, serverListener.LocalAddr())

	server, err := turn.NewServer(turn.ServerConfig{Realm: "pion.ly", AuthHandler: optimisticAuthHandler, PacketConnConfigs: []turn.PacketConnConfig{{PacketConn: serverListener, RelayAddressGenerator: &turn.RelayAddressGeneratorNone{Address: localhostIPStr}}}})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, server.Close())
	}()

	urls := []*stun.URI{{Scheme: stun.SchemeTypeTURN, Proto: stun.ProtoTypeUDP, Host: localhostIPStr, Port: serverPort, Username: "username", Password: "password"}}

	agent, err := NewAgent(&AgentConfig{NetworkTypes: supportedNetworkTypes(), Urls: urls, CandidateTypes: []CandidateType{CandidateTypeServerReflexive, CandidateTypeRelay}})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	candidateGathered, candidateGatheredFunc := context.WithCancel(context.Background())
	require.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c != nil && c.Type() == CandidateTypeServerReflexive {
			candidateGatheredFunc()
		}
	}))

	require.NoError(t, agent.GatherCandidates())

	<-candidateGathered.Done()
}

func TestGatherCandidatesRelayProducesRelay(t *testing.T) {
	for _, network := range []NetworkType{NetworkTypeUDP4, NetworkTypeTCP4, NetworkTypeUDP6, NetworkTypeTCP6} {
		t.Run(network.String(), func(t *testing.T) {
			defer test.CheckRoutines(t)()
			host := "127.0.0.1"
			relayNetwork := NetworkTypeUDP4
			if network.IsIPv6() {
				host = "::1"
				relayNetwork = NetworkTypeUDP6
			}
			bindAddr := net.JoinHostPort(host, "0")
			// Vnet does not support IPv6; exercise real loopback sockets.
			peer, err := net.ListenPacket(relayNetwork.String(), bindAddr) //nolint:noctx
			skipOnPermission(t, err, "listening on loopback")
			if network.IsIPv6() && (errors.Is(err, syscall.EAFNOSUPPORT) ||
				errors.Is(err, syscall.EADDRNOTAVAIL) || errors.Is(err, syscall.EPROTONOSUPPORT)) {
				t.Skipf("IPv6 loopback unavailable: %v", err)
			}
			require.NoError(t, err)
			defer peer.Close() //nolint:errcheck

			conf := turn.ServerConfig{Realm: "pion.ly", AuthHandler: optimisticAuthHandler, StrictAddressFamily: true}
			generator := &turn.RelayAddressGeneratorNone{Address: host}
			var addr net.Addr
			proto := stun.ProtoTypeUDP
			if network.IsUDP() {
				listener, listenErr := net.ListenPacket(network.String(), bindAddr) //nolint:noctx
				require.NoError(t, listenErr)
				defer listener.Close() //nolint:errcheck
				addr = listener.LocalAddr()
				conf.PacketConnConfigs = []turn.PacketConnConfig{{PacketConn: listener, RelayAddressGenerator: generator}}
			} else {
				listener, listenErr := net.Listen(network.String(), bindAddr) //nolint:noctx
				require.NoError(t, listenErr)
				defer listener.Close() //nolint:errcheck
				addr = listener.Addr()
				proto = stun.ProtoTypeTCP
				conf.ListenerConfigs = []turn.ListenerConfig{{Listener: listener, RelayAddressGenerator: generator}}
			}
			server, err := turn.NewServer(conf)
			require.NoError(t, err)
			defer func() { require.NoError(t, server.Close()) }()

			agent, err := NewAgentWithOptions(
				WithNetworkTypes([]NetworkType{relayNetwork}),
				WithTURNTransportProtocols([]NetworkType{network}),
				WithCandidateTypes([]CandidateType{CandidateTypeRelay}),
				WithMulticastDNSMode(MulticastDNSModeDisabled),
				WithUrls([]*stun.URI{{Scheme: stun.SchemeTypeTURN, Host: host, Port: portFromAddr(t, addr), Proto: proto, Username: "username", Password: "password"}}),
			)
			require.NoError(t, err)
			defer func() { require.NoError(t, agent.Close()) }()
			candidates := gatherAndCollectCandidates(t, agent)
			require.Len(t, candidates, 1)
			candidate := candidates[0]
			require.Equal(t, CandidateTypeRelay, candidate.Type())
			require.Equal(t, relayNetwork, candidate.NetworkType())
			require.Equal(t, host, candidate.Address())
			require.Positive(t, candidate.Port())

			relay, ok := candidate.(*CandidateRelay)
			require.True(t, ok)
			require.Equal(t, proto.String(), relay.RelayProtocol())
			payload := []byte("TURN relay")
			_, err = relay.conn.WriteTo(payload, peer.LocalAddr())
			require.NoError(t, err)
			require.NoError(t, peer.SetReadDeadline(time.Now().Add(5*time.Second)))
			buffer := make([]byte, 1500)
			n, source, err := peer.ReadFrom(buffer)
			require.NoError(t, err)
			require.Equal(t, payload, buffer[:n])
			require.Equal(t, net.JoinHostPort(candidate.Address(), strconv.Itoa(candidate.Port())), source.String())
		})
	}
}

type relayGatherNet struct {
	addr           *net.UDPAddr
	resolveUDPAddr func(string, string) (*net.UDPAddr, error)
}

type unresolvableRelayGatherNet struct {
	*relayGatherNet
	resolveUDPCalls atomic.Int32
}

func (n *unresolvableRelayGatherNet) ResolveUDPAddr(string, string) (*net.UDPAddr, error) {
	n.resolveUDPCalls.Add(1)

	return nil, errors.New("DNS unavailable") //nolint:err113 // test
}

func newRelayGatherNet(addr *net.UDPAddr) *relayGatherNet {
	if addr == nil {
		addr = &net.UDPAddr{IP: net.IPv4(10, 0, 0, 1)}
	}

	return &relayGatherNet{addr: addr}
}

func (n *relayGatherNet) ListenPacket(string, string) (net.PacketConn, error) {
	return newStubPacketConn(n.addr), nil
}

func (n *relayGatherNet) ListenUDP(string, *net.UDPAddr) (transport.UDPConn, error) {
	return nil, transport.ErrNotSupported
}

func (n *relayGatherNet) ListenTCP(string, *net.TCPAddr) (transport.TCPListener, error) {
	return nil, transport.ErrNotSupported
}

func (n *relayGatherNet) Dial(string, string) (net.Conn, error) {
	return nil, transport.ErrNotSupported
}

func (n *relayGatherNet) DialUDP(string, *net.UDPAddr, *net.UDPAddr) (transport.UDPConn, error) {
	return nil, transport.ErrNotSupported
}

func (n *relayGatherNet) DialTCP(string, *net.TCPAddr, *net.TCPAddr) (transport.TCPConn, error) {
	return nil, transport.ErrNotSupported
}

func (n *relayGatherNet) ResolveIPAddr(network, address string) (*net.IPAddr, error) {
	return net.ResolveIPAddr(network, address)
}

func (n *relayGatherNet) ResolveUDPAddr(network, address string) (*net.UDPAddr, error) {
	if n.resolveUDPAddr != nil {
		return n.resolveUDPAddr(network, address)
	}

	return net.ResolveUDPAddr(network, address)
}

func (n *relayGatherNet) ResolveTCPAddr(network, address string) (*net.TCPAddr, error) {
	return net.ResolveTCPAddr(network, address)
}

func (n *relayGatherNet) Interfaces() ([]*transport.Interface, error) {
	iface := transport.NewInterface(net.Interface{Index: 1, MTU: 1500, Name: "relaytest0", Flags: net.FlagUp})
	maskBits := 32
	prefixBits := 24
	if n.addr.IP.To4() == nil {
		maskBits = 128
		prefixBits = 64
	}
	iface.AddAddress(&net.IPNet{IP: n.addr.IP, Mask: net.CIDRMask(prefixBits, maskBits)})

	return []*transport.Interface{iface}, nil
}

func (n *relayGatherNet) InterfaceByIndex(index int) (*transport.Interface, error) {
	ifaces, err := n.Interfaces()
	if err != nil {
		return nil, err
	}

	for _, iface := range ifaces {
		if iface.Index == index {
			return iface, nil
		}
	}

	return nil, transport.ErrInterfaceNotFound
}

func (n *relayGatherNet) InterfaceByName(name string) (*transport.Interface, error) {
	ifaces, err := n.Interfaces()
	if err != nil {
		return nil, err
	}

	for _, iface := range ifaces {
		if iface.Name == name {
			return iface, nil
		}
	}

	return nil, transport.ErrInterfaceNotFound
}

func (n *relayGatherNet) CreateDialer(*net.Dialer) transport.Dialer {
	return nil
}

func (n *relayGatherNet) CreateListenConfig(*net.ListenConfig) transport.ListenConfig {
	return nil
}

type relayListenCaptureNet struct {
	listenPacketAddresses []string
	mu                    sync.Mutex
}

func newRelayListenCaptureNet() *relayListenCaptureNet {
	return &relayListenCaptureNet{}
}

func (n *relayListenCaptureNet) ListenPacket(_ string, address string) (net.PacketConn, error) {
	n.mu.Lock()
	n.listenPacketAddresses = append(n.listenPacketAddresses, address)
	n.mu.Unlock()

	udpAddr, err := net.ResolveUDPAddr("udp4", address)
	if err != nil {
		return nil, err
	}

	return newStubPacketConn(udpAddr), nil
}

func (n *relayListenCaptureNet) ListenUDP(string, *net.UDPAddr) (transport.UDPConn, error) {
	return nil, transport.ErrNotSupported
}

func (n *relayListenCaptureNet) ListenTCP(string, *net.TCPAddr) (transport.TCPListener, error) {
	return nil, transport.ErrNotSupported
}

func (n *relayListenCaptureNet) Dial(string, string) (net.Conn, error) {
	return nil, transport.ErrNotSupported
}

func (n *relayListenCaptureNet) DialUDP(string, *net.UDPAddr, *net.UDPAddr) (transport.UDPConn, error) {
	return nil, transport.ErrNotSupported
}

func (n *relayListenCaptureNet) DialTCP(string, *net.TCPAddr, *net.TCPAddr) (transport.TCPConn, error) {
	return nil, transport.ErrNotSupported
}

func (n *relayListenCaptureNet) ResolveIPAddr(network, address string) (*net.IPAddr, error) {
	return net.ResolveIPAddr(network, address)
}

func (n *relayListenCaptureNet) ResolveUDPAddr(network, address string) (*net.UDPAddr, error) {
	return net.ResolveUDPAddr(network, address)
}

func (n *relayListenCaptureNet) ResolveTCPAddr(network, address string) (*net.TCPAddr, error) {
	return net.ResolveTCPAddr(network, address)
}

func (n *relayListenCaptureNet) Interfaces() ([]*transport.Interface, error) {
	iface0 := transport.NewInterface(net.Interface{Index: 1, MTU: 1500, Name: "eth0", Flags: net.FlagUp})
	iface0.AddAddress(&net.IPNet{IP: net.IPv4(127, 0, 0, 1), Mask: net.CIDRMask(8, 32)})

	iface1 := transport.NewInterface(net.Interface{Index: 2, MTU: 1500, Name: "wlan0", Flags: net.FlagUp})
	iface1.AddAddress(&net.IPNet{IP: net.IPv4(127, 0, 0, 2), Mask: net.CIDRMask(8, 32)})

	return []*transport.Interface{iface0, iface1}, nil
}

func (n *relayListenCaptureNet) InterfaceByIndex(index int) (*transport.Interface, error) {
	ifaces, err := n.Interfaces()
	if err != nil {
		return nil, err
	}

	for _, iface := range ifaces {
		if iface.Index == index {
			return iface, nil
		}
	}

	return nil, transport.ErrInterfaceNotFound
}

func (n *relayListenCaptureNet) InterfaceByName(name string) (*transport.Interface, error) {
	ifaces, err := n.Interfaces()
	if err != nil {
		return nil, err
	}

	for _, iface := range ifaces {
		if iface.Name == name {
			return iface, nil
		}
	}

	return nil, transport.ErrInterfaceNotFound
}

func (n *relayListenCaptureNet) CreateDialer(*net.Dialer) transport.Dialer {
	return nil
}

func (n *relayListenCaptureNet) CreateListenConfig(*net.ListenConfig) transport.ListenConfig {
	return nil
}

func (n *relayListenCaptureNet) listenAddresses() []string {
	n.mu.Lock()
	defer n.mu.Unlock()

	addrs := make([]string, len(n.listenPacketAddresses))
	copy(addrs, n.listenPacketAddresses)

	return addrs
}

type hostGatherNet struct {
	addr *net.UDPAddr
}

func newHostGatherNet(addr *net.UDPAddr) *hostGatherNet {
	if addr == nil {
		addr = &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}
	}

	return &hostGatherNet{addr: addr}
}

func (n *hostGatherNet) ListenPacket(string, string) (net.PacketConn, error) {
	return newStubPacketConn(n.addr), nil
}

func (n *hostGatherNet) ListenUDP(network string, laddr *net.UDPAddr) (transport.UDPConn, error) {
	if laddr == nil {
		laddr = n.addr
	}

	return net.ListenUDP(network, laddr) //nolint:wrapcheck
}

func (n *hostGatherNet) ListenTCP(string, *net.TCPAddr) (transport.TCPListener, error) {
	return nil, transport.ErrNotSupported
}

func (n *hostGatherNet) Dial(string, string) (net.Conn, error) {
	return nil, transport.ErrNotSupported
}

func (n *hostGatherNet) DialUDP(string, *net.UDPAddr, *net.UDPAddr) (transport.UDPConn, error) {
	return nil, transport.ErrNotSupported
}

func (n *hostGatherNet) DialTCP(string, *net.TCPAddr, *net.TCPAddr) (transport.TCPConn, error) {
	return nil, transport.ErrNotSupported
}

func (n *hostGatherNet) ResolveIPAddr(network, address string) (*net.IPAddr, error) {
	return net.ResolveIPAddr(network, address)
}

func (n *hostGatherNet) ResolveUDPAddr(network, address string) (*net.UDPAddr, error) {
	return net.ResolveUDPAddr(network, address)
}

func (n *hostGatherNet) ResolveTCPAddr(network, address string) (*net.TCPAddr, error) {
	return net.ResolveTCPAddr(network, address)
}

func (n *hostGatherNet) Interfaces() ([]*transport.Interface, error) {
	iface := transport.NewInterface(net.Interface{Index: 1, MTU: 1500, Name: "hosttest0", Flags: net.FlagUp})
	iface.AddAddress(&net.IPNet{IP: n.addr.IP, Mask: net.CIDRMask(24, 32)})

	return []*transport.Interface{iface}, nil
}

func (n *hostGatherNet) InterfaceByIndex(index int) (*transport.Interface, error) {
	ifaces, err := n.Interfaces()
	if err != nil {
		return nil, err
	}

	for _, iface := range ifaces {
		if iface.Index == index {
			return iface, nil
		}
	}

	return nil, transport.ErrInterfaceNotFound
}

func (n *hostGatherNet) InterfaceByName(name string) (*transport.Interface, error) {
	ifaces, err := n.Interfaces()
	if err != nil {
		return nil, err
	}

	for _, iface := range ifaces {
		if iface.Name == name {
			return iface, nil
		}
	}

	return nil, transport.ErrInterfaceNotFound
}

func (n *hostGatherNet) CreateDialer(*net.Dialer) transport.Dialer {
	return nil
}

func (n *hostGatherNet) CreateListenConfig(*net.ListenConfig) transport.ListenConfig {
	return nil
}

type srflxListenCaptureNet struct {
	listenCalls []*net.UDPAddr
	mu          sync.Mutex
}

func newSrflxListenCaptureNet() *srflxListenCaptureNet {
	return &srflxListenCaptureNet{}
}

func (n *srflxListenCaptureNet) ListenPacket(string, string) (net.PacketConn, error) {
	return newStubPacketConn(&net.UDPAddr{IP: net.IPv4zero, Port: 0}), nil
}

func (n *srflxListenCaptureNet) ListenUDP(network string, laddr *net.UDPAddr) (transport.UDPConn, error) {
	n.mu.Lock()
	if laddr != nil {
		n.listenCalls = append(n.listenCalls, &net.UDPAddr{IP: append(net.IP{}, laddr.IP...), Port: laddr.Port, Zone: laddr.Zone})
	} else {
		n.listenCalls = append(n.listenCalls, nil)
	}
	n.mu.Unlock()

	return net.ListenUDP(network, &net.UDPAddr{IP: net.IPv4zero, Port: 0}) //nolint:wrapcheck
}

func (n *srflxListenCaptureNet) ListenTCP(string, *net.TCPAddr) (transport.TCPListener, error) {
	return nil, transport.ErrNotSupported
}

func (n *srflxListenCaptureNet) Dial(string, string) (net.Conn, error) {
	return nil, transport.ErrNotSupported
}

func (n *srflxListenCaptureNet) DialUDP(string, *net.UDPAddr, *net.UDPAddr) (transport.UDPConn, error) {
	return nil, transport.ErrNotSupported
}

func (n *srflxListenCaptureNet) DialTCP(string, *net.TCPAddr, *net.TCPAddr) (transport.TCPConn, error) {
	return nil, transport.ErrNotSupported
}

func (n *srflxListenCaptureNet) ResolveIPAddr(network, address string) (*net.IPAddr, error) {
	return net.ResolveIPAddr(network, address)
}

func (n *srflxListenCaptureNet) ResolveUDPAddr(network, address string) (*net.UDPAddr, error) {
	return net.ResolveUDPAddr(network, address)
}

func (n *srflxListenCaptureNet) ResolveTCPAddr(network, address string) (*net.TCPAddr, error) {
	return net.ResolveTCPAddr(network, address)
}

func (n *srflxListenCaptureNet) Interfaces() ([]*transport.Interface, error) {
	iface0 := transport.NewInterface(net.Interface{Index: 1, MTU: 1500, Name: "eth0", Flags: net.FlagUp})
	iface0.AddAddress(&net.IPNet{IP: net.IPv4(127, 0, 0, 1), Mask: net.CIDRMask(8, 32)})

	iface1 := transport.NewInterface(net.Interface{Index: 2, MTU: 1500, Name: "wlan0", Flags: net.FlagUp})
	iface1.AddAddress(&net.IPNet{IP: net.IPv4(127, 0, 0, 2), Mask: net.CIDRMask(8, 32)})

	return []*transport.Interface{iface0, iface1}, nil
}

func (n *srflxListenCaptureNet) InterfaceByIndex(index int) (*transport.Interface, error) {
	ifaces, err := n.Interfaces()
	if err != nil {
		return nil, err
	}

	for _, iface := range ifaces {
		if iface.Index == index {
			return iface, nil
		}
	}

	return nil, transport.ErrInterfaceNotFound
}

func (n *srflxListenCaptureNet) InterfaceByName(name string) (*transport.Interface, error) {
	ifaces, err := n.Interfaces()
	if err != nil {
		return nil, err
	}

	for _, iface := range ifaces {
		if iface.Name == name {
			return iface, nil
		}
	}

	return nil, transport.ErrInterfaceNotFound
}

func (n *srflxListenCaptureNet) CreateDialer(*net.Dialer) transport.Dialer {
	return nil
}

func (n *srflxListenCaptureNet) CreateListenConfig(*net.ListenConfig) transport.ListenConfig {
	return nil
}

func (n *srflxListenCaptureNet) listenCallIPs() []string {
	n.mu.Lock()
	defer n.mu.Unlock()

	ipStrings := make([]string, 0, len(n.listenCalls))
	for _, addr := range n.listenCalls {
		if addr == nil || addr.IP == nil {
			ipStrings = append(ipStrings, "")

			continue
		}
		ipStrings = append(ipStrings, addr.IP.String())
	}

	return ipStrings
}

type errorPacketConn struct {
	addr   net.Addr
	closed bool
}

type testTCPPacketConn struct {
	addr *net.TCPAddr
}

func (c *testTCPPacketConn) ReadFrom([]byte) (int, net.Addr, error)    { return 0, c.addr, io.EOF }
func (c *testTCPPacketConn) WriteTo(p []byte, _ net.Addr) (int, error) { return len(p), nil }
func (c *testTCPPacketConn) Close() error                              { return nil }
func (c *testTCPPacketConn) LocalAddr() net.Addr                       { return c.addr }
func (c *testTCPPacketConn) SetDeadline(time.Time) error               { return nil }
func (c *testTCPPacketConn) SetReadDeadline(time.Time) error           { return nil }
func (c *testTCPPacketConn) SetWriteDeadline(time.Time) error          { return nil }

type boundTCPMux struct {
	localAddr net.Addr
}

func (m *boundTCPMux) Close() error { return nil }

func (m *boundTCPMux) GetConnByUfrag(_ string, _ bool, local net.IP) (net.PacketConn, error) {
	return &testTCPPacketConn{addr: &net.TCPAddr{IP: local, Port: 12345}}, nil
}

func (m *boundTCPMux) RemoveConnByUfrag(string) {}

func (m *boundTCPMux) LocalAddr() net.Addr {
	if m.localAddr != nil {
		return m.localAddr
	}

	return &net.TCPAddr{}
}

func (c *errorPacketConn) ReadFrom(_ []byte) (int, net.Addr, error) {
	return 0, c.addr, io.EOF
}

func (c *errorPacketConn) WriteTo(_ []byte, _ net.Addr) (int, error) {
	return 0, errors.New("write failure") //nolint:err113 // test
}

func (c *errorPacketConn) Close() error {
	c.closed = true

	return nil
}

func (c *errorPacketConn) LocalAddr() net.Addr              { return c.addr }
func (c *errorPacketConn) SetDeadline(time.Time) error      { return nil }
func (c *errorPacketConn) SetReadDeadline(time.Time) error  { return nil }
func (c *errorPacketConn) SetWriteDeadline(time.Time) error { return nil }

type errorTurnNet struct {
	pc net.PacketConn
}

func (n *errorTurnNet) ListenPacket(string, string) (net.PacketConn, error) { return n.pc, nil }
func (n *errorTurnNet) ListenUDP(string, *net.UDPAddr) (transport.UDPConn, error) {
	return nil, transport.ErrNotSupported
}

func (n *errorTurnNet) ListenTCP(string, *net.TCPAddr) (transport.TCPListener, error) {
	return nil, transport.ErrNotSupported
}

func (n *errorTurnNet) Dial(string, string) (net.Conn, error) { return nil, transport.ErrNotSupported }
func (n *errorTurnNet) DialUDP(string, *net.UDPAddr, *net.UDPAddr) (transport.UDPConn, error) {
	return nil, transport.ErrNotSupported
}

func (n *errorTurnNet) DialTCP(string, *net.TCPAddr, *net.TCPAddr) (transport.TCPConn, error) {
	return nil, transport.ErrNotSupported
}

func (n *errorTurnNet) ResolveIPAddr(network, address string) (*net.IPAddr, error) {
	return net.ResolveIPAddr(network, address)
}

func (n *errorTurnNet) ResolveUDPAddr(network, address string) (*net.UDPAddr, error) {
	return net.ResolveUDPAddr(network, address)
}

func (n *errorTurnNet) ResolveTCPAddr(network, address string) (*net.TCPAddr, error) {
	return net.ResolveTCPAddr(network, address)
}

func (n *errorTurnNet) Interfaces() ([]*transport.Interface, error) {
	iface := transport.NewInterface(net.Interface{Index: 1, MTU: 1500, Name: "errturn0", Flags: net.FlagUp})
	iface.AddAddress(&net.IPNet{IP: net.IPv4(127, 0, 0, 1), Mask: net.CIDRMask(8, 32)})

	return []*transport.Interface{iface}, nil
}

func (n *errorTurnNet) InterfaceByIndex(index int) (*transport.Interface, error) {
	ifaces, err := n.Interfaces()
	if err != nil {
		return nil, err
	}
	for _, iface := range ifaces {
		if iface.Index == index {
			return iface, nil
		}
	}

	return nil, transport.ErrInterfaceNotFound
}

func (n *errorTurnNet) InterfaceByName(name string) (*transport.Interface, error) {
	ifaces, err := n.Interfaces()
	if err != nil {
		return nil, err
	}
	for _, iface := range ifaces {
		if iface.Name == name {
			return iface, nil
		}
	}

	return nil, transport.ErrInterfaceNotFound
}

func (n *errorTurnNet) CreateDialer(*net.Dialer) transport.Dialer { return nil }

func (n *errorTurnNet) CreateListenConfig(*net.ListenConfig) transport.ListenConfig { return nil }

type stubTurnClient struct {
	listenCalled   bool
	allocateCalled bool
	closeCalled    bool
	cfgConn        net.PacketConn
	relayConn      net.PacketConn
}

func (s *stubTurnClient) Listen() error {
	s.listenCalled = true

	return nil
}

func (s *stubTurnClient) AllocateWithContext(context.Context) (net.PacketConn, error) {
	s.allocateCalled = true
	if s.relayConn == nil {
		s.relayConn = newStubPacketConn(&net.UDPAddr{IP: net.IP{203, 0, 113, 5}, Port: 5000})
	}

	return s.relayConn, nil
}

func (s *stubTurnClient) Close() {
	s.closeCalled = true
}

func TestGatherCandidatesRelayCallsAddRelayCandidates(t *testing.T) {
	defer test.CheckRoutines(t)()

	stubClient := &stubTurnClient{}
	locConn := newStubPacketConn(&net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 50000})
	stubClient.relayConn = locConn

	agent, err := NewAgentWithOptions(
		WithNet(newRelayGatherNet(&net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 50000})),
		WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
		WithTURNTransportProtocols([]NetworkType{NetworkTypeUDP4}),
		WithCandidateTypes([]CandidateType{CandidateTypeRelay}),
		WithAddressRewriteRules(AddressRewriteRule{External: []string{"198.51.100.77"}, Local: "10.0.0.1", Iface: "relaytest0", AsCandidateType: CandidateTypeRelay, Mode: AddressRewriteReplace}),
		WithUrls([]*stun.URI{{Scheme: stun.SchemeTypeTURN, Host: "127.0.0.1", Port: 3478, Username: "username", Password: "password", Proto: stun.ProtoTypeUDP}}),
		WithMulticastDNSMode(MulticastDNSModeDisabled),
	)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	agent.turnClientFactory = func(cfg *turn.ClientConfig) (turnClient, error) {
		stubClient.cfgConn = cfg.Conn

		return stubClient, nil
	}

	candCh := make(chan Candidate, 1)
	require.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c != nil && c.Type() == CandidateTypeRelay {
			candCh <- c
		}
	}))

	agent.gatherCandidatesRelay(context.Background(), agent.urls, agent.gatherGeneration)

	var cand Candidate
	select {
	case cand = <-candCh:
	case <-time.After(2 * time.Second):
		assert.Fail(t, "expected relay candidate")
	}

	require.Equal(t, CandidateTypeRelay, cand.Type())
	assert.Equal(t, "198.51.100.77", cand.Address())

	assert.True(t, stubClient.listenCalled)
	assert.True(t, stubClient.allocateCalled)

	relay, ok := cand.(*CandidateRelay)
	require.True(t, ok)
	require.NoError(t, relay.close())
	assert.True(t, stubClient.closeCalled)
	assert.True(t, locConn.closed)
}

func TestGatherCandidatesRelayRespectsInterfaceFilter(t *testing.T) {
	defer test.CheckRoutines(t)()

	netCapture := newRelayListenCaptureNet()
	stubClient := &stubTurnClient{}

	agent, err := NewAgentWithOptions(
		WithNet(netCapture),
		WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
		WithTURNTransportProtocols([]NetworkType{NetworkTypeUDP4}),
		WithCandidateTypes([]CandidateType{CandidateTypeRelay}),
		WithMulticastDNSMode(MulticastDNSModeDisabled),
		WithUrls([]*stun.URI{{Scheme: stun.SchemeTypeTURN, Host: "127.0.0.1", Port: 3478, Username: "username", Password: "password", Proto: stun.ProtoTypeUDP}}),
		WithInterfaceFilter(func(iface string) bool {
			return iface == "eth0" //nolint:goconst
		}),
		WithIncludeLoopback(),
	)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	agent.turnClientFactory = func(cfg *turn.ClientConfig) (turnClient, error) {
		stubClient.cfgConn = cfg.Conn

		return stubClient, nil
	}

	require.NoError(t, agent.OnCandidate(func(Candidate) {}))

	agent.gatherCandidatesRelay(context.Background(), agent.urls, agent.gatherGeneration)

	listenAddrs := netCapture.listenAddresses()
	require.NotEmpty(t, listenAddrs)
	for _, addr := range listenAddrs {
		require.Equal(t, "127.0.0.1:0", addr)
	}
}

func TestGatherCandidatesRelayRespectsNetworkTypeAndTransport(t *testing.T) { //nolint:cyclop
	defer test.CheckRoutines(t)()

	for _, transportType := range []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6} {
		for _, relayType := range []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6} {
			for _, candidateNetworks := range [][]NetworkType{nil, {NetworkTypeUDP4}, {NetworkTypeUDP6}} {
				name := fmt.Sprintf("transport=%s/relay=%s/candidates=%v", transportType, relayType, candidateNetworks)
				t.Run(name, func(t *testing.T) {
					transportIP := net.ParseIP("127.0.0.1")
					if transportType.IsIPv6() {
						transportIP = net.ParseIP("::1")
					}
					relayIP := net.ParseIP("192.0.2.1")
					if relayType.IsIPv6() {
						relayIP = net.ParseIP("2001:db8::1")
					}
					relayConn := newStubPacketConn(&net.UDPAddr{IP: relayIP, Port: 6000})
					client := &stubTurnClient{relayConn: relayConn}
					turnNet := newRelayGatherNet(&net.UDPAddr{IP: transportIP, Port: 50000})
					serverAddr := net.JoinHostPort(transportIP.String(), "3478")
					turnNet.resolveUDPAddr = func(network, address string) (*net.UDPAddr, error) {
						assert.Equal(t, transportType.String(), network)
						assert.Equal(t, "turn.test:3478", address)

						return net.ResolveUDPAddr(network, serverAddr)
					}
					agent, err := NewAgentWithOptions(
						WithNet(turnNet),
						WithNetworkTypes(candidateNetworks),
						WithTURNTransportProtocols([]NetworkType{transportType}),
						WithCandidateTypes([]CandidateType{CandidateTypeRelay}),
						WithMulticastDNSMode(MulticastDNSModeDisabled),
						WithUrls([]*stun.URI{{Scheme: stun.SchemeTypeTURN, Host: "turn.test", Port: 3478, Username: "username", Password: "password", Proto: stun.ProtoTypeUDP}}),
					)
					require.NoError(t, err)
					defer func() { require.NoError(t, agent.Close()) }()
					agent.turnClientFactory = func(cfg *turn.ClientConfig) (turnClient, error) {
						assert.Equal(t, serverAddr, cfg.TURNServerAddr)
						assert.Same(t, turnNet, cfg.Net)
						client.cfgConn = cfg.Conn

						return client, nil
					}
					require.NoError(t, agent.OnCandidate(func(Candidate) {}))
					agent.gatherCandidatesRelay(context.Background(), agent.urls, agent.gatherGeneration)
					require.True(t, client.allocateCalled, "TURN transport must remain independent of candidate family")
					candidates, err := agent.GetLocalCandidates()
					require.NoError(t, err)
					if len(candidateNetworks) == 0 || candidateNetworks[0] == relayType {
						require.Len(t, candidates, 1)
						require.Equal(t, relayType, candidates[0].NetworkType())
						require.Equal(t, transportIP.String(), candidates[0].RelatedAddress().Address)
						require.False(t, client.closeCalled)
					} else {
						require.Empty(t, candidates)
						require.True(t, client.closeCalled)
						require.True(t, relayConn.closed)
						controlConn, ok := client.cfgConn.(*stubPacketConn)
						require.True(t, ok)
						require.True(t, controlConn.closed)
					}
				})
			}
		}
	}

	t.Run("skips TCP transport URL when TURN transport protocols allow only UDP", func(t *testing.T) {
		stubClient := &stubTurnClient{}

		agent, err := NewAgentWithOptions(
			WithNet(newRelayGatherNet(&net.UDPAddr{IP: net.IPv4(10, 0, 0, 3), Port: 50000})),
			WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
			WithTURNTransportProtocols([]NetworkType{NetworkTypeUDP4}),
			WithCandidateTypes([]CandidateType{CandidateTypeRelay}),
			WithUrls([]*stun.URI{{Scheme: stun.SchemeTypeTURN, Host: "127.0.0.1", Port: 3478, Username: "username", Password: "password", Proto: stun.ProtoTypeTCP}}),
			WithMulticastDNSMode(MulticastDNSModeDisabled),
		)
		require.NoError(t, err)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		factoryCalls := 0
		agent.turnClientFactory = func(cfg *turn.ClientConfig) (turnClient, error) {
			factoryCalls++
			stubClient.cfgConn = cfg.Conn

			return stubClient, nil
		}

		candidateCh := make(chan Candidate, 1)
		require.NoError(t, agent.OnCandidate(func(c Candidate) {
			if c != nil {
				candidateCh <- c
			}
		}))

		agent.gatherCandidatesRelay(context.Background(), agent.urls, agent.gatherGeneration)

		select {
		case <-candidateCh:
			assert.Fail(t, "unexpected relay candidate for TCP TURN URL with UDP-only network types")
		case <-time.After(200 * time.Millisecond):
		}

		require.Equal(t, 0, factoryCalls)
	})
}

func TestGatherCandidatesRelayDefaultClientError(t *testing.T) {
	defer test.CheckRoutines(t)()

	errConn := &errorPacketConn{addr: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}}
	agent, err := NewAgentWithOptions(
		WithNet(&errorTurnNet{pc: errConn}),
		WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
		WithTURNTransportProtocols([]NetworkType{NetworkTypeUDP4}),
		WithCandidateTypes([]CandidateType{CandidateTypeRelay}),
		WithUrls([]*stun.URI{{Scheme: stun.SchemeTypeTURN, Proto: stun.ProtoTypeUDP, Host: "127.0.0.1", Port: 3478, Username: "user", Password: "pass"}}),
		WithMulticastDNSMode(MulticastDNSModeDisabled),
	)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	candidateCh := make(chan struct{}, 1)
	require.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c != nil {
			candidateCh <- struct{}{}
		}
	}))

	agent.gatherCandidatesRelay(context.Background(), agent.urls, agent.gatherGeneration)

	select {
	case <-candidateCh:
		assert.Fail(t, "unexpected candidate when TURN client fails")
	case <-time.After(200 * time.Millisecond):
	}

	assert.True(t, errConn.closed, "expected packet conn to be closed on TURN client failure")
}

func TestCloseConnLog(t *testing.T) {
	a, err := NewAgent(&AgentConfig{})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, a.Close())
	}()

	closeConnAndLog(nil, a.log, "normal nil")

	var nc *net.UDPConn
	closeConnAndLog(nc, a.log, "nil ptr")
}

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

type relayTCPProxyDialer struct {
	localAddr *net.TCPAddr
}

func (d *relayTCPProxyDialer) Dial(string, string) (net.Conn, error) {
	return &relayTCPProxyConn{localAddr: d.localAddr}, nil
}

type relayTCPProxyConn struct {
	localAddr *net.TCPAddr
}

func (c *relayTCPProxyConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *relayTCPProxyConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *relayTCPProxyConn) Close() error                     { return nil }
func (c *relayTCPProxyConn) LocalAddr() net.Addr              { return c.localAddr }
func (c *relayTCPProxyConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *relayTCPProxyConn) SetDeadline(time.Time) error      { return nil }
func (c *relayTCPProxyConn) SetReadDeadline(time.Time) error  { return nil }
func (c *relayTCPProxyConn) SetWriteDeadline(time.Time) error { return nil }

func (m *mockProxy) Dial(string, string) (net.Conn, error) {
	m.proxyWasDialed()

	return &mockConn{}, nil
}

func TestTURNProxyDialer(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	proxyWasDialed, proxyWasDialedFunc := context.WithCancel(context.Background())
	proxy.RegisterDialerType("tcp", func(*url.URL, proxy.Dialer) (proxy.Dialer, error) {
		return &mockProxy{proxyWasDialedFunc}, nil
	})

	tcpProxyURI, err := url.Parse("tcp://fakeproxy:3128")
	require.NoError(t, err)

	proxyDialer, err := proxy.FromURL(tcpProxyURI, proxy.Direct)
	require.NoError(t, err)

	agent, err := NewAgent(&AgentConfig{CandidateTypes: []CandidateType{CandidateTypeRelay}, NetworkTypes: supportedNetworkTypes(), Urls: []*stun.URI{{Scheme: stun.SchemeTypeTURN, Host: localhostIPStr, Username: "username", Password: "password", Proto: stun.ProtoTypeTCP, Port: 5000}}, ProxyDialer: proxyDialer})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	candidateGatherFinish, candidateGatherFinishFunc := context.WithCancel(context.Background())
	require.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c == nil {
			candidateGatherFinishFunc()
		}
	}))

	require.NoError(t, agent.GatherCandidates())
	<-candidateGatherFinish.Done()
	<-proxyWasDialed.Done()
}

func TestGatherCandidatesRelayTURNOverTCPProducesUDPRelayCandidate(t *testing.T) {
	defer test.CheckRoutines(t)()

	stubClient := &stubTurnClient{}
	stubClient.relayConn = newStubPacketConn(&net.UDPAddr{IP: net.IPv4(203, 0, 113, 10), Port: 6000})

	agent, err := NewAgentWithOptions(
		WithNet(newRelayGatherNet(&net.UDPAddr{IP: net.IPv4(10, 0, 0, 5), Port: 50000})),
		WithNetworkTypes([]NetworkType{NetworkTypeUDP4, NetworkTypeTCP4}),
		WithTURNTransportProtocols([]NetworkType{NetworkTypeTCP4}),
		WithCandidateTypes([]CandidateType{CandidateTypeRelay}),
		WithUrls([]*stun.URI{{Scheme: stun.SchemeTypeTURN, Host: "example.com", Port: 3478, Username: "username", Password: "password", Proto: stun.ProtoTypeTCP}}),
		WithMulticastDNSMode(MulticastDNSModeDisabled),
	)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	agent.proxyDialer = &relayTCPProxyDialer{localAddr: &net.TCPAddr{IP: net.IPv4(10, 0, 0, 5), Port: 55000}}
	agent.turnClientFactory = func(cfg *turn.ClientConfig) (turnClient, error) {
		stubClient.cfgConn = cfg.Conn

		return stubClient, nil
	}

	relayCandidateCh := make(chan *CandidateRelay, 1)
	require.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c == nil || c.Type() != CandidateTypeRelay {
			return
		}

		relay, ok := c.(*CandidateRelay)
		require.True(t, ok)

		select {
		case relayCandidateCh <- relay:
		default:
		}
	}))

	agent.gatherCandidatesRelay(context.Background(), agent.urls, agent.gatherGeneration)

	select {
	case relay := <-relayCandidateCh:
		require.Equal(t, NetworkTypeUDP4, relay.NetworkType())
		require.Equal(t, tcp, relay.RelayProtocol())
	case <-time.After(time.Second):
		require.FailNow(t, "expected relay candidate for TURN over TCP")
	}
}

func TestGatherCandidatesRelayProxySkipsTURNResolution(t *testing.T) {
	defer test.CheckRoutines(t)()

	turnNet := &unresolvableRelayGatherNet{relayGatherNet: newRelayGatherNet(&net.UDPAddr{IP: net.IPv4(10, 0, 0, 5), Port: 50000})}
	agent, err := NewAgentWithOptions(
		WithNet(turnNet),
		WithNetworkTypes([]NetworkType{NetworkTypeUDP4, NetworkTypeTCP4}),
		WithTURNTransportProtocols([]NetworkType{NetworkTypeTCP4}),
		WithCandidateTypes([]CandidateType{CandidateTypeRelay}),
		WithUrls([]*stun.URI{{Scheme: stun.SchemeTypeTURN, Host: "unresolvable.invalid", Port: 3478, Username: "username", Password: "password", Proto: stun.ProtoTypeTCP}}),
		WithMulticastDNSMode(MulticastDNSModeDisabled),
	)
	require.NoError(t, err)
	defer func() { require.NoError(t, agent.Close()) }()

	agent.proxyDialer = &relayTCPProxyDialer{localAddr: &net.TCPAddr{IP: net.IPv4(10, 0, 0, 5), Port: 55000}}
	clientConfig := make(chan *turn.ClientConfig, 1)
	agent.turnClientFactory = func(cfg *turn.ClientConfig) (turnClient, error) {
		clientConfig <- cfg

		return nil, errors.New("stop after capturing config") //nolint:err113 // test
	}

	agent.gatherCandidatesRelay(context.Background(), agent.urls, agent.gatherGeneration)

	var config *turn.ClientConfig
	select {
	case config = <-clientConfig:
	case <-time.After(time.Second):
		require.FailNow(t, "TURN client was not created")
	}
	require.Empty(t, config.TURNServerAddr)
	require.Same(t, turnNet, config.Net)
	require.Zero(t, turnNet.resolveUDPCalls.Load())
}

func TestGatherCandidatesLocalUDPMux(t *testing.T) {
	t.Run("requires mux", func(t *testing.T) {
		agent, err := NewAgent(&AgentConfig{})
		require.NoError(t, err)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		err = agent.gatherCandidatesLocalUDPMux(context.Background(), agent.gatherGeneration, agent.localUfrag)
		require.ErrorIs(t, err, errUDPMuxDisabled)
	})

	t.Run("creates host candidates from mux addresses", func(t *testing.T) {
		listenAddr := &net.UDPAddr{IP: net.IP{127, 0, 0, 1}, Port: 4789}
		udpMux := newMockUDPMux([]net.Addr{listenAddr})

		agent, err := NewAgent(&AgentConfig{NetworkTypes: []NetworkType{NetworkTypeUDP4}, CandidateTypes: []CandidateType{CandidateTypeHost}, UDPMux: udpMux, IncludeLoopback: true})
		require.NoError(t, err)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		require.NoError(t, agent.OnCandidate(func(Candidate) {}))

		err = agent.gatherCandidatesLocalUDPMux(context.Background(), agent.gatherGeneration, agent.localUfrag)
		require.NoError(t, err)

		candidates, err := agent.GetLocalCandidates()
		require.NoError(t, err)
		require.NotEmpty(t, candidates)

		host, ok := candidates[0].(*CandidateHost)
		require.True(t, ok, "expected host candidate")
		require.Equal(t, listenAddr.IP.String(), host.Address())
		require.Equal(t, listenAddr.Port, host.Port())
		require.Equal(t, 1, udpMux.connCount(), "expected mux to provide a single connection")
	})
}

func TestGatherCandidatesSrflxUDPMux(t *testing.T) {
	stunURI := &stun.URI{Scheme: stun.SchemeTypeSTUN, Host: "127.0.0.1", Port: 3478}
	relatedAddr := &net.UDPAddr{IP: net.IP{10, 0, 0, 1}, Port: 49000}
	srflxAddr := &stun.XORMappedAddress{IP: net.IP{203, 0, 113, 5}, Port: 50000}

	udpMuxSrflx := newMockUniversalUDPMux([]net.Addr{relatedAddr}, srflxAddr)

	agent, err := NewAgent(&AgentConfig{NetworkTypes: []NetworkType{NetworkTypeUDP4}, CandidateTypes: []CandidateType{CandidateTypeServerReflexive}, UDPMuxSrflx: udpMuxSrflx})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	require.NoError(t, agent.OnCandidate(func(Candidate) {}))

	agent.gatherCandidatesSrflxUDPMux(context.Background(), []*stun.URI{stunURI}, []NetworkType{NetworkTypeUDP4}, agent.gatherGeneration, agent.localUfrag)

	candidates, err := agent.GetLocalCandidates()
	require.NoError(t, err)
	require.Len(t, candidates, 1)

	srflx, ok := candidates[0].(*CandidateServerReflexive)
	require.True(t, ok, "expected server reflexive candidate")
	require.Equal(t, srflxAddr.IP.String(), srflx.Address())
	require.Equal(t, srflxAddr.Port, srflx.Port())
	require.NotNil(t, srflx.RelatedAddress())
	require.Equal(t, relatedAddr.IP.String(), srflx.RelatedAddress().Address)
	require.Equal(t, relatedAddr.Port, srflx.RelatedAddress().Port)
	require.Equal(t, 1, udpMuxSrflx.connCount(), "expected mux to be asked for one connection")
}

func TestGatherCandidatesSrflxRespectsInterfaceFilter(t *testing.T) {
	netCapture := newSrflxListenCaptureNet()

	agent, err := NewAgentWithOptions(
		WithNet(netCapture),
		WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
		WithCandidateTypes([]CandidateType{CandidateTypeServerReflexive}),
		WithMulticastDNSMode(MulticastDNSModeDisabled),
		WithUrls([]*stun.URI{{Scheme: stun.SchemeTypeSTUN, Host: localhostIPStr, Port: 9}}),
		WithInterfaceFilter(func(iface string) bool {
			return iface == "eth0"
		}),
		WithIncludeLoopback(),
		WithSTUNGatherTimeout(5*time.Millisecond),
	)

	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	require.NoError(t, agent.OnCandidate(func(Candidate) {}))

	agent.gatherCandidatesSrflx(context.Background(), agent.urls, []NetworkType{NetworkTypeUDP4}, agent.gatherGeneration)

	listenIPs := netCapture.listenCallIPs()
	require.NotEmpty(t, listenIPs)
	for _, ip := range listenIPs {
		require.Equal(t, "127.0.0.1", ip)
	}
}

func TestGatherCandidatesSrflxUDPMuxRespectsURLTransport(t *testing.T) {
	relatedAddr := &net.UDPAddr{IP: net.IP{10, 0, 0, 1}, Port: 49001}
	srflxAddr := &stun.XORMappedAddress{IP: net.IP{203, 0, 113, 6}, Port: 50001}

	udpMuxSrflx := newMockUniversalUDPMux([]net.Addr{relatedAddr}, srflxAddr)

	agent, err := NewAgent(&AgentConfig{NetworkTypes: []NetworkType{NetworkTypeUDP4}, CandidateTypes: []CandidateType{CandidateTypeServerReflexive}, UDPMuxSrflx: udpMuxSrflx})

	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	require.NoError(t, agent.OnCandidate(func(Candidate) {}))

	agent.gatherCandidatesSrflxUDPMux(context.Background(), []*stun.URI{
		{Scheme: stun.SchemeTypeSTUN, Proto: stun.ProtoTypeTCP, Host: "127.0.0.1", Port: 3478},
		{Scheme: stun.SchemeTypeSTUN, Proto: stun.ProtoTypeUDP, Host: "127.0.0.1", Port: 3478},
	}, []NetworkType{NetworkTypeUDP4}, agent.gatherGeneration, agent.localUfrag)

	candidates, err := agent.GetLocalCandidates()
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Equal(t, 1, udpMuxSrflx.connCount(), "expected only UDP transport URL to be used")
}

func TestMultiUDPMuxUsage(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	var expectedPorts []int
	var udpMuxInstances []UDPMux
	for i := range 3 {
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IP{127, 0, 0, 1}, Port: 0})
		require.NoError(t, err)
		defer func() {
			_ = conn.Close()
		}()

		expectedPorts = append(expectedPorts, portFromAddr(t, conn.LocalAddr()))
		muxDefault := NewUDPMuxDefault(UDPMuxParams{UDPConn: conn})
		udpMuxInstances = append(udpMuxInstances, muxDefault)
		idx := i
		defer func() {
			_ = udpMuxInstances[idx].Close()
		}()
	}

	agent, err := NewAgent(&AgentConfig{NetworkTypes: []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6}, CandidateTypes: []CandidateType{CandidateTypeHost}, UDPMux: NewMultiUDPMuxDefault(udpMuxInstances...)})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	candidateCh := make(chan Candidate)
	require.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c == nil {
			close(candidateCh)

			return
		}
		candidateCh <- c
	}))
	require.NoError(t, agent.GatherCandidates())

	portFound := make(map[int]bool)
	for c := range candidateCh {
		portFound[c.Port()] = true
		require.True(t, c.NetworkType().IsUDP(), "All candidates should be UDP")
	}
	require.Len(t, portFound, len(expectedPorts))
	for _, port := range expectedPorts {
		require.True(t, portFound[port], "There should be a candidate for each UDP mux port")
	}
}

func TestAddRelayCandidatesWithRewrite(t *testing.T) {
	appendRule := AddressRewriteRule{External: []string{"203.0.113.77"}, Local: "198.51.100.77", AsCandidateType: CandidateTypeRelay, Mode: AddressRewriteAppend}
	for _, testCase := range []struct {
		name            string
		networks        []NetworkType
		rule            AddressRewriteRule
		expected        []string
		immediateCloses int
	}{
		{name: "default families keep both aliases", rule: appendRule, expected: []string{"2001:db8::1", "203.0.113.77"}},
		{name: "IPv4 keeps mapped alias", networks: []NetworkType{NetworkTypeUDP4}, rule: appendRule, expected: []string{"203.0.113.77"}},
		{name: "IPv6 keeps original alias", networks: []NetworkType{NetworkTypeUDP6}, rule: appendRule, expected: []string{"2001:db8::1"}},
		{name: "rule filter removes all replacements", networks: []NetworkType{NetworkTypeUDP4}, rule: AddressRewriteRule{External: []string{"2001:db8::77"}, AsCandidateType: CandidateTypeRelay, Mode: AddressRewriteReplace, Networks: []NetworkType{NetworkTypeUDP4}}, immediateCloses: 1},
		{name: "candidate family removes all replacements", networks: []NetworkType{NetworkTypeUDP6}, rule: AddressRewriteRule{External: []string{"203.0.113.77"}, AsCandidateType: CandidateTypeRelay, Mode: AddressRewriteReplace}, immediateCloses: 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			defer test.CheckRoutines(t)()
			agent, err := NewAgentWithOptions(WithNet(newHostGatherNet(nil)), WithNetworkTypes(testCase.networks), WithMulticastDNSMode(MulticastDNSModeDisabled), WithAddressRewriteRules(testCase.rule))
			require.NoError(t, err)
			defer func() { require.NoError(t, agent.Close()) }()
			require.NoError(t, agent.OnCandidate(func(Candidate) {}))

			var allocationCloses, transportCloses int
			agent.addRelayCandidates(t.Context(), agent.gatherGeneration, relayEndpoint{
				network: udp, address: net.ParseIP("2001:db8::1"), port: 3478, relAddr: "198.51.100.77", relPort: 50000, conn: newStubPacketConn(nil),
				closeConn: func() { allocationCloses++ },
				onClose: func() error {
					transportCloses++

					return nil
				},
			})
			candidates, err := agent.GetLocalCandidates()
			require.NoError(t, err)
			addresses := make([]string, 0, len(candidates))
			for _, candidate := range candidates {
				addresses = append(addresses, candidate.Address())
			}
			require.ElementsMatch(t, testCase.expected, addresses)
			require.Equal(t, testCase.immediateCloses, allocationCloses)
			require.Equal(t, testCase.immediateCloses, transportCloses)
			require.NoError(t, agent.Close())
			require.Equal(t, testCase.immediateCloses, allocationCloses)
			require.Equal(t, 1, transportCloses, "TURN cleanup belongs to the first retained alias, or runs immediately when none remain")
		})
	}
}

func TestResolveAddressRewriteRespectsInterface(t *testing.T) {
	for _, candidateType := range []CandidateType{CandidateTypeServerReflexive, CandidateTypeRelay} {
		t.Run(candidateType.String(), func(t *testing.T) {
			mapper, err := newAddressRewriteMapper([]AddressRewriteRule{{External: []string{"203.0.113.41"}, Local: "198.51.100.6", AsCandidateType: candidateType, Iface: "hosttest0", Mode: AddressRewriteReplace}})
			require.NoError(t, err)
			agent := &Agent{addressRewriteMapper: mapper, log: logging.NewDefaultLoggerFactory().NewLogger("test")}
			for _, iface := range []string{"hosttest0", "other0"} {
				t.Run(iface, func(t *testing.T) {
					localIP := net.ParseIP("198.51.100.6")
					original := localIP
					var addresses []net.IP
					var ok bool
					if candidateType == CandidateTypeRelay {
						original = net.ParseIP("10.0.0.41")
						addresses, ok = agent.resolveRelayAddresses(relayEndpoint{address: original, relAddr: localIP.String(), iface: iface})
					} else {
						addresses, ok = agent.resolveSrflxAddresses(localIP, iface)
					}
					require.True(t, ok)
					require.Len(t, addresses, 1)
					expected := original.String()
					if iface == "hosttest0" {
						expected = "203.0.113.41"
					}
					require.Equal(t, expected, addresses[0].String())
				})
			}
		})
	}
}

func TestGatherAddressRewriteAppendHostMux(t *testing.T) { //nolint:cyclop
	for _, testCase := range []struct {
		name    string
		network NetworkType
		muxes   int
	}{
		{name: "TCP mux", network: NetworkTypeTCP4, muxes: 1},
		{name: "multi TCP mux", network: NetworkTypeTCP4, muxes: 2},
		{name: "multi UDP mux", network: NetworkTypeUDP4, muxes: 2},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Cleanup(test.CheckRoutines(t))
			options := []AgentOption{
				WithNet(newHostGatherNet(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})),
				WithCandidateTypes([]CandidateType{CandidateTypeHost}),
				WithNetworkTypes([]NetworkType{testCase.network}),
				WithIncludeLoopback(),
				WithMulticastDNSMode(MulticastDNSModeDisabled),
				WithAddressRewriteRules(AddressRewriteRule{External: []string{"198.51.100.1"}, Local: "127.0.0.1", AsCandidateType: CandidateTypeHost, Mode: AddressRewriteAppend}),
			}
			ports := make([]int, 0, testCase.muxes)
			if testCase.network.IsTCP() {
				muxes := make([]TCPMux, 0, testCase.muxes)
				for range testCase.muxes {
					listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
					require.NoError(t, err)
					child := NewTCPMuxDefault(TCPMuxParams{Listener: listener, ReadBufferSize: 20})
					t.Cleanup(func() { require.NoError(t, child.Close()) })
					addr, ok := listener.Addr().(*net.TCPAddr)
					require.True(t, ok)
					ports = append(ports, addr.Port)
					muxes = append(muxes, child)
				}
				mux := muxes[0]
				if testCase.muxes > 1 {
					mux = NewMultiTCPMuxDefault(muxes...)
				}
				options = append(options, WithTCPMux(mux))
			} else {
				muxes := make([]UDPMux, 0, testCase.muxes)
				for range testCase.muxes {
					conn, err := net.ListenUDP(udp, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
					require.NoError(t, err)
					child := NewUDPMuxDefault(UDPMuxParams{UDPConn: conn})
					t.Cleanup(func() { require.NoError(t, child.Close()) })
					addr, ok := conn.LocalAddr().(*net.UDPAddr)
					require.True(t, ok)
					ports = append(ports, addr.Port)
					muxes = append(muxes, child)
				}
				mux := NewMultiUDPMuxDefault(muxes...)
				options = append(options, WithUDPMux(mux))
			}
			agent, err := NewAgentWithOptions(options...)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, agent.Close()) })

			candidates := gatherForRewriteTest(t, agent)
			require.Len(t, candidates, 2*testCase.muxes)
			byPort := make(map[int][]*CandidateHost)
			for _, candidate := range candidates {
				host, ok := candidate.(*CandidateHost)
				require.True(t, ok)
				require.Equal(t, CandidateTypeHost, host.Type())
				require.Equal(t, testCase.network, host.NetworkType())
				if testCase.network.IsTCP() {
					require.Equal(t, TCPTypePassive, host.TCPType())
				}
				byPort[host.Port()] = append(byPort[host.Port()], host)
			}
			require.Len(t, byPort, testCase.muxes)

			unwrap := func(host *CandidateHost) *sharedPacketConn {
				t.Helper()

				if testCase.network.IsTCP() {
					wrapper, ok := host.conn.(*sharedPacketConn)
					require.True(t, ok)
					underlying, ok := wrapper.underlying.(*tcpPacketConn)
					require.True(t, ok)
					require.Same(t, &underlying.refs, wrapper.refs)

					return wrapper
				}
				wrapper, ok := host.conn.(*sharedAddrPortConn)
				require.True(t, ok)
				underlying, ok := wrapper.underlying.(*udpMuxedConn)
				require.True(t, ok)
				require.Same(t, &underlying.refs, wrapper.refs)

				return wrapper.sharedPacketConn
			}
			wrappers := make([]*sharedPacketConn, 0, testCase.muxes)
			for _, port := range ports {
				group := byPort[port]
				require.Len(t, group, 2, "two aliases per mux port")
				require.ElementsMatch(t, []string{"127.0.0.1", "198.51.100.1"}, []string{group[0].Address(), group[1].Address()})
				first, second := unwrap(group[0]), unwrap(group[1])
				require.NotSame(t, first, second)
				require.Same(t, first.underlying, second.underlying)
				require.Equal(t, int32(2), first.refs.Load())
				wrappers = append(wrappers, first)
			}
			if len(wrappers) > 1 {
				require.NotSame(t, wrappers[0].underlying, wrappers[1].underlying, "different mux ports own distinct connections")
			}

			require.NoError(t, agent.Close())
			for _, wrapper := range wrappers {
				require.Zero(t, wrapper.refs.Load(), "closing the agent releases every alias")
			}
		})
	}
}

func TestCreateRelayCandidateErrorPaths(t *testing.T) {
	newAgent := func(t *testing.T) *Agent {
		t.Helper()

		agent, err := NewAgentWithOptions(WithNet(newHostGatherNet(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})), WithMulticastDNSMode(MulticastDNSModeDisabled))
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, agent.Close())
		})

		return agent
	}

	t.Run("incomplete endpoint is ignored", func(t *testing.T) {
		agent := newAgent(t)
		agent.addRelayCandidates(context.Background(), agent.gatherGeneration, relayEndpoint{})

		candidates, err := agent.GetLocalCandidates()
		require.NoError(t, err)
		assert.Empty(t, candidates)
	})

	t.Run("invalid candidate closes connection", func(t *testing.T) {
		closed := false
		agent := newAgent(t)
		agent.addRelayCandidates(context.Background(), agent.gatherGeneration, relayEndpoint{
			network: "bogus-network",
			address: net.IPv4(10, 0, 0, 4),
			port:    3478,
			relAddr: "198.51.100.4",
			relPort: 5000,
			conn:    newStubPacketConn(nil),
			closeConn: func() {
				closed = true
			},
		})

		candidates, err := agent.GetLocalCandidates()
		require.NoError(t, err)
		assert.Empty(t, candidates)
		assert.True(t, closed)
	})

	t.Run("canceled gather closes candidate", func(t *testing.T) {
		onCloseCalls := 0
		agent := newAgent(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		agent.addRelayCandidates(ctx, agent.gatherGeneration, relayEndpoint{
			network: NetworkTypeUDP4.String(),
			address: net.IPv4(10, 0, 0, 5),
			port:    3478,
			relAddr: "198.51.100.5",
			relPort: 5000,
			conn:    newStubPacketConn(nil),
			onClose: func() error {
				onCloseCalls++

				return errNotImplemented
			},
		})

		candidates, err := agent.GetLocalCandidates()
		require.NoError(t, err)
		assert.Empty(t, candidates)
		assert.Equal(t, 1, onCloseCalls)
	})
}

func TestGatherCandidatesLocalTCPMuxSkipsUnboundInterfaces(t *testing.T) {
	tcpMux := &boundTCPMux{localAddr: &net.TCPAddr{IP: net.ParseIP("203.0.113.10"), Port: 5555}}
	agent, err := NewAgentWithOptions(WithNet(newHostGatherNet(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})), WithCandidateTypes([]CandidateType{CandidateTypeHost}), WithNetworkTypes([]NetworkType{NetworkTypeTCP4}), WithTCPMux(tcpMux), WithIncludeLoopback(), WithMulticastDNSMode(MulticastDNSModeDisabled))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, agent.Close())
	})
	require.NoError(t, agent.OnCandidate(func(Candidate) {}))

	agent.gatherCandidatesLocal(context.Background(), []NetworkType{NetworkTypeTCP4}, agent.gatherGeneration, agent.localUfrag)

	cands, err := agent.GetLocalCandidates()
	require.NoError(t, err)
	assert.Empty(t, cands)
}

func TestGatherCandidatesLocalHostErrorPaths(t *testing.T) {
	t.Run("UDPMux invalid address closes conn", func(t *testing.T) {
		mux := newInvalidAddrUDPMux()
		agent, err := NewAgentWithOptions(WithNet(newHostGatherNet(nil)), WithCandidateTypes([]CandidateType{CandidateTypeHost}), WithNetworkTypes([]NetworkType{NetworkTypeUDP4}), WithUDPMux(mux), WithMulticastDNSMode(MulticastDNSModeDisabled))
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, agent.Close())
		})
		require.NoError(t, agent.OnCandidate(func(Candidate) {}))

		assert.NoError(t, agent.gatherCandidatesLocalUDPMux(context.Background(), agent.gatherGeneration, agent.localUfrag))

		assert.True(t, mux.conn.closed)
		cands, err := agent.GetLocalCandidates()
		require.NoError(t, err)
		assert.Empty(t, cands)
	})

	t.Run("NewCandidateHost failure logs and closes conn", func(t *testing.T) {
		agent, err := NewAgentWithOptions(WithNet(newHostGatherNet(nil)), WithCandidateTypes([]CandidateType{CandidateTypeHost}), WithNetworkTypes([]NetworkType{NetworkTypeUDP4}), WithMulticastDNSMode(MulticastDNSModeQueryAndGather))
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, agent.Close())
		})
		require.NoError(t, agent.OnCandidate(func(Candidate) {}))
		agent.includeLoopback = true
		agent.mDNSName = "invalid-mdns" // no .local suffix -> NewCandidateHost parse fails

		agent.gatherCandidatesLocal(context.Background(), []NetworkType{NetworkTypeUDP4}, agent.gatherGeneration, agent.localUfrag)

		cands, err := agent.GetLocalCandidates()
		require.NoError(t, err)
		assert.Empty(t, cands)
	})

	t.Run("addCandidate error logs and keeps no candidates", func(t *testing.T) {
		agent, err := NewAgentWithOptions(WithNet(newHostGatherNet(nil)), WithCandidateTypes([]CandidateType{CandidateTypeHost}), WithNetworkTypes([]NetworkType{NetworkTypeUDP4}), WithMulticastDNSMode(MulticastDNSModeDisabled))
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, agent.Close())
		})
		require.NoError(t, agent.OnCandidate(func(Candidate) {}))
		agent.includeLoopback = true

		agent.loop.Close()

		agent.gatherCandidatesLocal(context.Background(), []NetworkType{NetworkTypeUDP4}, agent.gatherGeneration, agent.localUfrag)

		agent.loop.Run(agent.loop, func(context.Context) { //nolint:errcheck,gosec
			assert.Empty(t, agent.localCandidates[NetworkTypeUDP4])
		})
	})
}

func TestMultiTCPMuxUsage(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	var expectedPorts []int
	var tcpMuxInstances []TCPMux
	for range 3 {
		listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IP{127, 0, 0, 1}, Port: 0})
		require.NoError(t, err)
		defer func() {
			_ = listener.Close()
		}()

		expectedPorts = append(expectedPorts, portFromAddr(t, listener.Addr()))
		tcpMux := NewTCPMuxDefault(TCPMuxParams{Listener: listener, ReadBufferSize: 8})
		defer func() {
			_ = tcpMux.Close()
		}()
		tcpMuxInstances = append(tcpMuxInstances, tcpMux)
	}

	agent, err := NewAgent(&AgentConfig{NetworkTypes: supportedNetworkTypes(), CandidateTypes: []CandidateType{CandidateTypeHost}, TCPMux: NewMultiTCPMuxDefault(tcpMuxInstances...)})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	candidateCh := make(chan Candidate)
	require.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c == nil {
			close(candidateCh)

			return
		}
		candidateCh <- c
	}))
	require.NoError(t, agent.GatherCandidates())

	portFound := make(map[int]bool)
	for c := range candidateCh {
		activeCandidate := c.Port() == 0
		if c.NetworkType().IsTCP() && !activeCandidate {
			portFound[c.Port()] = true
		}
	}
	require.Len(t, portFound, len(expectedPorts))
	for _, port := range expectedPorts {
		require.True(t, portFound[port], "There should be a candidate for each TCP mux port")
	}
}

func TestUniversalUDPMuxUsage(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IP{127, 0, 0, 1}, Port: 0})
	require.NoError(t, err)
	defer func() {
		_ = conn.Close()
	}()

	udpMuxSrflx := &universalUDPMuxMock{conn: conn}

	numSTUNS := 3
	urls := []*stun.URI{}
	for i := range numSTUNS {
		urls = append(urls, &stun.URI{Scheme: SchemeTypeSTUN, Host: localhostIPStr, Port: 3478 + i})
	}

	agent, err := NewAgent(&AgentConfig{NetworkTypes: supportedNetworkTypes(), Urls: urls, CandidateTypes: []CandidateType{CandidateTypeServerReflexive}, UDPMuxSrflx: udpMuxSrflx})
	require.NoError(t, err)
	var aClosed bool
	defer func() {
		if aClosed {
			return
		}
		require.NoError(t, agent.Close())
	}()

	candidateGathered, candidateGatheredFunc := context.WithCancel(context.Background())
	require.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c == nil {
			candidateGatheredFunc()

			return
		}
		t.Log(c.NetworkType(), c.Priority(), c)
	}))
	require.NoError(t, agent.GatherCandidates())

	<-candidateGathered.Done()

	require.NoError(t, agent.Close())
	aClosed = true

	// Twice because of 2 STUN servers configured
	require.Equal(t, numSTUNS, udpMuxSrflx.getXORMappedAddrUsedTimes, "expected times that GetXORMappedAddr should be called")
	// One for Restart() when agent has been initialized and one time when Close() the agent
	require.Equal(t, 2, udpMuxSrflx.removeConnByUfragTimes, "expected times that RemoveConnByUfrag should be called")
	// Twice because of 2 STUN servers configured
	require.Equal(t, numSTUNS, udpMuxSrflx.getConnForURLTimes, "expected times that GetConnForURL should be called")
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

func TestShouldFilterLocationTrackedIP(t *testing.T) {
	linkLocal := netip.MustParseAddr("fe80::1")
	globalV6 := netip.MustParseAddr("2001:db8::1")
	ipv4 := netip.MustParseAddr("192.0.2.1")

	require.True(t, shouldFilterLocationTrackedIP(linkLocal))
	require.False(t, shouldFilterLocationTrackedIP(globalV6))
	require.False(t, shouldFilterLocationTrackedIP(ipv4))
}

func TestShouldFilterLocationTracked(t *testing.T) {
	require.True(t, shouldFilterLocationTracked(net.ParseIP("fe80::abcd")))
	require.False(t, shouldFilterLocationTracked(net.ParseIP("2001:db8::abcd")))
	require.False(t, shouldFilterLocationTracked(net.ParseIP("192.0.2.10")))
	require.False(t, shouldFilterLocationTracked(net.IP{}))
}

func TestContinualGatheringPolicy(t *testing.T) { //nolint:cyclop
	// Limit runtime in case of deadlocks
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	loggerFactory := logging.NewDefaultLoggerFactory()
	loggerFactory.DefaultLogLevel = logging.LogLevelDebug

	t.Run("GatherOnce completes gathering", func(t *testing.T) {
		agent, err := NewAgent(&AgentConfig{ //nolint:contextcheck
			NetworkTypes:   []NetworkType{NetworkTypeUDP4},
			CandidateTypes: []CandidateType{CandidateTypeHost},
			LoggerFactory:  loggerFactory,
		})
		require.NoError(t, err)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		// Set handler to collect candidates
		candidateCh := make(chan Candidate, 10)
		err = agent.OnCandidate(func(c Candidate) {
			if c != nil {
				candidateCh <- c
			}
		})
		require.NoError(t, err)

		// Start gathering
		err = agent.GatherCandidates() //nolint:contextcheck
		require.NoError(t, err)

		// Wait for gathering to complete
		gatheringComplete := false
		timeout := time.After(5 * time.Second)
		for !gatheringComplete {
			select {
			case <-candidateCh:
				// Got a candidate, continue
			case <-timeout:
				assert.Fail(t, "Timeout waiting for gathering to complete")
			case <-time.After(100 * time.Millisecond):
				// Check if gathering is complete
				state, gatherErr := agent.GetGatheringState() //nolint:contextcheck
				require.NoError(t, gatherErr)
				if state == GatheringStateComplete {
					gatheringComplete = true
				}
			case <-ctx.Done():
				assert.Fail(t, "Context timeout")
			}
		}

		// Verify gathering state is complete
		state, err := agent.GetGatheringState() //nolint:contextcheck
		require.NoError(t, err)
		assert.Equal(t, GatheringStateComplete, state, "GatherOnce should set state to Complete")
	})

	t.Run("GatherContinually never completes", func(t *testing.T) {
		monitorInterval := 500 * time.Millisecond
		agent, err := NewAgentWithOptions( //nolint:contextcheck
			WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
			WithCandidateTypes([]CandidateType{CandidateTypeHost}),
			WithContinualGatheringPolicy(GatherContinually),
			WithNetworkMonitorInterval(monitorInterval),
		)
		require.NoError(t, err)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		// Set handler to collect candidates
		candidateCh := make(chan Candidate, 10)
		err = agent.OnCandidate(func(c Candidate) {
			if c != nil {
				candidateCh <- c
			}
		})
		require.NoError(t, err)

		// Start gathering
		err = agent.GatherCandidates() //nolint:contextcheck
		require.NoError(t, err)

		// Wait for initial candidates
		select {
		case <-candidateCh:
			// Got at least one candidate
		case <-time.After(5 * time.Second):
			assert.Fail(t, "Timeout waiting for initial candidates")
		case <-ctx.Done():
			assert.Fail(t, "Context timeout")
		}

		// Wait to ensure gathering doesn't complete
		time.Sleep(1 * time.Second)

		// Verify gathering state is still gathering
		state, err := agent.GetGatheringState() //nolint:contextcheck
		require.NoError(t, err)
		assert.Equal(t, GatheringStateGathering, state, "GatherContinually should keep state as Gathering")
	})

	t.Run("Network monitoring interval is configurable", func(t *testing.T) {
		customInterval := 100 * time.Millisecond
		agent, err := NewAgentWithOptions(WithNetworkTypes([]NetworkType{NetworkTypeUDP4}), WithCandidateTypes([]CandidateType{CandidateTypeHost}), WithContinualGatheringPolicy(GatherContinually), WithNetworkMonitorInterval(customInterval))
		require.NoError(t, err)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		// Verify the interval was set
		assert.Equal(t, customInterval, agent.networkMonitorInterval)
	})

	t.Run("Default network monitoring interval", func(t *testing.T) {
		agent, err := NewAgentWithOptions(WithNetworkTypes([]NetworkType{NetworkTypeUDP4}), WithCandidateTypes([]CandidateType{CandidateTypeHost}), WithContinualGatheringPolicy(GatherContinually))
		require.NoError(t, err)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		// Verify default interval is 2 seconds
		assert.Equal(t, 2*time.Second, agent.networkMonitorInterval)
	})
}

func TestNetworkChangeDetection(t *testing.T) {
	// Limit runtime in case of deadlocks
	report := test.CheckRoutines(t)
	defer report()

	loggerFactory := logging.NewDefaultLoggerFactory()
	loggerFactory.DefaultLogLevel = logging.LogLevelDebug

	t.Run("detectNetworkChanges identifies new interfaces", func(t *testing.T) {
		customInterval := 100 * time.Millisecond
		agent, err := NewAgentWithOptions(WithNetworkTypes([]NetworkType{NetworkTypeUDP4}), WithCandidateTypes([]CandidateType{CandidateTypeHost}), WithContinualGatheringPolicy(GatherContinually), WithNetworkMonitorInterval(customInterval))
		require.NoError(t, err)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		// Initialize the last known interfaces
		_, addrs, err := localInterfaces(agent.net, agent.interfaceFilter, agent.ipFilter, agent.networkTypes, agent.includeLoopback)
		require.NoError(t, err)

		for _, info := range addrs {
			agent.lastKnownInterfaces[info.addr.String()] = info.addr
		}

		// First check should return false (no changes)
		hasChanges := agent.detectNetworkChanges()
		assert.False(t, hasChanges, "Should not detect changes when interfaces haven't changed")

		// Simulate a removed interface by clearing the last known interfaces
		// and then checking again
		if len(agent.lastKnownInterfaces) > 0 {
			// Remove one interface from the map to simulate change
			for key := range agent.lastKnownInterfaces {
				delete(agent.lastKnownInterfaces, key)

				break
			}

			// This should detect a change
			hasChanges = agent.detectNetworkChanges()
			assert.True(t, hasChanges, "Should detect changes when interfaces are different")
		}
	})
}

func TestContinualGatheringPolicyString(t *testing.T) {
	tests := []struct {
		policy   ContinualGatheringPolicy
		expected string
	}{
		{GatherOnce, "gather_once"},
		{GatherContinually, "gather_continually"},
		{ContinualGatheringPolicy(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.policy.String())
		})
	}
}

type stubPacketConn struct {
	addr   net.Addr
	closed bool
	mu     sync.Mutex
}

func newStubPacketConn(addr net.Addr) *stubPacketConn {
	if addr == nil {
		addr = &net.UDPAddr{IP: net.IPv4zero, Port: 0}
	}

	return &stubPacketConn{addr: addr}
}

func (s *stubPacketConn) ReadFrom(_ []byte) (int, net.Addr, error) {
	return 0, s.addr, io.EOF
}

func (s *stubPacketConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	return len(p), nil
}

func (s *stubPacketConn) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true

	return nil
}

func (s *stubPacketConn) LocalAddr() net.Addr { return s.addr }

func (s *stubPacketConn) SetDeadline(time.Time) error      { return nil }
func (s *stubPacketConn) SetReadDeadline(time.Time) error  { return nil }
func (s *stubPacketConn) SetWriteDeadline(time.Time) error { return nil }

type mockUDPMux struct {
	listenAddrs []net.Addr
	mu          sync.Mutex
	conns       []*stubPacketConn
}

func newMockUDPMux(addrs []net.Addr) *mockUDPMux {
	return &mockUDPMux{listenAddrs: addrs}
}

func (m *mockUDPMux) GetConn(string, net.Addr) (net.PacketConn, error) {
	conn := newStubPacketConn(m.listenAddrs[0])
	m.mu.Lock()
	m.conns = append(m.conns, conn)
	m.mu.Unlock()

	return conn, nil
}

func (m *mockUDPMux) RemoveConnByUfrag(string) {}

func (m *mockUDPMux) GetListenAddresses() []net.Addr {
	return m.listenAddrs
}

func (m *mockUDPMux) Close() error { return nil }

func (m *mockUDPMux) connCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return len(m.conns)
}

type gatedUDPMux struct {
	addr        net.Addr
	gathering   chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
	ufrag       chan string
}

func newGatedUDPMux(addr net.Addr) *gatedUDPMux {
	return &gatedUDPMux{
		addr:      addr,
		gathering: make(chan struct{}),
		release:   make(chan struct{}),
		ufrag:     make(chan string, 1),
	}
}

func (m *gatedUDPMux) GetConn(ufrag string, _ net.Addr) (net.PacketConn, error) {
	m.ufrag <- ufrag

	return newStubPacketConn(m.addr), nil
}

func (m *gatedUDPMux) RemoveConnByUfrag(string) {}

func (m *gatedUDPMux) GetListenAddresses() []net.Addr {
	close(m.gathering)
	<-m.release

	return []net.Addr{m.addr}
}

func (m *gatedUDPMux) Close() error { return nil }
func (m *gatedUDPMux) Release()     { m.releaseOnce.Do(func() { close(m.release) }) }

func TestGatherUsesStartingUfragAcrossRestart(t *testing.T) {
	defer test.CheckRoutines(t)()
	defer test.TimeOut(5 * time.Second).Stop()

	mux := newGatedUDPMux(&net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 4000})
	agent, err := NewAgentWithOptions(WithNet(newHostGatherNet(&net.UDPAddr{IP: net.IPv4(10, 0, 0, 1)})), WithNetworkTypes([]NetworkType{NetworkTypeUDP4}), WithCandidateTypes([]CandidateType{CandidateTypeHost}), WithUDPMux(mux), WithMulticastDNSMode(MulticastDNSModeDisabled))
	require.NoError(t, err)
	defer func() { require.NoError(t, agent.Close()) }()
	defer mux.Release()

	startingUfrag, _, err := agent.GetLocalUserCredentials()
	require.NoError(t, err)
	require.NoError(t, agent.OnCandidate(func(Candidate) {}))
	require.NoError(t, agent.GatherCandidates())
	<-mux.gathering

	require.NoError(t, agent.Restart("", ""))
	mux.Release()
	require.Equal(t, startingUfrag, <-mux.ufrag)
}

type invalidAddrUDPMux struct {
	conn *stubPacketConn
}

func newInvalidAddrUDPMux() *invalidAddrUDPMux {
	return &invalidAddrUDPMux{conn: newStubPacketConn(&net.UDPAddr{IP: net.IPv4(10, 0, 0, 10), Port: 1234})}
}

func (m *invalidAddrUDPMux) GetConn(string, net.Addr) (net.PacketConn, error) {
	return m.conn, nil
}

func (m *invalidAddrUDPMux) RemoveConnByUfrag(string) {}

func (m *invalidAddrUDPMux) GetListenAddresses() []net.Addr {
	return []net.Addr{&net.UDPAddr{IP: nil, Port: 1234}}
}

func (m *invalidAddrUDPMux) Close() error { return nil }

type mockUniversalUDPMux struct {
	*mockUDPMux
	xorAddr *stun.XORMappedAddress
}

func newMockUniversalUDPMux(addrs []net.Addr, xorAddr *stun.XORMappedAddress) *mockUniversalUDPMux {
	return &mockUniversalUDPMux{mockUDPMux: newMockUDPMux(addrs), xorAddr: xorAddr}
}

func (m *mockUniversalUDPMux) GetXORMappedAddr(net.Addr, time.Duration) (*stun.XORMappedAddress, error) {
	return m.xorAddr, nil
}

func (m *mockUniversalUDPMux) GetRelayedAddr(net.Addr, time.Duration) (*net.Addr, error) {
	return nil, errNotImplemented
}

func (m *mockUniversalUDPMux) GetConnForURL(ufrag string, url string, addr net.Addr) (net.PacketConn, error) {
	return m.GetConn(ufrag+url, addr)
}

func TestTURNContext(t *testing.T) {
	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 3).Stop()

	listener, err := net.ListenPacket("udp4", "127.0.0.1:0") // nolint: noctx
	skipOnPermission(t, err, "listening for TURN server")

	turnPacketSeen, turnPacketSeenDone := context.WithCancel(context.Background())
	go func() {
		_, _, readErr := listener.ReadFrom(nil)
		assert.NoError(t, readErr)
		assert.NoError(t, listener.Close())
		turnPacketSeenDone()
	}()

	agent, err := NewAgent(&AgentConfig{NetworkTypes: supportedNetworkTypes(), Urls: []*stun.URI{{Scheme: stun.SchemeTypeTURN, Proto: stun.ProtoTypeUDP, Host: localhostIPStr, Port: portFromAddr(t, listener.LocalAddr()), Username: "username", Password: "password"}}, CandidateTypes: []CandidateType{CandidateTypeRelay}})
	require.NoError(t, err)
	require.NoError(t, agent.OnCandidate(func(Candidate) {}))

	require.NoError(t, agent.GatherCandidates())
	<-turnPacketSeen.Done()
	assert.NoError(t, agent.Close())
}
