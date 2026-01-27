// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

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
	"github.com/pion/ice/v4/internal/taskloop"
	"github.com/pion/logging"
	"github.com/pion/stun/v3"
	transport "github.com/pion/transport/v4"
	"github.com/pion/transport/v4/test"
	"github.com/pion/transport/v4/vnet"
	"github.com/pion/turn/v4"
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

func TestListenUDP(t *testing.T) {
	agent, err := NewAgent(&AgentConfig{})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	_, localAddrs, err := localInterfaces(
		agent.net,
		agent.interfaceFilter,
		agent.ipFilter,
		[]NetworkType{NetworkTypeUDP4},
		false,
	)
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
	for i := 0; i < total; i++ {
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

	agent, err := NewAgent(&AgentConfig{
		NetworkTypes:    []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
		IncludeLoopback: true,
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	candidateGathered, candidateGatheredFunc := context.WithCancel(context.Background())
	require.NoError(t, agent.OnCandidate(func(Candidate) {
		candidateGatheredFunc()
	}))

	// Testing for panic
	for i := 0; i < 10; i++ {
		_ = agent.GatherCandidates()
	}

	<-candidateGathered.Done()
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
	muxUnspecDefault := NewUDPMuxDefault(UDPMuxParams{
		UDPConn: unspecConn,
	})

	testCases := []testCase{
		{
			name: "mux should not have loopback candidate",
			agentConfig: &AgentConfig{
				NetworkTypes: []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
				UDPMux:       mux,
			},
			loExpected: false,
		},
		{
			name: "mux with loopback should not have loopback candidate",
			agentConfig: &AgentConfig{
				NetworkTypes: []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
				UDPMux:       muxWithLo,
			},
			loExpected: true,
		},
		{
			name: "UDPMuxDefault with unspecified IP should not have loopback candidate",
			agentConfig: &AgentConfig{
				NetworkTypes: []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
				UDPMux:       muxUnspecDefault,
			},
			loExpected: false,
		},
		{
			name: "UDPMuxDefault with unspecified IP should respect agent includeloopback",
			agentConfig: &AgentConfig{
				NetworkTypes:    []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
				UDPMux:          muxUnspecDefault,
				IncludeLoopback: true,
			},
			loExpected: true,
		},
		{
			name: "includeloopback enabled",
			agentConfig: &AgentConfig{
				NetworkTypes:    []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
				IncludeLoopback: true,
			},
			loExpected: true,
		},
		{
			name: "includeloopback disabled",
			agentConfig: &AgentConfig{
				NetworkTypes:    []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
				IncludeLoopback: false,
			},
			loExpected: false,
		},
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
			var loopback int32
			require.NoError(t, agent.OnCandidate(func(c Candidate) {
				if c != nil {
					if net.ParseIP(c.Address()).IsLoopback() {
						atomic.StoreInt32(&loopback, 1)
					}
				} else {
					candidateGatheredFunc()

					return
				}
				t.Log(c.NetworkType(), c.Priority(), c)
			}))
			require.NoError(t, agent.GatherCandidates())

			<-candidateGathered.Done()

			require.Equal(t, tcase.loExpected, atomic.LoadInt32(&loopback) == 1)
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

	serverPort := randomPort(t)
	serverListener, err := net.ListenPacket("udp4", localhostIPStr+":"+strconv.Itoa(serverPort)) // nolint: noctx
	require.NoError(t, err)

	server, err := turn.NewServer(turn.ServerConfig{
		Realm:       "pion.ly",
		AuthHandler: optimisticAuthHandler,
		PacketConnConfigs: []turn.PacketConnConfig{
			{
				PacketConn:            serverListener,
				RelayAddressGenerator: &turn.RelayAddressGeneratorNone{Address: localhostIPStr},
			},
		},
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, server.Close())
	}()

	urls := []*stun.URI{}
	for i := 0; i <= 10; i++ {
		urls = append(urls, &stun.URI{
			Scheme: stun.SchemeTypeSTUN,
			Host:   localhostIPStr,
			Port:   serverPort + 1,
		})
	}
	urls = append(urls, &stun.URI{
		Scheme: stun.SchemeTypeSTUN,
		Host:   localhostIPStr,
		Port:   serverPort,
	})

	listener, err := net.ListenTCP("tcp", &net.TCPAddr{
		IP: net.IP{127, 0, 0, 1},
	})
	require.NoError(t, err)
	defer func() {
		_ = listener.Close()
	}()

	tcpMux := NewTCPMuxDefault(
		TCPMuxParams{
			Listener:       listener,
			Logger:         logging.NewDefaultLoggerFactory().NewLogger("ice"),
			ReadBufferSize: 8,
		},
	)
	defer func() {
		_ = tcpMux.Close()
	}()

	agent, err := NewAgent(&AgentConfig{
		NetworkTypes:   supportedNetworkTypes(),
		Urls:           urls,
		CandidateTypes: []CandidateType{CandidateTypeHost, CandidateTypeServerReflexive},
		TCPMux:         tcpMux,
	})
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
			packetConnConfigs = append(packetConnConfigs, turn.PacketConnConfig{
				PacketConn:            packetConn,
				RelayAddressGenerator: &turn.RelayAddressGeneratorNone{Address: localhostIPStr},
			})
		}

		listenerConfigs := []turn.ListenerConfig{}
		if listener != nil {
			listenerConfigs = append(listenerConfigs, turn.ListenerConfig{
				Listener:              listener,
				RelayAddressGenerator: &turn.RelayAddressGeneratorNone{Address: localhostIPStr},
			})
		}

		server, err := turn.NewServer(turn.ServerConfig{
			Realm:             "pion.ly",
			AuthHandler:       optimisticAuthHandler,
			PacketConnConfigs: packetConnConfigs,
			ListenerConfigs:   listenerConfigs,
		})
		require.NoError(t, err)
		defer func() {
			require.NoError(t, server.Close())
		}()

		urls := []*stun.URI{}
		// avoid long delay on unreachable ports on Windows
		if runtime.GOOS != "windows" {
			for i := 0; i <= 10; i++ {
				urls = append(urls, &stun.URI{
					Scheme:   scheme,
					Host:     localhostIPStr,
					Username: "username",
					Password: "password",
					Proto:    protocol,
					Port:     serverPort + 1 + i,
				})
			}
		}
		urls = append(urls, &stun.URI{
			Scheme:   scheme,
			Host:     localhostIPStr,
			Username: "username",
			Password: "password",
			Proto:    protocol,
			Port:     serverPort,
		})

		agent, err := NewAgent(&AgentConfig{
			CandidateTypes:     []CandidateType{CandidateTypeRelay},
			InsecureSkipVerify: true,
			NetworkTypes:       supportedNetworkTypes(),
			Urls:               urls,
		})
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
		serverPort := randomPort(t)
		serverListener, err := net.ListenPacket("udp", localhostIPStr+":"+strconv.Itoa(serverPort)) // nolint: noctx
		require.NoError(t, err)

		runTest(stun.ProtoTypeUDP, stun.SchemeTypeTURN, serverListener, nil, serverPort)
	})

	t.Run("TCP Relay", func(t *testing.T) {
		serverPort := randomPort(t)
		serverListener, err := net.Listen("tcp", localhostIPStr+":"+strconv.Itoa(serverPort)) // nolint: noctx
		require.NoError(t, err)

		runTest(stun.ProtoTypeTCP, stun.SchemeTypeTURN, nil, serverListener, serverPort)
	})

	t.Run("TLS Relay", func(t *testing.T) {
		certificate, genErr := selfsign.GenerateSelfSigned()
		require.NoError(t, genErr)

		serverPort := randomPort(t)
		serverListener, err := tls.Listen("tcp", localhostIPStr+":"+strconv.Itoa(serverPort), &tls.Config{ //nolint:gosec
			Certificates: []tls.Certificate{certificate},
		})
		require.NoError(t, err)

		runTest(stun.ProtoTypeTCP, stun.SchemeTypeTURNS, nil, serverListener, serverPort)
	})

	t.Run("DTLS Relay", func(t *testing.T) {
		certificate, genErr := selfsign.GenerateSelfSigned()
		require.NoError(t, genErr)

		serverPort := randomPort(t)
		serverListener, err := dtls.Listen(
			"udp",
			&net.UDPAddr{IP: net.ParseIP(localhostIPStr), Port: serverPort},
			&dtls.Config{
				Certificates: []tls.Certificate{certificate},
			},
		)
		require.NoError(t, err)

		runTest(stun.ProtoTypeUDP, stun.SchemeTypeTURNS, nil, serverListener, serverPort)
	})
}

// Assert that STUN and TURN gathering are done concurrently.
func TestSTUNTURNConcurrency(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 8).Stop()

	serverPort := randomPort(t)
	serverListener, err := net.ListenPacket("udp4", localhostIPStr+":"+strconv.Itoa(serverPort)) // nolint: noctx
	require.NoError(t, err)

	server, err := turn.NewServer(turn.ServerConfig{
		Realm:       "pion.ly",
		AuthHandler: optimisticAuthHandler,
		PacketConnConfigs: []turn.PacketConnConfig{
			{
				PacketConn:            serverListener,
				RelayAddressGenerator: &turn.RelayAddressGeneratorNone{Address: localhostIPStr},
			},
		},
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, server.Close())
	}()

	urls := []*stun.URI{}
	for i := 0; i <= 10; i++ {
		urls = append(urls, &stun.URI{
			Scheme: stun.SchemeTypeSTUN,
			Host:   localhostIPStr,
			Port:   serverPort + 1,
		})
	}
	urls = append(urls, &stun.URI{
		Scheme:   stun.SchemeTypeTURN,
		Proto:    stun.ProtoTypeUDP,
		Host:     localhostIPStr,
		Port:     serverPort,
		Username: "username",
		Password: "password",
	})

	agent, err := NewAgent(&AgentConfig{
		NetworkTypes:   supportedNetworkTypes(),
		Urls:           urls,
		CandidateTypes: []CandidateType{CandidateTypeServerReflexive, CandidateTypeRelay},
	})
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

	serverPort := randomPort(t)
	serverListener, err := net.ListenPacket("udp4", localhostIPStr+":"+strconv.Itoa(serverPort)) // nolint: noctx
	require.NoError(t, err)

	server, err := turn.NewServer(turn.ServerConfig{
		Realm:       "pion.ly",
		AuthHandler: optimisticAuthHandler,
		PacketConnConfigs: []turn.PacketConnConfig{
			{
				PacketConn:            serverListener,
				RelayAddressGenerator: &turn.RelayAddressGeneratorNone{Address: localhostIPStr},
			},
		},
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, server.Close())
	}()

	urls := []*stun.URI{{
		Scheme:   stun.SchemeTypeTURN,
		Proto:    stun.ProtoTypeUDP,
		Host:     localhostIPStr,
		Port:     serverPort,
		Username: "username",
		Password: "password",
	}}

	agent, err := NewAgent(&AgentConfig{
		NetworkTypes:   supportedNetworkTypes(),
		Urls:           urls,
		CandidateTypes: []CandidateType{CandidateTypeServerReflexive, CandidateTypeRelay},
	})
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
	defer test.CheckRoutines(t)()

	listener, err := net.ListenPacket("udp4", "127.0.0.1:0") // nolint: noctx
	skipOnPermission(t, err, "listening for TURN server")
	require.NoError(t, err)
	defer func() {
		_ = listener.Close()
	}()

	server, err := turn.NewServer(turn.ServerConfig{
		Realm:       "pion.ly",
		AuthHandler: optimisticAuthHandler,
		PacketConnConfigs: []turn.PacketConnConfig{
			{
				PacketConn:            listener,
				RelayAddressGenerator: &turn.RelayAddressGeneratorNone{Address: "127.0.0.1"},
			},
		},
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, server.Close())
	}()

	serverPort := listener.LocalAddr().(*net.UDPAddr).Port //nolint:forcetypeassert
	turnURL := &stun.URI{
		Scheme:   stun.SchemeTypeTURN,
		Host:     "127.0.0.1",
		Port:     serverPort,
		Username: "username",
		Password: "password",
		Proto:    stun.ProtoTypeUDP,
	}

	agent, err := NewAgent(&AgentConfig{
		NetworkTypes:   []NetworkType{NetworkTypeUDP4},
		CandidateTypes: []CandidateType{CandidateTypeRelay},
		Urls:           []*stun.URI{turnURL},
	})
	skipOnPermission(t, err, "creating relay agent")
	require.NoError(t, err)
	defer func() {
		_ = agent.Close()
	}()

	var (
		mu       sync.Mutex
		relays   []Candidate
		gathered = make(chan struct{})
	)

	require.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c == nil {
			close(gathered)

			return
		}
		if c.Type() == CandidateTypeRelay {
			mu.Lock()
			relays = append(relays, c)
			mu.Unlock()
		}
	}))

	require.NoError(t, agent.GatherCandidates())

	select {
	case <-gathered:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "gatherCandidatesRelay did not finish before timeout")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(relays) == 0 {
		t.Skip("no relay candidates gathered in this environment")
	}
	for _, r := range relays {
		require.Equal(t, CandidateTypeRelay, r.Type())
		require.True(t, r.NetworkType().IsUDP())
	}
}

