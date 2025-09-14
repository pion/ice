// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pion/ice/v4/internal/fakenet"
	"github.com/pion/logging"
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

func TestNewUDPMuxDefault_LocalAddrNotUDPAddr(t *testing.T) {
	defer test.CheckRoutines(t)()

	c1, c2 := net.Pipe()
	defer func() { _ = c2.Close() }()

	pc := &fakenet.PacketConn{Conn: c1}

	mux := NewUDPMuxDefault(UDPMuxParams{
		Logger:  logging.NewDefaultLoggerFactory().NewLogger("ice"),
		UDPConn: pc,
	})
	require.NotNil(t, mux)

	defer func() { _ = mux.Close() }()

	addrs := mux.GetListenAddresses()
	require.Len(t, addrs, 1)
	require.Equal(t, pc.LocalAddr().String(), addrs[0].String())
}

func TestUDPMuxDefault_GetConn_InvalidAddress(t *testing.T) {
	defer test.CheckRoutines(t)()

	connA, err := net.ListenUDP(udp, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)

	udpMux := NewUDPMuxDefault(UDPMuxParams{
		Logger:  nil,
		UDPConn: connA,
	})
	defer func() {
		_ = udpMux.Close()
		_ = connA.Close()
	}()

	connB, err := net.ListenUDP(udp, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer func() { _ = connB.Close() }()

	pc, gerr := udpMux.GetConn("some-ufrag", connB.LocalAddr())
	require.Nil(t, pc)
	require.ErrorIs(t, gerr, errInvalidAddress)
}

func TestUDPMuxDefault_registerConnForAddress_ClosedMuxEarlyReturn(t *testing.T) {
	defer test.CheckRoutines(t)()

	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	udpMux := NewUDPMuxDefault(UDPMuxParams{UDPConn: udp})

	require.NoError(t, udpMux.Close())
	_ = udp.Close()

	conn := secondTestMuxedConn(t, 64)
	addr, err := newIPPort(net.ParseIP("1.2.3.4"), "", 9999)
	require.NoError(t, err)

	before := len(udpMux.addressMap)
	udpMux.registerConnForAddress(conn, addr)
	after := len(udpMux.addressMap)

	require.Equal(t, before, after)
	_, exists := udpMux.addressMap[addr]
	require.False(t, exists)
}

func TestUDPMuxDefault_registerConnForAddress_ReplacesExisting(t *testing.T) {
	defer test.CheckRoutines(t)()

	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	udpMux := NewUDPMuxDefault(UDPMuxParams{UDPConn: udp})
	defer func() {
		_ = udpMux.Close()
		_ = udp.Close()
	}()

	ipAddr, err := newIPPort(net.ParseIP("5.6.7.8"), "", 12345)
	require.NoError(t, err)

	existing := secondTestMuxedConn(t, 64)
	existing.addresses = []ipPort{ipAddr}
	udpMux.addressMapMu.Lock()
	udpMux.addressMap[ipAddr] = existing
	udpMux.addressMapMu.Unlock()

	// new conn should replace existing mapping and cause removeAddress on the old one.
	newConn := secondTestMuxedConn(t, 64)
	udpMux.registerConnForAddress(newConn, ipAddr)

	// map should now point to newConn.
	udpMux.addressMapMu.RLock()
	mapped := udpMux.addressMap[ipAddr]
	udpMux.addressMapMu.RUnlock()
	require.Equal(t, newConn, mapped)

	// old conn should have ipAddr removed from its addresses.
	require.False(t, existing.containsAddress(ipAddr), "old conn should have removed the address backref")
}

func stunWithLen(l uint16) []byte {
	m := stun.New()
	m.Type = stun.MessageType{Method: stun.MethodBinding, Class: stun.ClassRequest}
	m.Encode()
	out := append([]byte{}, m.Raw...)
	out[2] = byte(l >> 8)
	out[3] = byte(l & 0xff)

	return out
}

type scriptedUDPPC struct {
	local *net.UDPAddr
	seq   []struct {
		data []byte
		addr net.Addr
		err  error
	}
	i int
}

func (s *scriptedUDPPC) ReadFrom(p []byte) (int, net.Addr, error) {
	if s.i >= len(s.seq) {
		return 0, s.local, errIoEOF
	}
	step := s.seq[s.i]
	s.i++
	if step.err != nil {
		return 0, step.addr, step.err
	}
	n := copy(p, step.data)

	return n, step.addr, nil
}
func (s *scriptedUDPPC) WriteTo([]byte, net.Addr) (int, error) { return 0, nil }
func (s *scriptedUDPPC) Close() error                          { return nil }
func (s *scriptedUDPPC) LocalAddr() net.Addr                   { return s.local }
func (s *scriptedUDPPC) SetDeadline(time.Time) error           { return nil }
func (s *scriptedUDPPC) SetReadDeadline(time.Time) error       { return nil }
func (s *scriptedUDPPC) SetWriteDeadline(time.Time) error      { return nil }

var errIoEOF = errors.New("EOF")

func TestUDPMux_connWorker_AddrNotUDP(t *testing.T) {
	defer test.CheckRoutines(t)()

	c1, c2 := net.Pipe()
	defer func() {
		_ = c2.Close()
	}()

	pc := &fakenet.PacketConn{Conn: c1}
	mux := NewUDPMuxDefault(UDPMuxParams{UDPConn: pc})
	require.NotNil(t, mux)
	defer func() {
		_ = mux.Close()
	}()

	_, _ = c2.Write([]byte("frame"))
	_ = c2.Close()
}

func TestUDPMux_connWorker_ReadError_Timeout(t *testing.T) {
	defer test.CheckRoutines(t)()

	c1, c2 := net.Pipe()
	pc := &fakenet.PacketConn{Conn: c1}
	mux := NewUDPMuxDefault(UDPMuxParams{UDPConn: pc})
	require.NotNil(t, mux)

	_ = pc.SetReadDeadline(time.Unix(0, 0))

	_ = c2.Close()
	_ = mux.Close()
}

func TestUDPMux_connWorker_NewIPPortError(t *testing.T) {
	defer test.CheckRoutines(t)()

	badIP := net.IP{1}
	remote := &net.UDPAddr{IP: badIP, Port: 9999}
	pc := &scriptedUDPPC{
		local: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 7000},
		seq: []struct {
			data []byte
			addr net.Addr
			err  error
		}{
			{data: []byte{1}, addr: remote, err: nil}, // triggers newIPPort error
		},
	}
	mux := NewUDPMuxDefault(UDPMuxParams{UDPConn: pc})
	require.NotNil(t, mux)
	_ = mux.Close()
}

