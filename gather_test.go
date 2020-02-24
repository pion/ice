// +build !js

package ice

import (
	"context"
	"crypto/tls"
	"net"
	"reflect"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/pion/dtls/v2"
	"github.com/pion/dtls/v2/pkg/crypto/selfsign"
	"github.com/pion/transport/test"
	"github.com/pion/turn/v2"
	"github.com/stretchr/testify/assert"
)

func TestListenUDP(t *testing.T) {
	a, err := NewAgent(&AgentConfig{})
	assert.NoError(t, err)

	localIPs, err := localInterfaces(a.net, a.interfaceFilter, []NetworkType{NetworkTypeUDP4})
	assert.NotEqual(t, len(localIPs), 0, "localInterfaces found no interfaces, unable to test")
	assert.NoError(t, err)

	ip := localIPs[0]

	conn, err := listenUDPInPortRange(a.net, a.log, 0, 0, udp, &net.UDPAddr{IP: ip, Port: 0})
	assert.NoError(t, err, "listenUDP error with no port restriction")
	assert.NotNil(t, conn, "listenUDP error with no port restriction return a nil conn")

	_, err = listenUDPInPortRange(a.net, a.log, 4999, 5000, udp, &net.UDPAddr{IP: ip, Port: 0})
	assert.Equal(t, err, ErrPort, "listenUDP with invalid port range did not return ErrPort")

	conn, err = listenUDPInPortRange(a.net, a.log, 5000, 5000, udp, &net.UDPAddr{IP: ip, Port: 0})
	assert.NoError(t, err, "listenUDP error with no port restriction")
	assert.NotNil(t, conn, "listenUDP error with no port restriction return a nil conn")

	_, port, err := net.SplitHostPort(conn.LocalAddr().String())
	assert.NoError(t, err)
	assert.Equal(t, port, "5000", "listenUDP with port restriction of 5000 listened on incorrect port")

	portMin := 5100
	portMax := 5109
	total := portMax - portMin + 1
	result := make([]int, 0, total)
	portRange := make([]int, 0, total)
	for i := 0; i < total; i++ {
		conn, err = listenUDPInPortRange(a.net, a.log, portMax, portMin, udp, &net.UDPAddr{IP: ip, Port: 0})
		assert.NoError(t, err, "listenUDP error with no port restriction")
		assert.NotNil(t, conn, "listenUDP error with no port restriction return a nil conn")

		_, port, err = net.SplitHostPort(conn.LocalAddr().String())
		if err != nil {
			t.Fatal(err)
		}
		p, _ := strconv.Atoi(port)
		if p < portMin || p > portMax {
			t.Fatalf("listenUDP with port restriction [%d, %d] listened on incorrect port (%s)", portMin, portMax, port)
		}
		result = append(result, p)
		portRange = append(portRange, portMin+i)
	}
	if sort.IntsAreSorted(result) {
		t.Fatalf("listenUDP with port restriction [%d, %d], ports result should be random", portMin, portMax)
	}
	sort.Ints(result)
	if !reflect.DeepEqual(result, portRange) {
		t.Fatalf("listenUDP with port restriction [%d, %d], got:%v, want:%v", portMin, portMax, result, portRange)
	}
	_, err = listenUDPInPortRange(a.net, a.log, portMax, portMin, udp, &net.UDPAddr{IP: ip, Port: 0})
	assert.Equal(t, err, ErrPort, "listenUDP with port restriction [%d, %d], did not return ErrPort", portMin, portMax)

	assert.NoError(t, a.Close())
}

// Assert that STUN gathering is done concurrently
func TestSTUNConcurrency(t *testing.T) {
	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	report := test.CheckRoutines(t)
	defer report()

	serverPort := randomPort(t)
	serverListener, err := net.ListenPacket("udp4", "127.0.0.1:"+strconv.Itoa(serverPort))
	assert.NoError(t, err)

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
	assert.NoError(t, err)

	urls := []*URL{}
	for i := 0; i <= 10; i++ {
		urls = append(urls, &URL{
			Scheme: SchemeTypeSTUN,
			Host:   "127.0.0.1",
			Port:   serverPort + 1,
		})
	}
	urls = append(urls, &URL{
		Scheme: SchemeTypeSTUN,
		Host:   "127.0.0.1",
		Port:   serverPort,
	})

	a, err := NewAgent(&AgentConfig{
		NetworkTypes:   supportedNetworkTypes,
		Trickle:        true,
		Urls:           urls,
		CandidateTypes: []CandidateType{CandidateTypeServerReflexive},
	})
	assert.NoError(t, err)

	candidateGathered, candidateGatheredFunc := context.WithCancel(context.Background())
	assert.NoError(t, a.OnCandidate(func(c Candidate) {
		if c != nil {
			candidateGatheredFunc()
		}
	}))
	assert.NoError(t, a.GatherCandidates())

	<-candidateGathered.Done()

	assert.NoError(t, a.Close())
	assert.NoError(t, server.Close())
}