type relayGatherNet struct {
	addr *net.UDPAddr
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
	return net.ResolveUDPAddr(network, address)
}

func (n *relayGatherNet) ResolveTCPAddr(network, address string) (*net.TCPAddr, error) {
	return net.ResolveTCPAddr(network, address)
}

func (n *relayGatherNet) Interfaces() ([]*transport.Interface, error) {
	iface := transport.NewInterface(net.Interface{
		Index: 1,
		MTU:   1500,
		Name:  "relaytest0",
		Flags: net.FlagUp,
	})
	iface.AddAddress(&net.IPNet{IP: n.addr.IP, Mask: net.CIDRMask(24, 32)})

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
	iface := transport.NewInterface(net.Interface{
		Index: 1,
		MTU:   1500,
		Name:  "hosttest0",
		Flags: net.FlagUp,
	})
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
	iface := transport.NewInterface(net.Interface{
		Index: 1,
		MTU:   1500,
		Name:  "errturn0",
		Flags: net.FlagUp,
	})
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

func (s *stubTurnClient) Allocate() (net.PacketConn, error) {
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
		WithCandidateTypes([]CandidateType{CandidateTypeRelay}),
		WithAddressRewriteRules(
			AddressRewriteRule{
				External:        []string{"198.51.100.77"},
				Local:           "10.0.0.1",
				AsCandidateType: CandidateTypeRelay,
				Mode:            AddressRewriteReplace,
			},
		),
		WithUrls([]*stun.URI{
			{
				Scheme:   stun.SchemeTypeTURN,
				Host:     "example.com",
				Port:     3478,
				Username: "username",
				Password: "password",
				Proto:    stun.ProtoTypeUDP,
			},
		}),
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

func TestGatherCandidatesRelayUsesTurnNet(t *testing.T) {
	defer test.CheckRoutines(t)()

	stubClient := &stubTurnClient{}
	turnNet := newRelayGatherNet(&net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 50000})

	agent, err := NewAgentWithOptions(
		WithNet(turnNet),
		WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
		WithCandidateTypes([]CandidateType{CandidateTypeRelay}),
		WithUrls([]*stun.URI{
			{
				Scheme:   stun.SchemeTypeTURN,
				Host:     "example.com",
				Port:     3478,
				Username: "username",
				Password: "password",
				Proto:    stun.ProtoTypeUDP,
			},
		}),
		WithMulticastDNSMode(MulticastDNSModeDisabled),
	)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	stubClient.relayConn = newStubPacketConn(&net.UDPAddr{IP: net.IP{203, 0, 113, 9}, Port: 6000})
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

	select {
	case cand := <-candCh:
		relay, ok := cand.(*CandidateRelay)
		require.True(t, ok)
		require.Equal(t, turnNet.addr.IP.String(), relay.RelatedAddress().Address)

		addr, ok := stubClient.cfgConn.LocalAddr().(*net.UDPAddr)
		require.True(t, ok)
		require.Equal(t, turnNet.addr.IP.String(), addr.IP.String())
	case <-time.After(time.Second):
		assert.Fail(t, "expected relay candidate using turn network")
	}
}

func TestGatherCandidatesRelayDefaultClientError(t *testing.T) {
	defer test.CheckRoutines(t)()

	errConn := &errorPacketConn{addr: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}}
	agent, err := NewAgentWithOptions(
		WithNet(&errorTurnNet{pc: errConn}),
		WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
		WithCandidateTypes([]CandidateType{CandidateTypeRelay}),
		WithUrls([]*stun.URI{
			{
				Scheme:   stun.SchemeTypeTURN,
				Proto:    stun.ProtoTypeUDP,
				Host:     "127.0.0.1",
				Port:     3478,
				Username: "user",
				Password: "pass",
			},
		}),
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

	agent, err := NewAgent(&AgentConfig{
		CandidateTypes: []CandidateType{CandidateTypeRelay},
		NetworkTypes:   supportedNetworkTypes(),
		Urls: []*stun.URI{
			{
				Scheme:   stun.SchemeTypeTURN,
				Host:     localhostIPStr,
				Username: "username",
				Password: "password",
				Proto:    stun.ProtoTypeTCP,
				Port:     5000,
			},
		},
		ProxyDialer: proxyDialer,
	})
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

func buildSimpleVNet(t *testing.T) (*vnet.Router, *vnet.Net) {
	t.Helper()

	router, err := vnet.NewRouter(&vnet.RouterConfig{
		CIDR:          "1.2.3.0/24",
		LoggerFactory: logging.NewDefaultLoggerFactory(),
	})
	require.NoError(t, err)

	nw, err := vnet.NewNet(&vnet.NetConfig{})
	require.NoError(t, err)

	require.NoError(t, router.AddNet(nw))
	require.NoError(t, router.Start())

	return router, nw
}

func TestGatherCandidatesSrflxMappedPortRangeError(t *testing.T) {
	defer test.CheckRoutines(t)()

	router, nw := buildSimpleVNet(t)
	defer func() {
		require.NoError(t, router.Stop())
	}()

	agent, err := NewAgentWithOptions(
		WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
		WithCandidateTypes([]CandidateType{CandidateTypeServerReflexive}),
		WithAddressRewriteRules(AddressRewriteRule{
			External:        []string{"203.0.113.10"},
			Local:           "0.0.0.0",
			AsCandidateType: CandidateTypeServerReflexive,
		}),
		WithNet(nw),
	)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	require.NoError(t, agent.OnCandidate(func(Candidate) {}))

	agent.portMin = 9000
	agent.portMax = 8000
	agent.gatherCandidatesSrflxMapped(context.Background(), []NetworkType{NetworkTypeUDP4}, agent.gatherGeneration)

	localCandidates, err := agent.GetLocalCandidates()
	require.NoError(t, err)
	require.Len(t, localCandidates, 0)
}

func TestGatherCandidatesLocalUDPMux(t *testing.T) {
	t.Run("requires mux", func(t *testing.T) {
		agent, err := NewAgent(&AgentConfig{})
		require.NoError(t, err)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		err = agent.gatherCandidatesLocalUDPMux(context.Background(), agent.gatherGeneration)
		require.ErrorIs(t, err, errUDPMuxDisabled)
	})

	t.Run("creates host candidates from mux addresses", func(t *testing.T) {
		listenAddr := &net.UDPAddr{IP: net.IP{127, 0, 0, 1}, Port: 4789}
		udpMux := newMockUDPMux([]net.Addr{listenAddr})

		agent, err := NewAgent(&AgentConfig{
			NetworkTypes:    []NetworkType{NetworkTypeUDP4},
			CandidateTypes:  []CandidateType{CandidateTypeHost},
			UDPMux:          udpMux,
			IncludeLoopback: true,
		})
		require.NoError(t, err)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		require.NoError(t, agent.OnCandidate(func(Candidate) {}))

		err = agent.gatherCandidatesLocalUDPMux(context.Background(), agent.gatherGeneration)
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
	stunURI := &stun.URI{
		Scheme: stun.SchemeTypeSTUN,
		Host:   "127.0.0.1",
		Port:   3478,
	}
	relatedAddr := &net.UDPAddr{IP: net.IP{10, 0, 0, 1}, Port: 49000}
	srflxAddr := &stun.XORMappedAddress{
		IP:   net.IP{203, 0, 113, 5},
		Port: 50000,
	}

	udpMuxSrflx := newMockUniversalUDPMux([]net.Addr{relatedAddr}, srflxAddr)

	agent, err := NewAgent(&AgentConfig{
		NetworkTypes:   []NetworkType{NetworkTypeUDP4},
		CandidateTypes: []CandidateType{CandidateTypeServerReflexive},
		UDPMuxSrflx:    udpMuxSrflx,
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	require.NoError(t, agent.OnCandidate(func(Candidate) {}))

	agent.gatherCandidatesSrflxUDPMux(
		context.Background(), []*stun.URI{stunURI}, []NetworkType{NetworkTypeUDP4}, agent.gatherGeneration,
	)

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

// TestUDPMuxDefaultWithNAT1To1IPsUsage requires that candidates
// are given and connections are valid when using UDPMuxDefault and NAT1To1IPs.
func TestUDPMuxDefaultWithNAT1To1IPsUsage(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	conn, err := net.ListenPacket("udp4", ":0") // nolint: noctx
	require.NoError(t, err)
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
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	gatherCandidateDone := make(chan struct{})
	require.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c == nil {
			close(gatherCandidateDone)
		} else {
			require.Equal(t, "1.2.3.4", c.Address())
		}
	}))
	require.NoError(t, agent.GatherCandidates())
	<-gatherCandidateDone

	require.NotEqual(t, 0, len(mux.connsIPv4))
}

// Assert that candidates are given for each mux in a MultiUDPMux.
func TestMultiUDPMuxUsage(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	var expectedPorts []int
	var udpMuxInstances []UDPMux
	for i := 0; i < 3; i++ {
		port := randomPort(t)
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IP{127, 0, 0, 1}, Port: port})
		require.NoError(t, err)
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

	agent, err := NewAgent(&AgentConfig{
		NetworkTypes:   []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
		CandidateTypes: []CandidateType{CandidateTypeHost},
		UDPMux:         NewMultiUDPMuxDefault(udpMuxInstances...),
	})
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

func closedStartedCh() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)

	return ch
}

func TestResolveRelayAddresses(t *testing.T) {
	logger := logging.NewDefaultLoggerFactory().NewLogger("test")

	t.Run("no mapping", func(t *testing.T) {
		agent := &Agent{log: logger}
		ep := relayEndpoint{address: net.IPv4(10, 0, 0, 10), relAddr: "198.51.100.1"}

		addrs, ok := agent.resolveRelayAddresses(ep)
		assert.True(t, ok)
		assert.Equal(t, []net.IP{ep.address}, addrs)
	})

	t.Run("append mode adds mapped address", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        []string{"203.0.113.10"},
				Local:           "198.51.100.1",
				AsCandidateType: CandidateTypeRelay,
			},
		})
		require.NoError(t, err)
		agent := &Agent{
			addressRewriteMapper: mapper,
			log:                  logger,
		}
		ep := relayEndpoint{address: net.IPv4(10, 0, 0, 10), relAddr: "198.51.100.1"}

		addrs, ok := agent.resolveRelayAddresses(ep)
		assert.True(t, ok)
		require.Len(t, addrs, 2)
		assert.Equal(t, "10.0.0.10", addrs[0].String())
		assert.Equal(t, "203.0.113.10", addrs[1].String())
	})

	t.Run("replace mode swaps to mapped", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        []string{"203.0.113.20"},
				Local:           "198.51.100.2",
				AsCandidateType: CandidateTypeRelay,
				Mode:            AddressRewriteReplace,
			},
		})
		require.NoError(t, err)
		agent := &Agent{
			addressRewriteMapper: mapper,
			log:                  logger,
		}
		ep := relayEndpoint{address: net.IPv4(10, 0, 0, 11), relAddr: "198.51.100.2"}

		addrs, ok := agent.resolveRelayAddresses(ep)
		assert.True(t, ok)
		require.Len(t, addrs, 1)
		assert.Equal(t, "203.0.113.20", addrs[0].String())
	})

	t.Run("replace match with zero external drops", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        nil,
				Local:           "198.51.100.4",
				AsCandidateType: CandidateTypeRelay,
				Mode:            AddressRewriteReplace,
			},
		})
		require.NoError(t, err)
		agent := &Agent{
			addressRewriteMapper: mapper,
			log:                  logger,
		}
		ep := relayEndpoint{address: net.IPv4(10, 0, 0, 13), relAddr: "198.51.100.4"}

		addrs, ok := agent.resolveRelayAddresses(ep)
		assert.False(t, ok)
		assert.Empty(t, addrs)
	})

	t.Run("append match with zero external keeps original", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        nil,
				Local:           "198.51.100.5",
				AsCandidateType: CandidateTypeRelay,
				Mode:            AddressRewriteAppend,
			},
		})
		require.NoError(t, err)
		agent := &Agent{
			addressRewriteMapper: mapper,
			log:                  logger,
		}
		ep := relayEndpoint{address: net.IPv4(10, 0, 0, 14), relAddr: "198.51.100.5"}

		addrs, ok := agent.resolveRelayAddresses(ep)
		assert.True(t, ok)
		require.Len(t, addrs, 1)
		assert.Equal(t, "10.0.0.14", addrs[0].String())
	})

	t.Run("invalid relAddr returns error", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        []string{"203.0.113.30"},
				AsCandidateType: CandidateTypeRelay,
			},
		})
		require.NoError(t, err)
		agent := &Agent{
			addressRewriteMapper: mapper,
			log:                  logger,
		}
		ep := relayEndpoint{address: net.IPv4(10, 0, 0, 13), relAddr: "not-an-ip"}

		addrs, ok := agent.resolveRelayAddresses(ep)
		assert.False(t, ok)
		assert.Nil(t, addrs)
	})

	t.Run("mapper present but unmatched keeps original", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        []string{"203.0.113.40"},
				Local:           "198.51.100.4",
				AsCandidateType: CandidateTypeRelay,
			},
		})
		require.NoError(t, err)
		agent := &Agent{
			addressRewriteMapper: mapper,
			log:                  logger,
		}
		ep := relayEndpoint{address: net.IPv4(10, 0, 0, 14), relAddr: "198.51.100.5"}

		addrs, ok := agent.resolveRelayAddresses(ep)
		assert.True(t, ok)
		require.Len(t, addrs, 1)
		assert.Equal(t, "10.0.0.14", addrs[0].String())
	})

	t.Run("relay rewrite respects iface filter", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        []string{"203.0.113.41"},
				Local:           "198.51.100.6",
				AsCandidateType: CandidateTypeRelay,
				Iface:           "hosttest0",
				Mode:            AddressRewriteReplace,
			},
		})
		require.NoError(t, err)
		agent := &Agent{
			addressRewriteMapper: mapper,
			log:                  logger,
			net:                  newHostGatherNet(&net.UDPAddr{IP: net.IPv4(198, 51, 100, 6)}),
		}
		ep := relayEndpoint{address: net.IPv4(10, 0, 0, 41), relAddr: "198.51.100.6", iface: "hosttest0"}

		addrs, ok := agent.resolveRelayAddresses(ep)
		assert.True(t, ok)
		require.Len(t, addrs, 1)
		assert.Equal(t, "203.0.113.41", addrs[0].String())

		agent.addressRewriteMapper.rulesByCandidateType[CandidateTypeRelay][0].rule.Iface = "other0"
		addrs, ok = agent.resolveRelayAddresses(ep)
		assert.True(t, ok)
		require.Len(t, addrs, 1)
		assert.Equal(t, "10.0.0.41", addrs[0].String())
	})
}

