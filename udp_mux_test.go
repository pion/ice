// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pion/stun/v3"
	"github.com/pion/transport/v3/test"
	"github.com/stretchr/testify/require"
)

func TestUDPMux(t *testing.T) { //nolint:cyclop
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	conn4, err := net.ListenUDP(udp, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)

	conn6, err := net.ListenUDP(udp, &net.UDPAddr{IP: net.IPv6loopback})
	if err != nil {
		t.Log("IPv6 is not supported on this machine")
	}

	connUnspecified, err := net.ListenUDP(udp, nil)
	require.NoError(t, err)

	conn4Unspecified, err := net.ListenUDP(udp, &net.UDPAddr{IP: net.IPv4zero})
	require.NoError(t, err)

	conn6Unspecified, err := net.ListenUDP(udp, &net.UDPAddr{IP: net.IPv6unspecified})
	if err != nil {
		t.Log("IPv6 is not supported on this machine")
	}

	type testCase struct {
		name    string
		conn    net.PacketConn
		network string
	}

	testCases := []testCase{
		{name: "IPv4loopback", conn: conn4, network: udp4},
		{name: "IPv6loopback", conn: conn6, network: udp6},
		{name: "Unspecified", conn: connUnspecified, network: udp},
		{name: "IPv4Unspecified", conn: conn4Unspecified, network: udp4},
		{name: "IPv6Unspecified", conn: conn6Unspecified, network: udp6},
	}

	if ipv6Available(t) {
		addr6 := getLocalIPAddress(t, NetworkTypeUDP6)

		conn6Unspecified, listenEerr := net.ListenUDP(udp, &net.UDPAddr{
			IP:   addr6.AsSlice(),
			Zone: addr6.Zone(),
		})
		if listenEerr != nil {
			t.Log("IPv6 is not supported on this machine")
		}

		testCases = append(testCases,
			testCase{name: "IPv6Specified", conn: conn6Unspecified, network: udp6},
		)
	}

	for _, subTest := range testCases {
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

			require.NotNil(t, udpMux.LocalAddr(), "udpMux.LocalAddr() is nil")

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

				testMuxConnection(t, udpMux, "ufrag3", udp6)
			} else if ptrSize != 32 || network != udp6 {
				testMuxConnection(t, udpMux, "ufrag2", network)
			}

			wg.Wait()

			require.NoError(t, udpMux.Close())

			// Can't create more connections
			_, err = udpMux.GetConn("failufrag", udpMux.LocalAddr())
			require.Error(t, err)
		})
	}
}

func testMuxConnection(t *testing.T, udpMux *UDPMuxDefault, ufrag string, network string) {
	t.Helper()

	pktConn, err := udpMux.GetConn(ufrag, udpMux.LocalAddr())
	require.NoError(t, err, "error retrieving muxed connection for ufrag")
	defer func() {
		_ = pktConn.Close()
	}()

	addr, ok := pktConn.LocalAddr().(*net.UDPAddr)
	require.True(t, ok, "pktConn.LocalAddr() is not a net.UDPAddr")
	if addr.IP.IsUnspecified() {
		addr = &net.UDPAddr{Port: addr.Port}
	}
	remoteConn, err := net.DialUDP(network, nil, addr)
	require.NoError(t, err, "error dialing test UDP connection")

	testMuxConnectionPair(t, pktConn, remoteConn, ufrag)
}

func testMuxConnectionPair(t *testing.T, pktConn net.PacketConn, remoteConn *net.UDPConn, ufrag string) {
	t.Helper()

	// Initial messages are dropped
	_, err := remoteConn.Write([]byte("dropped bytes"))
	require.NoError(t, err)
	// Wait for packet to be consumed
	time.Sleep(time.Millisecond)

	// Write out to establish connection
	msg := stun.New()
	msg.Type = stun.MessageType{Method: stun.MethodBinding, Class: stun.ClassRequest}
	msg.Add(stun.AttrUsername, []byte(ufrag+":otherufrag"))
	msg.Encode()
	_, err = pktConn.WriteTo(msg.Raw, remoteConn.LocalAddr())
	require.NoError(t, err)

	// Ensure received
	buf := make([]byte, receiveMTU)
	n, err := remoteConn.Read(buf)
	require.NoError(t, err)
	require.Equal(t, msg.Raw, buf[:n])

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
			require.NoError(t, err)
			require.Equal(t, receiveMTU, n)

			verifyPacket(t, readBuf[:n], nextSeq)

			// Write it back to sender
			_, err = pktConn.WriteTo(readBuf[:n], remoteConn.LocalAddr())
			require.NoError(t, err)

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
			require.NoError(t, err)
			require.Equal(t, receiveMTU, n)

			verifyPacket(t, readBuf[:n], nextSeq)

			read += n
			nextSeq++
		}
	}()

	sequence := 0
	for written := 0; written < targetSize; {
		buf := make([]byte, receiveMTU)
		// Byte 0-4: sequence
		// Bytes 4-36: sha256 checksum
		// Bytes2 36-mtu: random data
		_, err := rand.Read(buf[36:])
		require.NoError(t, err)
		h := sha256.Sum256(buf[36:])
		copy(buf[4:36], h[:])
		binary.LittleEndian.PutUint32(buf[0:4], uint32(sequence)) //nolint:gosec // G115

		_, err = remoteConn.Write(buf)
		require.NoError(t, err)

		written += len(buf)
		sequence++

		time.Sleep(time.Millisecond)
	}

	<-readDone
	<-remoteReadDone
}

