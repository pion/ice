// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"crypto/rand"
	"crypto/sha1" //nolint:gosec
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pion/stun"
	"github.com/pion/transport/v2/test"
	"github.com/stretchr/testify/require"
)

func TestUDPMux(t *testing.T) {
	require := require.New(t)

	report := test.CheckRoutines(t)
	defer report()

	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	conn4, err := net.ListenUDP(udp, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(err)

	conn6, err := net.ListenUDP(udp, &net.UDPAddr{IP: net.IPv6loopback})
	if err != nil {
		t.Log("IPv6 is not supported on this machine")
	}

	connUnspecified, err := net.ListenUDP(udp, nil)
	require.NoError(err)

	conn4Unspecified, err := net.ListenUDP(udp, &net.UDPAddr{IP: net.IPv4zero})
	require.NoError(err)

	conn6Unspecified, err := net.ListenUDP(udp, &net.UDPAddr{IP: net.IPv6unspecified})
	if err != nil {
		t.Log("IPv6 is not supported on this machine")
	}

	type testCase struct {
		name    string
		conn    net.PacketConn
		network string
	}

	for _, subTest := range []testCase{
		{name: "IPv4loopback", conn: conn4, network: udp4},
		{name: "IPv6loopback", conn: conn6, network: udp6},
		{name: "Unspecified", conn: connUnspecified, network: udp},
		{name: "IPv4Unspecified", conn: conn4Unspecified, network: udp4},
		{name: "IPv6Unspecified", conn: conn6Unspecified, network: udp6},
	} {
		network, conn := subTest.network, subTest.conn
		if udpConn, ok := conn.(*net.UDPConn); !ok || udpConn == nil {
			continue
		}
		t.Run(subTest.name, func(t *testing.T) {
			udpMux := NewUDPMuxDefault(UDPMuxParams{
				Logger:  nil,
				UDPConn: conn,
			})

			defer func() {
				_ = udpMux.Close()
				_ = conn.Close()
			}()

			require.NotNil(udpMux.LocalAddr())

			wg := sync.WaitGroup{}

			wg.Add(1)
			go func() {
				defer wg.Done()
				testMuxConnection(t, udpMux, "ufrag1", udp)
			}()

			const ptrSize = 32 << (^uintptr(0) >> 63)
			if network == udp {
				wg.Add(1)
				go func() {
					defer wg.Done()
					testMuxConnection(t, udpMux, "ufrag2", udp4)
				}()
				// Skip IPv6 test on i386
				if ptrSize != 32 {
					testMuxConnection(t, udpMux, "ufrag3", udp6)
				}
			} else if ptrSize != 32 || network != udp6 {
				testMuxConnection(t, udpMux, "ufrag2", network)
			}

			wg.Wait()

			require.NoError(udpMux.Close())

			// Can't create more connections
			_, err = udpMux.GetConn("failufrag", udpMux.LocalAddr())
			require.Error(err)
		})
	}
}

func TestAddressEncoding(t *testing.T) {
	require := require.New(t)

	cases := []struct {
		name string
		addr net.UDPAddr
	}{
		{
			name: "empty address",
		},
		{
			name: "ipv4",
			addr: net.UDPAddr{
				IP:   net.IPv4(244, 120, 0, 5),
				Port: 6000,
				Zone: "",
			},
		},
		{
			name: "ipv6",
			addr: net.UDPAddr{
				IP:   net.IPv6loopback,
				Port: 2500,
				Zone: "zone",
			},
		},
	}

	for _, c := range cases {
		addr := c.addr
		t.Run(c.name, func(t *testing.T) {
			buf := make([]byte, maxAddrSize)
			n, err := encodeUDPAddr(&addr, buf)
			require.NoError(err)

			parsedAddr, err := decodeUDPAddr(buf[:n])
			require.NoError(err)
			require.EqualValues(&addr, parsedAddr)
		})
	}
}

func testMuxConnection(t *testing.T, udpMux *UDPMuxDefault, ufrag string, network string) {
	require := require.New(t)

	pktConn, err := udpMux.GetConn(ufrag, udpMux.LocalAddr())
	require.NoError(err, "Failed to retrieve muxed connection for ufrag")
	defer func() {
		_ = pktConn.Close()
	}()

	addr, ok := pktConn.LocalAddr().(*net.UDPAddr)
	require.True(ok, "pktConn.LocalAddr() is not a net.UDPAddr")
	if addr.IP.IsUnspecified() {
		addr = &net.UDPAddr{Port: addr.Port}
	}
	remoteConn, err := net.DialUDP(network, nil, addr)
	require.NoError(err, "Failed to dial test UDP connection")

	testMuxConnectionPair(t, pktConn, remoteConn, ufrag)
}

func testMuxConnectionPair(t *testing.T, pktConn net.PacketConn, remoteConn *net.UDPConn, ufrag string) {
	require := require.New(t)

	// Initial messages are dropped
	_, err := remoteConn.Write([]byte("dropped bytes"))
	require.NoError(err)
	// Wait for packet to be consumed
	time.Sleep(time.Millisecond)

	// Write out to establish connection
	msg := stun.New()
	msg.Type = stun.MessageType{Method: stun.MethodBinding, Class: stun.ClassRequest}
	msg.Add(stun.AttrUsername, []byte(ufrag+":otherufrag"))
	msg.Encode()
	_, err = pktConn.WriteTo(msg.Raw, remoteConn.LocalAddr())
	require.NoError(err)

	// Ensure received
	buf := make([]byte, receiveMTU)
	n, err := remoteConn.Read(buf)
	require.NoError(err)
	require.Equal(msg.Raw, buf[:n])

	// Start writing packets through mux
	targetSize := 1 * 1024 * 1024
	readDone := make(chan struct{}, 1)
	remoteReadDone := make(chan struct{}, 1)

	// Read packets from the muxed side
	go func() {
		defer func() {
			t.Logf("closing read chan for: %s", ufrag)
			close(readDone)
		}()
		readBuf := make([]byte, receiveMTU)
		nextSeq := uint32(0)
		for read := 0; read < targetSize; {
			n, _, err := pktConn.ReadFrom(readBuf)
			require.NoError(err)
			require.Equal(receiveMTU, n)

			verifyPacket(t, readBuf[:n], nextSeq)

			// Write it back to sender
			_, err = pktConn.WriteTo(readBuf[:n], remoteConn.LocalAddr())
			require.NoError(err)

			read += n
			nextSeq++
		}
	}()

	go func() {
		defer func() {
			close(remoteReadDone)
		}()
		readBuf := make([]byte, receiveMTU)
		nextSeq := uint32(0)
		for read := 0; read < targetSize; {
			n, _, err := remoteConn.ReadFrom(readBuf)
			require.NoError(err)
			require.Equal(receiveMTU, n)

			verifyPacket(t, readBuf[:n], nextSeq)

			read += n
			nextSeq++
		}
	}()

	sequence := 0
	for written := 0; written < targetSize; {
		buf := make([]byte, receiveMTU)
		// Byte 0-4: sequence
		// Bytes 4-24: sha1 checksum
		// Bytes2 4-mtu: random data
		_, err := rand.Read(buf[24:])
		require.NoError(err)
		h := sha1.Sum(buf[24:]) //nolint:gosec
		copy(buf[4:24], h[:])
		binary.LittleEndian.PutUint32(buf[0:4], uint32(sequence))

		_, err = remoteConn.Write(buf)
		require.NoError(err)

		written += len(buf)
		sequence++

		time.Sleep(time.Millisecond)
	}

	<-readDone
	<-remoteReadDone
}

func verifyPacket(t *testing.T, b []byte, nextSeq uint32) {
	readSeq := binary.LittleEndian.Uint32(b[0:4])
	require.Equal(t, nextSeq, readSeq)
	h := sha1.Sum(b[24:]) //nolint:gosec
	require.Equal(t, h[:], b[4:24])
}

func TestUDPMux_Agent_Restart(t *testing.T) {
	require := require.New(t)

	oneSecond := time.Second
	connA, connB := pipe(&AgentConfig{
		DisconnectedTimeout: &oneSecond,
		FailedTimeout:       &oneSecond,
	})

	aNotifier, aConnected := onConnected()
	require.NoError(connA.agent.OnConnectionStateChange(aNotifier))

	bNotifier, bConnected := onConnected()
	require.NoError(connB.agent.OnConnectionStateChange(bNotifier))

	// Maintain Credentials across restarts
	ufragA, pwdA, err := connA.agent.GetLocalUserCredentials()
	require.NoError(err)

	ufragB, pwdB, err := connB.agent.GetLocalUserCredentials()
	require.NoError(err)

	require.NoError(err)

	// Restart and Re-Signal
	require.NoError(connA.agent.Restart(ufragA, pwdA))
	require.NoError(connB.agent.Restart(ufragB, pwdB))

	require.NoError(connA.agent.SetRemoteCredentials(ufragB, pwdB))
	require.NoError(connB.agent.SetRemoteCredentials(ufragA, pwdA))
	gatherAndExchangeCandidates(connA.agent, connB.agent)

	// Wait until both have gone back to connected
	<-aConnected
	<-bConnected

	require.NoError(connA.agent.Close())
	require.NoError(connB.agent.Close())
}