func TestResolveHostAndSrflxFallbacks(t *testing.T) { //nolint:maintidx
	logger := logging.NewDefaultLoggerFactory().NewLogger("test")

	t.Run("host no rule keeps original", func(t *testing.T) {
		agent := &Agent{
			addressRewriteMapper: &addressRewriteMapper{
				rulesByCandidateType: make(map[CandidateType][]*addressRewriteRuleMapping),
			},
			log: logger,
		}

		addr := netip.MustParseAddr("10.0.0.45")
		mapped, ok := agent.applyHostAddressRewrite(addr, []netip.Addr{addr}, "")
		assert.True(t, ok)
		require.Len(t, mapped, 1)
		assert.Equal(t, addr, mapped[0])
	})

	t.Run("host replace unmatched keeps original", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        []string{"203.0.113.50"},
				Local:           "198.51.100.50",
				AsCandidateType: CandidateTypeHost,
			},
		})
		require.NoError(t, err)

		agent := &Agent{
			addressRewriteMapper: mapper,
			log:                  logger,
		}

		addr := netip.MustParseAddr("10.0.0.50")
		mapped, ok := agent.applyHostAddressRewrite(addr, []netip.Addr{addr}, "")
		assert.True(t, ok)
		require.Len(t, mapped, 1)
		assert.Equal(t, addr, mapped[0])
	})

	t.Run("host replace match with zero external drops", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        nil,
				Local:           "10.0.0.51",
				AsCandidateType: CandidateTypeHost,
				Mode:            AddressRewriteReplace,
			},
		})
		require.NoError(t, err)

		agent := &Agent{
			addressRewriteMapper: mapper,
			log:                  logger,
		}

		addr := netip.MustParseAddr("10.0.0.51")
		mapped, ok := agent.applyHostAddressRewrite(addr, []netip.Addr{addr}, "")
		assert.False(t, ok)
		assert.Empty(t, mapped)
	})

	t.Run("host append match with zero external keeps original", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        nil,
				Local:           "10.0.0.52",
				AsCandidateType: CandidateTypeHost,
				Mode:            AddressRewriteAppend,
			},
		})
		require.NoError(t, err)

		agent := &Agent{
			addressRewriteMapper: mapper,
			log:                  logger,
		}

		addr := netip.MustParseAddr("10.0.0.52")
		mapped, ok := agent.applyHostAddressRewrite(addr, []netip.Addr{addr}, "")
		assert.True(t, ok)
		require.Len(t, mapped, 1)
		assert.Equal(t, addr, mapped[0])
	})

	t.Run("host rewrite respects iface filter", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        []string{"203.0.113.53"},
				Local:           "10.0.0.53",
				AsCandidateType: CandidateTypeHost,
				Iface:           "hosttest0",
			},
		})
		require.NoError(t, err)

		agent := &Agent{
			addressRewriteMapper: mapper,
			log:                  logger,
		}

		addr := netip.MustParseAddr("10.0.0.53")
		mapped, ok := agent.applyHostAddressRewrite(addr, []netip.Addr{addr}, "hosttest0")
		assert.True(t, ok)
		require.Len(t, mapped, 1)
		assert.Equal(t, "203.0.113.53", mapped[0].String())

		mapped, ok = agent.applyHostAddressRewrite(addr, []netip.Addr{addr}, "other0")
		assert.True(t, ok)
		require.Len(t, mapped, 1)
		assert.Equal(t, addr, mapped[0])
	})

	t.Run("srflx replace unmatched keeps original", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        []string{"198.51.100.60"},
				Local:           "203.0.113.60",
				AsCandidateType: CandidateTypeServerReflexive,
				Mode:            AddressRewriteReplace,
			},
		})
		require.NoError(t, err)
		agent := &Agent{
			addressRewriteMapper: mapper,
			log:                  logger,
		}

		localIP := net.IPv4(192, 0, 2, 60)
		addrs, ok := agent.resolveSrflxAddresses(localIP, "hosttest0")
		assert.True(t, ok)
		require.Len(t, addrs, 1)
		assert.Equal(t, localIP.String(), addrs[0].String())
	})

	t.Run("srflx replace match with zero external drops", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        nil,
				Local:           "192.0.2.70",
				AsCandidateType: CandidateTypeServerReflexive,
				Mode:            AddressRewriteReplace,
			},
		})
		require.NoError(t, err)
		agent := &Agent{
			addressRewriteMapper: mapper,
			log:                  logger,
		}

		localIP := net.IPv4(192, 0, 2, 70)
		addrs, ok := agent.resolveSrflxAddresses(localIP, "hosttest0")
		assert.False(t, ok)
		assert.Empty(t, addrs)
	})

	t.Run("srflx append match with zero external keeps original", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        nil,
				Local:           "192.0.2.71",
				AsCandidateType: CandidateTypeServerReflexive,
				Mode:            AddressRewriteAppend,
			},
		})
		require.NoError(t, err)
		agent := &Agent{
			addressRewriteMapper: mapper,
			log:                  logger,
		}

		localIP := net.IPv4(192, 0, 2, 71)
		addrs, ok := agent.resolveSrflxAddresses(localIP, "hosttest0")
		assert.True(t, ok)
		require.Len(t, addrs, 1)
		assert.Equal(t, localIP.String(), addrs[0].String())
	})

	t.Run("srflx rewrite applies only on matching iface", func(t *testing.T) {
		localIP := net.IPv4(192, 0, 2, 90)
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        []string{"198.51.100.90"},
				Local:           localIP.String(),
				AsCandidateType: CandidateTypeServerReflexive,
				Iface:           "hosttest0",
				Mode:            AddressRewriteReplace,
			},
		})
		require.NoError(t, err)
		agent := &Agent{
			addressRewriteMapper: mapper,
			log:                  logger,
			net:                  newHostGatherNet(&net.UDPAddr{IP: localIP}),
		}

		addrs, ok := agent.resolveSrflxAddresses(localIP, "hosttest0")
		assert.True(t, ok)
		require.Len(t, addrs, 1)
		assert.Equal(t, "198.51.100.90", addrs[0].String())

		mapper.rulesByCandidateType[CandidateTypeServerReflexive][0].rule.Iface = "other0"
		addrs, ok = agent.resolveSrflxAddresses(localIP, "hosttest0")
		assert.True(t, ok)
		require.Len(t, addrs, 1)
		assert.Equal(t, localIP.String(), addrs[0].String())
	})

	t.Run("srflx append catch-all with zero external keeps original", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        nil,
				AsCandidateType: CandidateTypeServerReflexive,
				Mode:            AddressRewriteAppend,
			},
		})
		require.NoError(t, err)
		agent := &Agent{
			addressRewriteMapper: mapper,
			log:                  logger,
		}

		localIP := net.IPv4(192, 0, 2, 72)
		addrs, ok := agent.resolveSrflxAddresses(localIP, "")
		assert.True(t, ok)
		require.Len(t, addrs, 1)
		assert.Equal(t, localIP.String(), addrs[0].String())
	})

	t.Run("srflx no mapper returns original", func(t *testing.T) {
		agent := &Agent{
			addressRewriteMapper: nil,
			log:                  logger,
		}

		localIP := net.IPv4(192, 0, 2, 90)
		addrs, ok := agent.resolveSrflxAddresses(localIP, "")
		assert.True(t, ok)
		require.Len(t, addrs, 1)
		assert.Equal(t, localIP.String(), addrs[0].String())
	})

	t.Run("srflx replace with zero externals drops", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        nil,
				Local:           "192.0.2.91",
				AsCandidateType: CandidateTypeServerReflexive,
				Mode:            AddressRewriteReplace,
			},
		})
		require.NoError(t, err)
		agent := &Agent{
			addressRewriteMapper: mapper,
			log:                  logger,
		}

		localIP := net.IPv4(192, 0, 2, 91)
		addrs, ok := agent.resolveSrflxAddresses(localIP, "")
		assert.False(t, ok)
		assert.Nil(t, addrs)
	})

	t.Run("srflx invalid local ip returns false", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        []string{"198.51.100.99"},
				AsCandidateType: CandidateTypeServerReflexive,
			},
		})
		require.NoError(t, err)
		agent := &Agent{
			addressRewriteMapper: mapper,
			log:                  logger,
		}

		addrs, ok := agent.resolveSrflxAddresses(nil, "")
		assert.False(t, ok)
		assert.Nil(t, addrs)
	})

	t.Run("relay unmatched keeps original", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        []string{"203.0.113.70"},
				Local:           "198.51.100.70",
				AsCandidateType: CandidateTypeRelay,
			},
		})
		require.NoError(t, err)

		agent := &Agent{
			addressRewriteMapper: mapper,
			log:                  logger,
		}

		ep := relayEndpoint{address: net.IPv4(10, 0, 0, 70), relAddr: "198.51.100.71"}
		addrs, ok := agent.resolveRelayAddresses(ep)
		assert.True(t, ok)
		require.Len(t, addrs, 1)
		assert.Equal(t, "10.0.0.70", addrs[0].String())
	})
}