// Assert that TURN gathering is done concurrently
func TestTURNConcurrency(t *testing.T) {
	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	report := test.CheckRoutines(t)
	defer report()

	runTest := func(protocol ProtoType, scheme SchemeType, packetConn net.PacketConn, listener net.Listener, serverPort int) {
		packetConnConfigs := []turn.PacketConnConfig{}
		if packetConn != nil {
			packetConnConfigs = append(packetConnConfigs, turn.PacketConnConfig{
				PacketConn:            packetConn,
				RelayAddressGenerator: &turn.RelayAddressGeneratorNone{Address: "127.0.0.1"},
			})
		}

		listenerConfigs := []turn.ListenerConfig{}
		if listener != nil {
			listenerConfigs = append(listenerConfigs, turn.ListenerConfig{
				Listener:              listener,
				RelayAddressGenerator: &turn.RelayAddressGeneratorNone{Address: "127.0.0.1"},
			})
		}

		server, err := turn.NewServer(turn.ServerConfig{
			Realm:             "pion.ly",
			AuthHandler:       optimisticAuthHandler,
			PacketConnConfigs: packetConnConfigs,
			ListenerConfigs:   listenerConfigs,
		})
		assert.NoError(t, err)

		urls := []*URL{}
		for i := 0; i <= 10; i++ {
			urls = append(urls, &URL{
				Scheme:   scheme,
				Host:     "127.0.0.1",
				Username: "username",
				Password: "password",
				Proto:    protocol,
				Port:     serverPort + 1,
			})
		}
		urls = append(urls, &URL{
			Scheme:   scheme,
			Host:     "127.0.0.1",
			Username: "username",
			Password: "password",
			Proto:    protocol,
			Port:     serverPort,
		})

		a, err := NewAgent(&AgentConfig{
			CandidateTypes:     []CandidateType{CandidateTypeRelay},
			InsecureSkipVerify: true,
			NetworkTypes:       supportedNetworkTypes,
			Trickle:            true,
			Urls:               urls,
		})
		assert.NoError(t, err)

		candidateGathered, candidateGatheredFunc := context.WithCancel(context.Background())
		assert.NoError(t, a.OnCandidate(func(c Candidate) {
			if c != nil {
				candidateGatheredFunc()
			}
		}))
		assert.NoError(t, a.GatherCandidates())

		<-candidateGathered.Done()

		assert.NoError(t, a.Close())
		assert.NoError(t, server.Close())
	}

	t.Run("UDP Relay", func(t *testing.T) {
		serverPort := randomPort(t)
		serverListener, err := net.ListenPacket("udp", "127.0.0.1:"+strconv.Itoa(serverPort))
		assert.NoError(t, err)

		runTest(ProtoTypeUDP, SchemeTypeTURN, serverListener, nil, serverPort)
	})

	t.Run("TCP Relay", func(t *testing.T) {
		serverPort := randomPort(t)
		serverListener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(serverPort))
		assert.NoError(t, err)

		runTest(ProtoTypeTCP, SchemeTypeTURN, nil, serverListener, serverPort)
	})

	t.Run("TLS Relay", func(t *testing.T) {
		certificate, genErr := selfsign.GenerateSelfSigned()
		assert.NoError(t, genErr)

		serverPort := randomPort(t)
		serverListener, err := tls.Listen("tcp", "127.0.0.1:"+strconv.Itoa(serverPort), &tls.Config{
			Certificates: []tls.Certificate{certificate},
		})
		assert.NoError(t, err)

		runTest(ProtoTypeTCP, SchemeTypeTURNS, nil, serverListener, serverPort)
	})

	t.Run("DTLS Relay", func(t *testing.T) {
		certificate, genErr := selfsign.GenerateSelfSigned()
		assert.NoError(t, genErr)

		serverPort := randomPort(t)
		serverListener, err := dtls.Listen("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: serverPort}, &dtls.Config{
			Certificates: []tls.Certificate{certificate},
		})
		assert.NoError(t, err)

		runTest(ProtoTypeUDP, SchemeTypeTURNS, nil, serverListener, serverPort)
	})
}