func verifyPacket(t *testing.T, b []byte, nextSeq uint32) {
	t.Helper()

	readSeq := binary.LittleEndian.Uint32(b[0:4])
	require.Equal(t, nextSeq, readSeq)
	h := sha256.Sum256(b[36:])
	require.Equal(t, h[:], b[4:36])
}

func TestUDPMux_Agent_Restart(t *testing.T) {
	oneSecond := time.Second
	connA, connB := pipe(t, &AgentConfig{
		DisconnectedTimeout: &oneSecond,
		FailedTimeout:       &oneSecond,
	})
	defer closePipe(t, connA, connB)

	aNotifier, aConnected := onConnected()
	require.NoError(t, connA.agent.OnConnectionStateChange(aNotifier))

	bNotifier, bConnected := onConnected()
	require.NoError(t, connB.agent.OnConnectionStateChange(bNotifier))

	// Maintain Credentials across restarts
	ufragA, pwdA, err := connA.agent.GetLocalUserCredentials()
	require.NoError(t, err)

	ufragB, pwdB, err := connB.agent.GetLocalUserCredentials()
	require.NoError(t, err)

	require.NoError(t, err)

	// Restart and Re-Signal
	require.NoError(t, connA.agent.Restart(ufragA, pwdA))
	require.NoError(t, connB.agent.Restart(ufragB, pwdB))

	require.NoError(t, connA.agent.SetRemoteCredentials(ufragB, pwdB))
	require.NoError(t, connB.agent.SetRemoteCredentials(ufragA, pwdA))
	gatherAndExchangeCandidates(t, connA.agent, connB.agent)

	// Wait until both have gone back to connected
	<-aConnected
	<-bConnected
}

func secondTestMuxedConn(t *testing.T, capBytes int) *udpMuxedConn {
	t.Helper()

	pool := &sync.Pool{
		New: func() any {
			return &bufferHolder{buf: make([]byte, capBytes)}
		},
	}
	params := &udpMuxedConnParams{
		AddrPool:  pool,
		LocalAddr: &net.UDPAddr{IP: net.IPv4zero, Port: 0},
	}

	return newUDPMuxedConn(params)
}

func TestUDPMuxedConn_ReadFrom(t *testing.T) {
	conn := secondTestMuxedConn(t, 1500)
	remote := &net.UDPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 5678}
	payload := []byte("this is a payload of length 29!")

	require.NoError(t, conn.writePacket(payload, remote))

	// read with too small of a buffer -> expect io.ErrShortBuffer, n=0, rAddr=nil
	small := make([]byte, 8)
	n, raddr, err := conn.ReadFrom(small)
	require.ErrorIs(t, err, io.ErrShortBuffer)
	require.Equal(t, 0, n)
	require.Nil(t, raddr)

	// try again with sufficient buffer
	require.NoError(t, conn.writePacket(payload, remote))
	dst := make([]byte, len(payload))
	n, raddr, err = conn.ReadFrom(dst)
	require.NoError(t, err)
	require.Equal(t, len(payload), n)
	require.Equal(t, payload, dst[:n])

	// rAddr should be what was set on the packet.
	require.NotNil(t, raddr)
	require.Equal(t, remote.String(), raddr.String())
}

func TestUDPMuxedConn_ReadFrom_EOFAfterClose(t *testing.T) {
	conn := secondTestMuxedConn(t, 64)

	// close with empty queue -> immediate EOF branch inside ReadFrom.
	require.NoError(t, conn.Close())

	buf := make([]byte, 16)
	n, raddr, err := conn.ReadFrom(buf)
	require.Equal(t, 0, n)
	require.Nil(t, raddr)
	require.ErrorIs(t, err, io.EOF)
}

func TestUDPMuxedConn_ReadFrom_WaitingThenClosedEOF(t *testing.T) {
	conn := secondTestMuxedConn(t, 64)

	errCh := make(chan error, 1)
	go func() {
		// empty queue sets state to waiting and block on notify/closedChan.
		_, _, err := conn.ReadFrom(make([]byte, 16))
		errCh <- err
	}()

	// let goroutine enter Waiting state.
	time.Sleep(10 * time.Millisecond)

	require.NoError(t, conn.Close())

	select {
	case err := <-errCh:
		require.ErrorIs(t, err, io.EOF)
	case <-time.After(1 * time.Second):
		require.Fail(t, "timeout waiting for ReadFrom to return after Close")
	}
}

func TestUDPMuxedConn_WriteTo_ClosedPipe(t *testing.T) {
	conn := secondTestMuxedConn(t, 64)
	require.NoError(t, conn.Close())

	n, err := conn.WriteTo([]byte("x"), &net.UDPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 1234})
	require.Equal(t, 0, n)
	require.ErrorIs(t, err, io.ErrClosedPipe)
}