func TestCatchAllRewriteApplied(t *testing.T) {
	logger := logging.NewDefaultLoggerFactory().NewLogger("test")

	t.Run("host catch-all replaces", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        []string{"203.0.113.80"},
				AsCandidateType: CandidateTypeHost,
			},
		})
		require.NoError(t, err)

		agent := &Agent{
			addressRewriteMapper: mapper,
			log:                  logger,
		}

		addr := netip.MustParseAddr("10.0.0.80")
		mapped, ok := agent.applyHostAddressRewrite(addr, []netip.Addr{addr}, "")
		assert.True(t, ok)
		require.Len(t, mapped, 1)
		assert.Equal(t, "203.0.113.80", mapped[0].String())
	})

	t.Run("srflx catch-all appends mapped only", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        []string{"203.0.113.81"},
				AsCandidateType: CandidateTypeServerReflexive,
			},
		})
		require.NoError(t, err)

		agent := &Agent{
			addressRewriteMapper: mapper,
			log:                  logger,
		}

		local := net.IPv4(10, 0, 0, 81)
		addrs, ok := agent.resolveSrflxAddresses(local, "")
		assert.True(t, ok)
		require.Len(t, addrs, 1)
		assert.Equal(t, "203.0.113.81", addrs[0].String())
	})

	t.Run("relay catch-all appends", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        []string{"203.0.113.82"},
				AsCandidateType: CandidateTypeRelay,
			},
		})
		require.NoError(t, err)

		agent := &Agent{
			addressRewriteMapper: mapper,
			log:                  logger,
		}

		ep := relayEndpoint{address: net.IPv4(10, 0, 0, 82), relAddr: "0.0.0.0"}
		addrs, ok := agent.resolveRelayAddresses(ep)
		assert.True(t, ok)
		require.Len(t, addrs, 2)
		assert.Equal(t, "10.0.0.82", addrs[0].String())
		assert.Equal(t, "203.0.113.82", addrs[1].String())
	})
}