func TestUDPMux_connWorker_STUNDecodeError(t *testing.T) {
	defer test.CheckRoutines(t)()

	remote := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 5678}
	pc := &scriptedUDPPC{
		local: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 7001},
		seq: []struct {
			data []byte
			addr net.Addr
			err  error
		}{
			// bad STUN length -> Decode() error -> Warnf + continue
			{data: stunWithLen(4), addr: remote, err: nil},
			{data: nil, addr: remote, err: errIoEOF}, // exit loop
		},
	}
	mux := NewUDPMuxDefault(UDPMuxParams{UDPConn: pc})
	require.NotNil(t, mux)
	_ = mux.Close()
}

func TestUDPMux_connWorker_STUNNoUsername(t *testing.T) {
	defer test.CheckRoutines(t)()

	msg := stun.New()
	msg.Type = stun.MessageType{Method: stun.MethodBinding, Class: stun.ClassRequest}
	msg.Encode() // valid STUN + no USERNAME

	remote := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 3), Port: 5679}
	pc := &scriptedUDPPC{
		local: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 7002},
		seq: []struct {
			data []byte
			addr net.Addr
			err  error
		}{
			{data: append([]byte{}, msg.Raw...), addr: remote, err: nil}, // Get(USERNAME) fails
			{data: nil, addr: remote, err: errIoEOF},                     // exit loop
		},
	}
	mux := NewUDPMuxDefault(UDPMuxParams{UDPConn: pc})
	require.NotNil(t, mux)
	_ = mux.Close()
}

func TestUDPMux_connWorker_WritePacketError(t *testing.T) {
	defer test.CheckRoutines(t)()

	local := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 7003}
	remote := &net.UDPAddr{IP: net.IPv4(203, 0, 113, 7), Port: 5555}
	payload := []byte("0123456789ABCDEF")

	pc := &scriptedUDPPC{
		local: local,
		seq: []struct {
			data []byte
			addr net.Addr
			err  error
		}{
			{data: payload, addr: remote, err: nil},
			{data: nil, addr: remote, err: errIoEOF}, // exit loop
		},
	}
	mux := NewUDPMuxDefault(UDPMuxParams{UDPConn: pc})
	require.NotNil(t, mux)
	defer func() {
		_ = mux.Close()
	}()

	// shrink pool to force io.ErrShortBuffer in writePacket
	mux.pool = &sync.Pool{New: func() any { return newBufferHolder(8) }}

	// make connWorker route to new conn.
	c, err := mux.GetConn("ufragX", mux.LocalAddr())
	require.NoError(t, err)
	defer func() {
		_ = c.Close()
	}()

	// remote port is controlled. we use 5555 here to skip int overflow check as we would
	// otherwise have to cast remote.Port (int) to uint16.
	ipport, err := newIPPort(remote.IP, remote.Zone, 5555)
	require.NoError(t, err)

	cInner, ok := c.(*udpMuxedConn)
	require.True(t, ok, "expected *udpMuxedConn from UDPMuxDefault.GetConn")
	mux.registerConnForAddress(cInner, ipport)
}

func TestNewUDPMuxDefault_UnspecifiedAddr_AutoInitNet(t *testing.T) {
	defer test.CheckRoutines(t)()

	conn, err := net.ListenUDP(udp, &net.UDPAddr{IP: net.IPv4zero})
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	mux := NewUDPMuxDefault(UDPMuxParams{
		Logger:  nil,
		UDPConn: conn,
		Net:     nil,
	})
	require.NotNil(t, mux)
	defer func() { _ = mux.Close() }()

	addrs := mux.GetListenAddresses()
	require.GreaterOrEqual(t, len(addrs), 1, "should list at least one local listen address")

	udpAddr, ok := conn.LocalAddr().(*net.UDPAddr)
	require.True(t, ok, "LocalAddr is not *net.UDPAddr")
	wantPort := udpAddr.Port

	for _, a := range addrs {
		ua, ok := a.(*net.UDPAddr)
		require.True(t, ok, "returned listen address must be *net.UDPAddr")
		require.Equal(t, wantPort, ua.Port, "listen addresses should reuse the same UDP port")
	}
}