// non-*net.UDPAddr that still satisfies net.Addr.
type notUDPAddr struct{}

func (notUDPAddr) Network() string { return "udp" }
func (notUDPAddr) String() string  { return "not-a-udp-addr" }

func TestUDPMuxedConn_WriteTo_BadAddrType(t *testing.T) {
	conn := secondTestMuxedConn(t, 64)

	n, err := conn.WriteTo([]byte("x"), notUDPAddr{})
	require.Equal(t, 0, n)
	require.ErrorIs(t, err, errFailedToCastUDPAddr)
}

// uses invalid IP length so newIPPort returns error.
func TestUDPMuxedConn_WriteTo_newIPPortError(t *testing.T) {
	conn := secondTestMuxedConn(t, 64)

	invalidIP := net.IP{1} // len=1 -> invalid
	raddr := &net.UDPAddr{IP: invalidIP, Port: 1234}

	n, err := conn.WriteTo([]byte("x"), raddr)
	require.Equal(t, 0, n)
	require.Error(t, err)
}

func TestUDPMuxedConn_SetDeadlines(t *testing.T) {
	conn := secondTestMuxedConn(t, 64)

	// While open
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(250*time.Millisecond)))
	require.NoError(t, conn.SetWriteDeadline(time.Now().Add(250*time.Millisecond)))

	// After close
	require.NoError(t, conn.Close())
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(250*time.Millisecond)))
	require.NoError(t, conn.SetWriteDeadline(time.Now().Add(250*time.Millisecond)))
}

func TestUDPMuxedConn_removeAddress(t *testing.T) {
	conn := secondTestMuxedConn(t, 64)

	mk := func(ip string, port uint16) ipPort {
		p, err := newIPPort(net.ParseIP(ip), "", port)
		require.NoError(t, err)

		return p
	}

	a1 := mk("1.1.1.1", 1000)
	a2 := mk("2.2.2.2", 2000)
	a3 := mk("3.3.3.3", 3000)
	a4 := mk("9.9.9.9", 9000) // non-existent in lists below

	t.Run("remove-existing-middle", func(t *testing.T) {
		// true for a1/a3, false for a2
		conn.addresses = []ipPort{a1, a2, a3}
		conn.removeAddress(a2)
		got := conn.getAddresses()
		require.Equal(t, []ipPort{a1, a3}, got)
	})

	t.Run("remove-non-existing", func(t *testing.T) {
		// only true (no matches)
		conn.addresses = []ipPort{a1, a3}
		conn.removeAddress(a4)
		got := conn.getAddresses()
		require.Equal(t, []ipPort{a1, a3}, got)
	})

	t.Run("remove-duplicates-all", func(t *testing.T) {
		// all occurrences are removed (false twice)
		conn.addresses = []ipPort{a1, a1, a2}
		conn.removeAddress(a1)
		got := conn.getAddresses()
		require.Equal(t, []ipPort{a2}, got)
	})

	t.Run("remove-from-empty", func(t *testing.T) {
		// no iters loop, no panic + remain empty
		conn.addresses = nil
		conn.removeAddress(a1)
		got := conn.getAddresses()
		require.Empty(t, got)
	})
}

func TestUDPMuxedConn_writePacket_ShortBuffer(t *testing.T) {
	conn := secondTestMuxedConn(t, 8) // pool buf cap=8
	addr := &net.UDPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 9999}

	err := conn.writePacket(make([]byte, 16), addr) // len=16 > cap=8
	require.ErrorIs(t, err, io.ErrShortBuffer)

	require.Nil(t, conn.bufHead)
	require.Nil(t, conn.bufTail)
}

func TestUDPMuxedConn_writePacket_ClosedState(t *testing.T) {
	conn := secondTestMuxedConn(t, 64)
	addr := &net.UDPAddr{IP: net.IPv4(5, 6, 7, 8), Port: 1234}

	// closed state before write
	conn.mu.Lock()
	conn.state = udpMuxedConnClosed
	conn.mu.Unlock()

	err := conn.writePacket([]byte{1, 2, 3}, addr) // fits in cap
	require.ErrorIs(t, err, io.ErrClosedPipe)

	// queue unchanged
	require.Nil(t, conn.bufHead)
	require.Nil(t, conn.bufTail)
}

func TestUDPMuxedConn_writePacket_NotifyDefaultBranch(t *testing.T) {
	conn := secondTestMuxedConn(t, 64)
	addr := &net.UDPAddr{IP: net.IPv4(9, 9, 9, 9), Port: 4242}

	// fill notify channel so send would block
	conn.notify <- struct{}{}

	// set pre-state to waiting so the post-unlock select triggers
	conn.mu.Lock()
	conn.state = udpMuxedConnWaiting
	conn.mu.Unlock()

	// write should take default branch
	err := conn.writePacket([]byte("hello"), addr)
	require.NoError(t, err)

	// packet enqueued
	require.NotNil(t, conn.bufHead)
	require.NotNil(t, conn.bufTail)

	// channel still full => no send happened (default path executed)
	require.Equal(t, 1, len(conn.notify))
}