func TestAddRelayCandidatesWithRewrite(t *testing.T) {
	mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
		{
			External:        []string{"203.0.113.77"},
			Local:           "198.51.100.77",
			AsCandidateType: CandidateTypeRelay,
		},
	})
	require.NoError(t, err)

	agent := &Agent{
		addressRewriteMapper: mapper,
		log:                  logging.NewDefaultLoggerFactory().NewLogger("test"),
		loop:                 taskloop.New(func() {}),
		localCandidates:      make(map[NetworkType][]Candidate),
		remoteCandidates:     make(map[NetworkType][]Candidate),
		startedCh:            closedStartedCh(),
		candidateNotifier: &handlerNotifier{
			candidateFunc: func(Candidate) {},
			done:          make(chan struct{}),
		},
	}

	ep := relayEndpoint{
		network: "udp",
		address: net.IPv4(10, 0, 0, 50),
		port:    3478,
		relAddr: "198.51.100.77",
		relPort: 50000,
		conn:    newStubPacketConn(nil),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t.Cleanup(func() {
		agent.loop.Close()
	})

	agent.addRelayCandidates(ctx, agent.gatherGeneration, ep)

	cands := agent.localCandidates[NetworkTypeUDP4]
	require.Len(t, cands, 2)
	assert.Equal(t, "10.0.0.50", cands[0].Address())
	assert.Equal(t, "203.0.113.77", cands[1].Address())
}

func TestAddRelayCandidatesSkipsNilConnOrAddress(t *testing.T) {
	agent := &Agent{
		log:              logging.NewDefaultLoggerFactory().NewLogger("test"),
		localCandidates:  make(map[NetworkType][]Candidate),
		remoteCandidates: make(map[NetworkType][]Candidate),
		startedCh:        closedStartedCh(),
		candidateNotifier: &handlerNotifier{
			candidateFunc: func(Candidate) {},
			done:          make(chan struct{}),
		},
		loop: taskloop.New(func() {}),
	}
	t.Cleanup(func() {
		agent.loop.Close()
	})

	ctx := context.Background()

	agent.addRelayCandidates(ctx, agent.gatherGeneration, relayEndpoint{
		network: NetworkTypeUDP4.String(),
		address: net.IPv4(10, 0, 0, 1),
		port:    3478,
		relAddr: "198.51.100.1",
		relPort: 5000,
		conn:    nil,
	})
	cands, err := agent.GetLocalCandidates()
	require.NoError(t, err)
	assert.Len(t, cands, 0)

	agent.addRelayCandidates(ctx, agent.gatherGeneration, relayEndpoint{
		network: NetworkTypeUDP4.String(),
		address: nil,
		port:    3478,
		relAddr: "198.51.100.1",
		relPort: 5000,
		conn:    newStubPacketConn(nil),
	})
	cands, err = agent.GetLocalCandidates()
	require.NoError(t, err)
	assert.Len(t, cands, 0)
}

func TestAddRelayCandidatesSkipsWhenResolveFails(t *testing.T) {
	t.Run("replace with zero externals drops", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        nil,
				Local:           "198.51.100.2",
				AsCandidateType: CandidateTypeRelay,
				Mode:            AddressRewriteReplace,
			},
		})
		require.NoError(t, err)

		agent := &Agent{
			addressRewriteMapper: mapper,
			log:                  logging.NewDefaultLoggerFactory().NewLogger("test"),
			localCandidates:      make(map[NetworkType][]Candidate),
			remoteCandidates:     make(map[NetworkType][]Candidate),
			startedCh:            closedStartedCh(),
			candidateNotifier: &handlerNotifier{
				candidateFunc: func(Candidate) {},
				done:          make(chan struct{}),
			},
			loop: taskloop.New(func() {}),
		}
		t.Cleanup(func() {
			agent.loop.Close()
		})

		agent.addRelayCandidates(context.Background(), agent.gatherGeneration, relayEndpoint{
			network: NetworkTypeUDP4.String(),
			address: net.IPv4(10, 0, 0, 2),
			port:    3478,
			relAddr: "198.51.100.2",
			relPort: 5000,
			conn:    newStubPacketConn(nil),
		})

		cands, err := agent.GetLocalCandidates()
		require.NoError(t, err)
		assert.Len(t, cands, 0)
	})

	t.Run("invalid relAddr causes skip", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        []string{"203.0.113.10"},
				AsCandidateType: CandidateTypeRelay,
			},
		})
		require.NoError(t, err)

		agent := &Agent{
			addressRewriteMapper: mapper,
			log:                  logging.NewDefaultLoggerFactory().NewLogger("test"),
			localCandidates:      make(map[NetworkType][]Candidate),
			remoteCandidates:     make(map[NetworkType][]Candidate),
			startedCh:            closedStartedCh(),
			candidateNotifier: &handlerNotifier{
				candidateFunc: func(Candidate) {},
				done:          make(chan struct{}),
			},
			loop: taskloop.New(func() {}),
		}
		t.Cleanup(func() {
			agent.loop.Close()
		})

		agent.addRelayCandidates(context.Background(), agent.gatherGeneration, relayEndpoint{
			network: NetworkTypeUDP4.String(),
			address: net.IPv4(10, 0, 0, 3),
			port:    3478,
			relAddr: "not-an-ip",
			relPort: 5000,
			conn:    newStubPacketConn(nil),
		})

		cands, err := agent.GetLocalCandidates()
		require.NoError(t, err)
		assert.Len(t, cands, 0)
	})
}

func TestCreateRelayCandidateErrorPaths(t *testing.T) {
	t.Run("NewCandidateRelay failure skips and closes conn", func(t *testing.T) {
		var closed bool
		agent := &Agent{
			log:              logging.NewDefaultLoggerFactory().NewLogger("test"),
			localCandidates:  make(map[NetworkType][]Candidate),
			remoteCandidates: make(map[NetworkType][]Candidate),
			startedCh:        closedStartedCh(),
			candidateNotifier: &handlerNotifier{
				candidateFunc: func(Candidate) {},
				done:          make(chan struct{}),
			},
			loop: taskloop.New(func() {}),
		}
		t.Cleanup(func() {
			agent.loop.Close()
		})

		ep := relayEndpoint{
			network: "bogus-network",
			address: net.IPv4(10, 0, 0, 4),
			port:    3478,
			relAddr: "198.51.100.4",
			relPort: 5000,
			conn:    newStubPacketConn(nil),
			closeConn: func() {
				closed = true
			},
		}

		agent.addRelayCandidates(context.Background(), agent.gatherGeneration, ep)

		cands, err := agent.GetLocalCandidates()
		require.NoError(t, err)
		assert.Empty(t, cands)
		assert.True(t, closed)
	})

	t.Run("addCandidate failure triggers candidate close", func(t *testing.T) {
		var onCloseCalled int
		agent := &Agent{
			log:              logging.NewDefaultLoggerFactory().NewLogger("test"),
			localCandidates:  make(map[NetworkType][]Candidate),
			remoteCandidates: make(map[NetworkType][]Candidate),
			startedCh:        closedStartedCh(),
			candidateNotifier: &handlerNotifier{
				candidateFunc: func(Candidate) {},
				done:          make(chan struct{}),
			},
			loop: taskloop.New(func() {}),
		}
		t.Cleanup(func() {
			agent.loop.Close()
		})

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // force addCandidate to fail

		agent.addRelayCandidates(ctx, agent.gatherGeneration, relayEndpoint{
			network: NetworkTypeUDP4.String(),
			address: net.IPv4(10, 0, 0, 5),
			port:    3478,
			relAddr: "198.51.100.5",
			relPort: 5000,
			conn:    newStubPacketConn(nil),
			onClose: func() error {
				onCloseCalled++

				return fmt.Errorf("close err") //nolint:err113
			},
		})

		cands, err := agent.GetLocalCandidates()
		require.NoError(t, err)
		assert.Empty(t, cands)
		assert.Equal(t, 1, onCloseCalled)
	})
}

func TestGatherCandidatesLocalTCPMuxSkipsUnboundInterfaces(t *testing.T) {
	tcpMux := &boundTCPMux{
		localAddr: &net.TCPAddr{IP: net.ParseIP("203.0.113.10"), Port: 5555},
	}
	agent, err := NewAgentWithOptions(
		WithNet(newHostGatherNet(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})),
		WithCandidateTypes([]CandidateType{CandidateTypeHost}),
		WithNetworkTypes([]NetworkType{NetworkTypeTCP4}),
		WithTCPMux(tcpMux),
		WithIncludeLoopback(),
		WithMulticastDNSMode(MulticastDNSModeDisabled),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, agent.Close())
	})
	require.NoError(t, agent.OnCandidate(func(Candidate) {}))

	agent.gatherCandidatesLocal(context.Background(), []NetworkType{NetworkTypeTCP4}, agent.gatherGeneration)

	cands, err := agent.GetLocalCandidates()
	require.NoError(t, err)
	assert.Empty(t, cands)
}

func TestGatherCandidatesLocalHostErrorPaths(t *testing.T) {
	t.Run("UDPMux invalid address closes conn", func(t *testing.T) {
		mux := newInvalidAddrUDPMux()
		rec := &recordingLogger{}
		agent, err := NewAgentWithOptions(
			WithNet(newHostGatherNet(nil)),
			WithCandidateTypes([]CandidateType{CandidateTypeHost}),
			WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
			WithUDPMux(mux),
			WithMulticastDNSMode(MulticastDNSModeDisabled),
			WithLoggerFactory(&recordingLoggerFactory{logger: rec}),
		)
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, agent.Close())
		})
		require.NoError(t, agent.OnCandidate(func(Candidate) {}))

		assert.NoError(t, agent.gatherCandidatesLocalUDPMux(context.Background(), agent.gatherGeneration))

		assert.True(t, mux.conn.closed)
		cands, err := agent.GetLocalCandidates()
		require.NoError(t, err)
		assert.Empty(t, cands)
		assert.Greater(t, len(rec.warnings), 0)
	})

	t.Run("NewCandidateHost failure logs and closes conn", func(t *testing.T) {
		rec := &recordingLogger{}
		agent, err := NewAgentWithOptions(
			WithNet(newHostGatherNet(nil)),
			WithCandidateTypes([]CandidateType{CandidateTypeHost}),
			WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
			WithMulticastDNSMode(MulticastDNSModeQueryAndGather),
			WithLoggerFactory(&recordingLoggerFactory{logger: rec}),
		)
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, agent.Close())
		})
		require.NoError(t, agent.OnCandidate(func(Candidate) {}))
		agent.includeLoopback = true
		agent.mDNSName = "invalid-mdns" // no .local suffix -> NewCandidateHost parse fails

		agent.gatherCandidatesLocal(context.Background(), []NetworkType{NetworkTypeUDP4}, agent.gatherGeneration)

		cands, err := agent.GetLocalCandidates()
		require.NoError(t, err)
		assert.Empty(t, cands)
		assert.Greater(t, len(rec.warnings), 0)
	})

	t.Run("addCandidate error logs and keeps no candidates", func(t *testing.T) {
		rec := &recordingLogger{}
		agent, err := NewAgentWithOptions(
			WithNet(newHostGatherNet(nil)),
			WithCandidateTypes([]CandidateType{CandidateTypeHost}),
			WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
			WithMulticastDNSMode(MulticastDNSModeDisabled),
			WithLoggerFactory(&recordingLoggerFactory{logger: rec}),
		)
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, agent.Close())
		})
		require.NoError(t, agent.OnCandidate(func(Candidate) {}))
		agent.includeLoopback = true

		agent.loop.Close()

		agent.gatherCandidatesLocal(context.Background(), []NetworkType{NetworkTypeUDP4}, agent.gatherGeneration)

		agent.loop.Run(agent.loop, func(context.Context) { //nolint:errcheck,gosec
			assert.Empty(t, agent.localCandidates[NetworkTypeUDP4])
		})
		assert.Greater(t, len(rec.warnings), 0)
	})

	t.Run("host rewrite replace with zero externals skips candidate", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        nil,
				Local:           "192.0.2.10",
				AsCandidateType: CandidateTypeHost,
				Mode:            AddressRewriteReplace,
			},
		})
		require.NoError(t, err)

		agent := &Agent{
			net:                  newHostGatherNet(&net.UDPAddr{IP: net.IPv4(192, 0, 2, 10)}),
			networkTypes:         []NetworkType{NetworkTypeUDP4},
			includeLoopback:      true,
			mDNSMode:             MulticastDNSModeDisabled,
			addressRewriteMapper: mapper,
			localCandidates:      make(map[NetworkType][]Candidate),
			remoteCandidates:     make(map[NetworkType][]Candidate),
			log:                  logging.NewDefaultLoggerFactory().NewLogger("test"),
		}
		agent.loop = taskloop.New(func() {})
		t.Cleanup(func() {
			agent.loop.Close()
		})

		agent.gatherCandidatesLocal(context.Background(), []NetworkType{NetworkTypeUDP4}, agent.gatherGeneration)

		cands, err := agent.GetLocalCandidates()
		require.NoError(t, err)
		assert.Empty(t, cands)
	})

	runUDPMuxRewrite := func(
		name string, rule AddressRewriteRule, ip net.IP, mux UDPMux, expectLen int, expectAddrs []string,
	) {
		t.Run(name, func(t *testing.T) {
			rec := &recordingLogger{}
			mapper, err := newAddressRewriteMapper([]AddressRewriteRule{rule})
			require.NoError(t, err)
			agent, err := NewAgentWithOptions(
				WithNet(newHostGatherNet(&net.UDPAddr{IP: ip})),
				WithCandidateTypes([]CandidateType{CandidateTypeHost}),
				WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
				WithUDPMux(mux),
				WithMulticastDNSMode(MulticastDNSModeDisabled),
				WithLoggerFactory(&recordingLoggerFactory{logger: rec}),
			)
			require.NoError(t, err)
			agent.addressRewriteMapper = mapper
			t.Cleanup(func() {
				require.NoError(t, agent.Close())
			})
			require.NoError(t, agent.OnCandidate(func(Candidate) {}))

			require.NoError(t, agent.gatherCandidatesLocalUDPMux(context.Background(), agent.gatherGeneration))

			cands, err := agent.GetLocalCandidates()
			require.NoError(t, err)
			if expectLen == 0 {
				assert.Empty(t, cands)
			} else {
				require.Len(t, cands, expectLen)
				got := []string{}
				for _, c := range cands {
					got = append(got, c.Address())
				}
				assert.ElementsMatch(t, expectAddrs, got)
			}
		})
	}

	runUDPMuxRewrite(
		"UDPMux append with zero externals logs and keeps original",
		AddressRewriteRule{
			External:        nil,
			Local:           "10.0.0.11",
			AsCandidateType: CandidateTypeHost,
			Mode:            AddressRewriteAppend,
		},
		net.IPv4(10, 0, 0, 11),
		newMockUDPMux([]net.Addr{&net.UDPAddr{IP: net.IP{10, 0, 0, 11}, Port: 1234}}),
		1,
		[]string{"10.0.0.11"},
	)

	runUDPMuxRewrite(
		"UDPMux replace with zero externals logs and drops",
		AddressRewriteRule{
			External:        nil,
			Local:           "10.0.0.12",
			AsCandidateType: CandidateTypeHost,
			Mode:            AddressRewriteReplace,
		},
		net.IPv4(10, 0, 0, 12),
		newMockUDPMux([]net.Addr{&net.UDPAddr{IP: net.IP{10, 0, 0, 12}, Port: 1234}}),
		0,
		nil,
	)

	runUDPMuxRewrite(
		"UDPMux findExternalIPs error logs and drops",
		AddressRewriteRule{
			External:        []string{"203.0.113.9"},
			AsCandidateType: CandidateTypeHost,
			Mode:            AddressRewriteReplace,
		},
		net.IPv4zero,
		newInvalidAddrUDPMux(),
		0,
		nil,
	)
}

func TestApplyHostRewriteForUDPMuxErrors(t *testing.T) {
	rec := &recordingLogger{}
	mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
		{
			External:        nil,
			Local:           "10.0.0.50",
			AsCandidateType: CandidateTypeHost,
			Mode:            AddressRewriteReplace,
		},
	})
	require.NoError(t, err)

	agent := &Agent{
		addressRewriteMapper: mapper,
		log:                  rec,
	}

	in := []net.IP{net.IPv4(10, 0, 0, 50)}
	out, ok := agent.applyHostRewriteForUDPMux(in, &net.UDPAddr{IP: net.IPv4(10, 0, 0, 50), Port: 1234})
	assert.False(t, ok)
	assert.Equal(t, in, out)
}

// Assert that candidates are given for each mux in a MultiTCPMux.
func TestMultiTCPMuxUsage(t *testing.T) {
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
		require.NoError(t, err)
		defer func() {
			_ = listener.Close()
		}()

		expectedPorts = append(expectedPorts, port)
		tcpMux := NewTCPMuxDefault(TCPMuxParams{
			Listener:       listener,
			ReadBufferSize: 8,
		})
		defer func() {
			_ = tcpMux.Close()
		}()
		tcpMuxInstances = append(tcpMuxInstances, tcpMux)
	}

	agent, err := NewAgent(&AgentConfig{
		NetworkTypes:   supportedNetworkTypes(),
		CandidateTypes: []CandidateType{CandidateTypeHost},
		TCPMux:         NewMultiTCPMuxDefault(tcpMuxInstances...),
	})
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

func TestGatherAddressRewriteHostModes(t *testing.T) { //nolint:cyclop
	t.Run("replace host via UDPMux", func(t *testing.T) {
		mux := newMockUDPMux([]net.Addr{&net.UDPAddr{IP: net.IP{10, 0, 0, 1}, Port: 1234}})

		agent, err := NewAgentWithOptions(
			WithNet(newStubNet(t)),
			WithCandidateTypes([]CandidateType{CandidateTypeHost}),
			WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
			WithUDPMux(mux),
			WithMulticastDNSMode(MulticastDNSModeDisabled),
			WithAddressRewriteRules(AddressRewriteRule{
				External:        []string{"203.0.113.1"},
				Local:           "10.0.0.1",
				AsCandidateType: CandidateTypeHost,
				Mode:            AddressRewriteReplace,
			}),
		)
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, agent.Close())
		})

		var (
			mu        sync.Mutex
			addresses []Candidate
			done      = make(chan struct{})
		)
		require.NoError(t, agent.OnCandidate(func(c Candidate) {
			if c == nil {
				close(done)

				return
			}
			mu.Lock()
			addresses = append(addresses, c)
			mu.Unlock()
		}))

		require.NoError(t, agent.GatherCandidates())
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			require.FailNow(t, "gather did not complete")
		}

		mu.Lock()
		defer mu.Unlock()
		require.Len(t, addresses, 1)
		assert.Equal(t, "203.0.113.1", addresses[0].Address())
		assert.Equal(t, CandidateTypeHost, addresses[0].Type())
	})

	t.Run("append host via UDPMux", func(t *testing.T) {
		mux := newMockUDPMux([]net.Addr{&net.UDPAddr{IP: net.IP{10, 0, 0, 1}, Port: 1234}})

		agent, err := NewAgentWithOptions(
			WithNet(newStubNet(t)),
			WithCandidateTypes([]CandidateType{CandidateTypeHost}),
			WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
			WithUDPMux(mux),
			WithMulticastDNSMode(MulticastDNSModeDisabled),
			WithAddressRewriteRules(AddressRewriteRule{
				External:        []string{"203.0.113.2"},
				Local:           "10.0.0.1",
				AsCandidateType: CandidateTypeHost,
				Mode:            AddressRewriteAppend,
			}),
		)
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, agent.Close())
		})

		var (
			mu        sync.Mutex
			addresses []Candidate
			done      = make(chan struct{})
		)
		require.NoError(t, agent.OnCandidate(func(c Candidate) {
			if c == nil {
				close(done)

				return
			}
			mu.Lock()
			addresses = append(addresses, c)
			mu.Unlock()
		}))

		require.NoError(t, agent.GatherCandidates())
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			require.FailNow(t, "gather did not complete")
		}

		mu.Lock()
		defer mu.Unlock()
		require.Len(t, addresses, 2)
		seenAddrs := []string{addresses[0].Address(), addresses[1].Address()}
		assert.ElementsMatch(t, []string{"10.0.0.1", "203.0.113.2"}, seenAddrs)
		for _, cand := range addresses {
			assert.Equal(t, CandidateTypeHost, cand.Type())
		}
	})

	t.Run("replace host via UDPMux with empty mapping drops candidate", func(t *testing.T) {
		mux := newMockUDPMux([]net.Addr{&net.UDPAddr{IP: net.IP{10, 0, 0, 2}, Port: 1234}})

		agent, err := NewAgentWithOptions(
			WithNet(newStubNet(t)),
			WithCandidateTypes([]CandidateType{CandidateTypeHost}),
			WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
			WithUDPMux(mux),
			WithMulticastDNSMode(MulticastDNSModeDisabled),
		)
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, agent.Close())
		})

		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        nil,
				Local:           "10.0.0.2",
				AsCandidateType: CandidateTypeHost,
				Mode:            AddressRewriteReplace,
			},
		})
		require.NoError(t, err)
		agent.addressRewriteMapper = mapper

		var (
			mu        sync.Mutex
			addresses []Candidate
			done      = make(chan struct{})
		)
		require.NoError(t, agent.OnCandidate(func(c Candidate) {
			if c == nil {
				close(done)

				return
			}
			mu.Lock()
			addresses = append(addresses, c)
			mu.Unlock()
		}))

		require.NoError(t, agent.GatherCandidates())
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			require.FailNow(t, "gather did not complete")
		}

		mu.Lock()
		defer mu.Unlock()
		assert.Empty(t, addresses)
		assert.Equal(t, 0, mux.connCount())
	})

	t.Run("append host via UDPMux with missing externals keeps original", func(t *testing.T) {
		mux := newMockUDPMux([]net.Addr{&net.UDPAddr{IP: net.IP{10, 0, 0, 3}, Port: 1234}})

		agent, err := NewAgentWithOptions(
			WithNet(newStubNet(t)),
			WithCandidateTypes([]CandidateType{CandidateTypeHost}),
			WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
			WithUDPMux(mux),
			WithMulticastDNSMode(MulticastDNSModeDisabled),
		)
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, agent.Close())
		})

		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        nil,
				Local:           "10.0.0.3",
				AsCandidateType: CandidateTypeHost,
				Mode:            AddressRewriteAppend,
			},
		})
		require.NoError(t, err)
		agent.addressRewriteMapper = mapper

		var (
			mu        sync.Mutex
			addresses []Candidate
			done      = make(chan struct{})
		)
		require.NoError(t, agent.OnCandidate(func(c Candidate) {
			if c == nil {
				close(done)

				return
			}
			mu.Lock()
			addresses = append(addresses, c)
			mu.Unlock()
		}))

		require.NoError(t, agent.GatherCandidates())
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			require.FailNow(t, "gather did not complete")
		}

		mu.Lock()
		defer mu.Unlock()
		require.Len(t, addresses, 1)
		assert.Equal(t, "10.0.0.3", addresses[0].Address())
		assert.Equal(t, 1, mux.connCount())
	})
}

func TestGatherAddressRewriteSrflxModes(t *testing.T) {
	urls := []*stun.URI{{
		Scheme: SchemeTypeSTUN,
		Host:   "127.0.0.1",
		Port:   3478,
	}}

	t.Run("append srflx still gathers", func(t *testing.T) {
		mux := newCountingUniversalUDPMux(
			[]net.Addr{&net.UDPAddr{IP: net.IP{10, 0, 0, 2}, Port: 2345}},
			&stun.XORMappedAddress{
				IP:   net.IP{198, 51, 100, 10},
				Port: 5000,
			},
		)

		var (
			mu        sync.Mutex
			addresses []string
			done      = make(chan struct{})
		)

		agent, err := NewAgentWithOptions(
			WithNet(newStubNet(t)),
			WithCandidateTypes([]CandidateType{CandidateTypeServerReflexive}),
			WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
			WithUDPMuxSrflx(mux),
			WithMulticastDNSMode(MulticastDNSModeDisabled),
			WithUrls(urls),
		)
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, agent.Close())
		})

		require.NoError(t, agent.OnCandidate(func(c Candidate) {
			if c == nil {
				close(done)
			}
			mu.Lock()
			if c != nil {
				addresses = append(addresses, c.Address())
			}
			mu.Unlock()
		}))

		require.NoError(t, agent.GatherCandidates())
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			require.FailNow(t, "gather did not complete")
		}

		assert.Greater(t, mux.getConnForURLCount, 0)
		mu.Lock()
		require.Len(t, addresses, 1)
		assert.Equal(t, "198.51.100.10", addresses[0])
		mu.Unlock()
	})

	t.Run("replace srflx skips gather", func(t *testing.T) {
		router, nw := buildSimpleVNet(t)
		defer func() {
			require.NoError(t, router.Stop())
		}()

		var (
			mu        sync.Mutex
			addresses []string
			done      = make(chan struct{})
		)

		agent, err := NewAgentWithOptions(
			WithNet(nw),
			WithCandidateTypes([]CandidateType{CandidateTypeServerReflexive}),
			WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
			WithMulticastDNSMode(MulticastDNSModeDisabled),
			WithUrls(urls),
			WithAddressRewriteRules(AddressRewriteRule{
				External:        []string{"203.0.113.50"},
				Local:           "0.0.0.0",
				AsCandidateType: CandidateTypeServerReflexive,
				Mode:            AddressRewriteReplace,
			}),
		)
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, agent.Close())
		})

		require.NoError(t, agent.OnCandidate(func(c Candidate) {
			if c == nil {
				close(done)
			}
			mu.Lock()
			if c != nil {
				addresses = append(addresses, c.Address())
			}
			mu.Unlock()
		}))

		require.NoError(t, agent.GatherCandidates())
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			require.FailNow(t, "gather did not complete")
		}

		mu.Lock()
		require.Len(t, addresses, 1)
		assert.Equal(t, "203.0.113.50", addresses[0])
		mu.Unlock()
	})
}

func TestGatherAddressRewriteRelayModes(t *testing.T) {
	t.Run("replace relay", func(t *testing.T) {
		agent, err := NewAgentWithOptions(
			WithNet(newStubNet(t)),
			WithCandidateTypes([]CandidateType{CandidateTypeRelay}),
			WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
			WithMulticastDNSMode(MulticastDNSModeDisabled),
			WithAddressRewriteRules(AddressRewriteRule{
				External:        []string{"203.0.113.60"},
				Local:           "10.0.0.10",
				AsCandidateType: CandidateTypeRelay,
				Mode:            AddressRewriteReplace,
			}),
		)
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, agent.Close())
		})

		agent.addRelayCandidates(context.Background(), agent.gatherGeneration, relayEndpoint{
			network:  NetworkTypeUDP4.String(),
			address:  net.ParseIP("192.0.2.10"),
			port:     5000,
			relAddr:  "10.0.0.10",
			relPort:  4000,
			protocol: udp,
			conn:     newStubPacketConn(&net.UDPAddr{IP: net.IP{10, 0, 0, 10}, Port: 4000}),
		})

		local, err := agent.GetLocalCandidates()
		require.NoError(t, err)
		require.Len(t, local, 1)
		assert.Equal(t, "203.0.113.60", local[0].Address())
	})

	t.Run("append relay", func(t *testing.T) {
		agent, err := NewAgentWithOptions(
			WithNet(newStubNet(t)),
			WithCandidateTypes([]CandidateType{CandidateTypeRelay}),
			WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
			WithMulticastDNSMode(MulticastDNSModeDisabled),
			WithAddressRewriteRules(AddressRewriteRule{
				External:        []string{"203.0.113.70"},
				Local:           "10.0.0.20",
				AsCandidateType: CandidateTypeRelay,
				Mode:            AddressRewriteAppend,
			}),
		)
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, agent.Close())
		})

		agent.addRelayCandidates(context.Background(), agent.gatherGeneration, relayEndpoint{
			network:  NetworkTypeUDP4.String(),
			address:  net.ParseIP("192.0.2.20"),
			port:     6000,
			relAddr:  "10.0.0.20",
			relPort:  5000,
			protocol: udp,
			conn:     newStubPacketConn(&net.UDPAddr{IP: net.IP{10, 0, 0, 20}, Port: 5000}),
		})

		local, err := agent.GetLocalCandidates()
		require.NoError(t, err)
		require.Len(t, local, 2)
		addresses := []string{local[0].Address(), local[1].Address()}
		assert.ElementsMatch(t, []string{"192.0.2.20", "203.0.113.70"}, addresses)
	})
}

// Assert that UniversalUDPMux is used while gathering when configured in the Agent.
func TestUniversalUDPMuxUsage(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IP{127, 0, 0, 1}, Port: randomPort(t)})
	require.NoError(t, err)
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
			Host:   localhostIPStr,
			Port:   3478 + i,
		})
	}

	agent, err := NewAgent(&AgentConfig{
		NetworkTypes:   supportedNetworkTypes(),
		Urls:           urls,
		CandidateTypes: []CandidateType{CandidateTypeServerReflexive},
		UDPMuxSrflx:    udpMuxSrflx,
	})
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
	require.Equal(
		t,
		numSTUNS,
		udpMuxSrflx.getXORMappedAddrUsedTimes,
		"expected times that GetXORMappedAddr should be called",
	)
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

type countingUniversalUDPMux struct {
	*mockUniversalUDPMux
	getConnForURLCount     int
	removeConnByUfragCount int
}

func newCountingUniversalUDPMux(addrs []net.Addr, xorAddr *stun.XORMappedAddress) *countingUniversalUDPMux {
	return &countingUniversalUDPMux{
		mockUniversalUDPMux: newMockUniversalUDPMux(addrs, xorAddr),
	}
}

func (m *countingUniversalUDPMux) GetConnForURL(ufrag string, url string, addr net.Addr) (net.PacketConn, error) {
	m.getConnForURLCount++

	return m.mockUniversalUDPMux.GetConnForURL(ufrag, url, addr)
}

func (m *countingUniversalUDPMux) RemoveConnByUfrag(s string) {
	m.removeConnByUfragCount++
	m.mockUniversalUDPMux.RemoveConnByUfrag(s)
}

func TestGatherCandidatesSrflxMappedEmitsCandidates(t *testing.T) {
	defer test.CheckRoutines(t)()

	router, nw := buildSimpleVNet(t)
	defer func() {
		require.NoError(t, router.Stop())
	}()

	agent, err := NewAgentWithOptions(
		WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
		WithCandidateTypes([]CandidateType{CandidateTypeServerReflexive}),
		WithAddressRewriteRules(AddressRewriteRule{
			External: []string{
				"203.0.113.10",
				"203.0.113.20",
			},
			AsCandidateType: CandidateTypeServerReflexive,
		}),
		WithNet(nw),
	)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	var (
		mu       sync.Mutex
		seen     []Candidate
		gathered = make(chan struct{})
	)

	require.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c == nil {
			close(gathered)

			return
		}

		mu.Lock()
		seen = append(seen, c)
		mu.Unlock()
	}))

	require.NoError(t, agent.GatherCandidates())

	select {
	case <-gathered:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "gatherCandidatesSrflxMapped did not finish before timeout")
	}

	mu.Lock()
	addresses := make([]string, 0, len(seen))
	for _, cand := range seen {
		addresses = append(addresses, cand.Address())
	}
	mu.Unlock()

	require.Len(t, addresses, 2)
	require.ElementsMatch(t, []string{"203.0.113.10", "203.0.113.20"}, addresses)

	localCandidates, err := agent.GetLocalCandidates()
	require.NoError(t, err)
	require.Len(t, localCandidates, 2)

	for _, cand := range localCandidates {
		require.Equal(t, CandidateTypeServerReflexive, cand.Type())
		relAddr := cand.RelatedAddress()
		require.NotNil(t, relAddr)
		require.NotEmpty(t, relAddr.Address)
		require.Equal(t, relAddr.Port, cand.Port())
	}
}

func TestGatherCandidatesSrflxMappedMissingExternalIPs(t *testing.T) {
	defer test.CheckRoutines(t)()

	router, nw := buildSimpleVNet(t)
	defer func() {
		require.NoError(t, router.Stop())
	}()

	agent, err := NewAgentWithOptions(
		WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
		WithCandidateTypes([]CandidateType{CandidateTypeServerReflexive}),
		WithNet(nw),
	)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	require.NoError(t, agent.OnCandidate(func(Candidate) {}))

	agent.addressRewriteMapper = &addressRewriteMapper{
		rulesByCandidateType: map[CandidateType][]*addressRewriteRuleMapping{
			CandidateTypeServerReflexive: {
				{
					ipv4Mapping: ipMapping{
						ipMap: map[string][]net.IP{
							"192.0.2.10": {net.ParseIP("203.0.113.10")},
						},
						valid: true,
					},
					allowIPv4: true,
				},
			},
		},
	}

	agent.gatherCandidatesSrflxMapped(context.Background(), []NetworkType{NetworkTypeUDP4}, agent.gatherGeneration)

	localCandidates, err := agent.GetLocalCandidates()
	require.NoError(t, err)
	require.Len(t, localCandidates, 1)
	require.Equal(t, CandidateTypeServerReflexive, localCandidates[0].Type())
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
		agent, err := NewAgentWithOptions(
			WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
			WithCandidateTypes([]CandidateType{CandidateTypeHost}),
			WithContinualGatheringPolicy(GatherContinually),
			WithNetworkMonitorInterval(customInterval),
		)
		require.NoError(t, err)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		// Verify the interval was set
		assert.Equal(t, customInterval, agent.networkMonitorInterval)
	})

	t.Run("Default network monitoring interval", func(t *testing.T) {
		agent, err := NewAgentWithOptions(
			WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
			WithCandidateTypes([]CandidateType{CandidateTypeHost}),
			WithContinualGatheringPolicy(GatherContinually),
		)
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
		agent, err := NewAgentWithOptions(
			WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
			WithCandidateTypes([]CandidateType{CandidateTypeHost}),
			WithContinualGatheringPolicy(GatherContinually),
			WithNetworkMonitorInterval(customInterval),
		)
		require.NoError(t, err)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		// Initialize the last known interfaces
		_, addrs, err := localInterfaces(
			agent.net,
			agent.interfaceFilter,
			agent.ipFilter,
			agent.networkTypes,
			agent.includeLoopback,
		)
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
	return &mockUniversalUDPMux{
		mockUDPMux: newMockUDPMux(addrs),
		xorAddr:    xorAddr,
	}
}

func (m *mockUniversalUDPMux) GetXORMappedAddr(net.Addr, time.Duration) (*stun.XORMappedAddress, error) {
	return m.xorAddr, nil
}

func (m *mockUniversalUDPMux) GetRelayedAddr(net.Addr, time.Duration) (*net.Addr, error) {
	return nil, errNotImplemented
}

func (m *mockUniversalUDPMux) GetConnForURL(ufrag string, url string, addr net.Addr) (net.PacketConn, error) {
	return m.mockUDPMux.GetConn(ufrag+url, addr)
}
