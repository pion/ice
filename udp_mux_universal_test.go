// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pion/ice/v4/internal/fakenet"
	"github.com/pion/logging"
	"github.com/pion/stun/v3"
	"github.com/stretchr/testify/require"
)

func TestUniversalUDPMux(t *testing.T) {
	conn, err := net.ListenUDP(udp, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)

	udpMux := NewUniversalUDPMuxDefault(UniversalUDPMuxParams{
		Logger:  nil,
		UDPConn: conn,
	})

	defer func() {
		_ = udpMux.Close()
		_ = conn.Close()
	}()

	require.NotNil(t, udpMux.LocalAddr(), "tcpMux.LocalAddr() is nil")

	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		testMuxSrflxConnection(t, udpMux, "ufrag4", udp)
	}()

	wg.Wait()
}

func testMuxSrflxConnection(t *testing.T, udpMux *UniversalUDPMuxDefault, ufrag string, network string) {
	t.Helper()

	pktConn, err := udpMux.GetConn(ufrag, udpMux.LocalAddr())
	require.NoError(t, err, "error retrieving muxed connection for ufrag")
	defer func() {
		_ = pktConn.Close()
	}()

	remoteConn, err := net.DialUDP(network, nil, &net.UDPAddr{ // nolint
		Port: udpMux.LocalAddr().(*net.UDPAddr).Port,
	})
	require.NoError(t, err, "error dialing test UDP connection")
	defer func() {
		_ = remoteConn.Close()
	}()

	// Use small value for TTL to check expiration of the address
	udpMux.params.XORMappedAddrCacheTTL = time.Millisecond * 20
	testXORIP := net.ParseIP("213.141.156.236")
	testXORPort := 21254

	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		address, e := udpMux.GetXORMappedAddr(remoteConn.LocalAddr(), time.Second)
		require.NoError(t, e)
		require.NotNil(t, address)
		require.True(t, address.IP.Equal(testXORIP))
		require.Equal(t, address.Port, testXORPort)
	}()

	// Wait until GetXORMappedAddr calls sendSTUN method
	time.Sleep(time.Millisecond)

	// Check that mapped address filled correctly after sent STUN
	udpMux.mu.Lock()
	mappedAddr, ok := udpMux.xorMappedMap[remoteConn.LocalAddr().String()]
	require.True(t, ok)
	require.NotNil(t, mappedAddr)
	require.True(t, mappedAddr.pending())
	require.False(t, mappedAddr.expired())
	udpMux.mu.Unlock()

	// Clean receiver read buffer
	buf := make([]byte, receiveMTU)
	_, err = remoteConn.Read(buf)
	require.NoError(t, err)

	// Write back to udpMux XOR message with address
	msg := stun.New()
	msg.Type = stun.MessageType{Method: stun.MethodBinding, Class: stun.ClassRequest}
	msg.Add(stun.AttrUsername, []byte(ufrag+":otherufrag"))
	addr := &stun.XORMappedAddress{
		IP:   testXORIP,
		Port: testXORPort,
	}
	err = addr.AddTo(msg)
	require.NoError(t, err)

	msg.Encode()
	_, err = remoteConn.Write(msg.Raw)
	require.NoError(t, err)

	// Wait for the packet to be consumed and parsed by udpMux
	wg.Wait()

	// We should get address immediately from the cached map
	address, err := udpMux.GetXORMappedAddr(remoteConn.LocalAddr(), time.Second)
	require.NoError(t, err)
	require.NotNil(t, address)

	udpMux.mu.Lock()
	// Check mappedAddr is not pending, we didn't send STUN twice
	require.False(t, mappedAddr.pending())

	// Check expiration by TTL
	time.Sleep(time.Millisecond * 21)
	require.True(t, mappedAddr.expired())
	udpMux.mu.Unlock()

	// After expire, we send STUN request again
	// but we not receive response in 5 milliseconds and should get error here
	address, err = udpMux.GetXORMappedAddr(remoteConn.LocalAddr(), time.Millisecond*5)
	require.NotNil(t, err)
	require.Nil(t, address)
}

func TestUniversalUDPMux_GetConnForURL_UniquePerURL(t *testing.T) {
	conn, err := net.ListenUDP(udp, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)

	udpMux := NewUniversalUDPMuxDefault(UniversalUDPMuxParams{
		Logger:  nil,
		UDPConn: conn,
	})
	defer func() {
		_ = udpMux.Close()
		_ = conn.Close()
	}()

	lf := udpMux.LocalAddr()
	require.NotNil(t, lf)

	// different URLs -> must be distinct muxed conns
	pc1, err := udpMux.GetConnForURL("ufragX", "stun:serverA", lf)
	require.NoError(t, err)
	defer func() {
		_ = pc1.Close()
	}()

	pc2, err := udpMux.GetConnForURL("ufragX", "stun:serverB", lf)
	require.NoError(t, err)
	defer func() {
		_ = pc2.Close()
	}()

	c1, ok := pc1.(*udpMuxedConn)
	require.True(t, ok, "pc1 is not *udpMuxedConn")
	c2, ok := pc2.(*udpMuxedConn)
	require.True(t, ok, "pc2 is not *udpMuxedConn")
	require.NotEqual(t, c1, c2, "expected distinct muxed conns for different URLs with same ufrag")

	pc1b, err := udpMux.GetConnForURL("ufragX", "stun:serverA", lf)
	require.NoError(t, err)
	defer func() {
		_ = pc1b.Close()
	}()

	c1b, ok := pc1b.(*udpMuxedConn)
	require.True(t, ok, "pc1b is not *udpMuxedConn")

	require.Equal(t, c1, c1b, "expected same muxed conn when requesting the same (ufrag,url)")
}

func newLogger() logging.LeveledLogger {
	return logging.NewDefaultLoggerFactory().NewLogger("ice")
}

func newFakenetReader(t *testing.T, payload []byte) *fakenet.PacketConn {
	t.Helper()
	r, w := net.Pipe()
	go func() {
		_, _ = w.Write(payload)
		_ = w.Close()
	}()
	pc := &fakenet.PacketConn{}
	pc.Conn = r

	return pc
}

func Test_udpConn_ReadFrom_STUNDecodeError(t *testing.T) {
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })

	srvAddr, ok := server.LocalAddr().(*net.UDPAddr)
	require.True(t, ok, "server.LocalAddr is not *net.UDPAddr")

	client, err := net.DialUDP("udp4", nil, srvAddr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	// build a valid STUN Binding Request then corrupt the header length field.
	msg := stun.New()
	msg.Type = stun.MessageType{Method: stun.MethodBinding, Class: stun.ClassRequest}
	msg.Encode()
	raw := append([]byte{}, msg.Raw...)
	decl := binary.BigEndian.Uint16(raw[2:4])
	binary.BigEndian.PutUint16(raw[2:4], decl+4) // makes Decode() fail

	_, err = client.Write(raw)
	require.NoError(t, err)

	u := &udpConn{PacketConn: server, mux: nil, logger: newLogger()}
	_ = server.SetReadDeadline(time.Now().Add(time.Second))

	buf := make([]byte, 1500)
	n, addr, gotErr := u.ReadFrom(buf)

	require.Equal(t, len(raw), n)
	require.IsType(t, &net.UDPAddr{}, addr)
	require.NoError(t, gotErr)
}

func Test_udpConn_ReadFrom_AddrNotUDP(t *testing.T) {
	msg := stun.New()
	msg.Type = stun.MessageType{Method: stun.MethodBinding, Class: stun.ClassRequest}
	msg.Encode()

	pc := newFakenetReader(t, msg.Raw)
	u := &udpConn{PacketConn: pc, mux: nil, logger: newLogger()}

	buf := make([]byte, 1500)
	n, addr, gotErr := u.ReadFrom(buf)

	require.Equal(t, len(msg.Raw), n)
	require.NoError(t, gotErr)

	require.NotNil(t, addr)
	_, isUDP := addr.(*net.UDPAddr)
	require.False(t, isUDP, "expected a non-UDP addr from fakenet.PacketConn")
}

func Test_udpConn_ReadFrom_XOR(t *testing.T) {
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })

	srvAddr, ok := server.LocalAddr().(*net.UDPAddr)
	require.True(t, ok, "server.LocalAddr is not *net.UDPAddr")

	client, err := net.DialUDP("udp4", nil, srvAddr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	// success response + short XORMappedAddress value will make GetFrom() fail.
	msg := stun.New()
	msg.Type = stun.MessageType{Method: stun.MethodBinding, Class: stun.ClassSuccessResponse}
	msg.Add(stun.AttrXORMappedAddress, []byte{0x00}) // intentionally invalid
	msg.Encode()

	mux := &UniversalUDPMuxDefault{
		UDPMuxDefault: &UDPMuxDefault{},
		xorMappedMap: map[string]*xorMapped{
			client.LocalAddr().String(): {
				waitAddrReceived: make(chan struct{}),
				expiresAt:        time.Now().Add(time.Minute),
			},
		},
	}

	_, err = client.Write(msg.Raw)
	require.NoError(t, err)

	u := &udpConn{PacketConn: server, mux: mux, logger: newLogger()}
	_ = server.SetReadDeadline(time.Now().Add(time.Second))

	buf := make([]byte, 1500)
	n, addr, gotErr := u.ReadFrom(buf)

	require.Equal(t, len(msg.Raw), n)
	require.IsType(t, &net.UDPAddr{}, addr)
	require.NoError(t, gotErr)
}

func Test_udpConn_ReadFrom_NonSTUN(t *testing.T) {
	payload := []byte("not a stun packet")
	pc := newFakenetReader(t, payload)

	u := &udpConn{PacketConn: pc, mux: nil, logger: newLogger()}

	buf := make([]byte, 1500)
	n, addr, gotErr := u.ReadFrom(buf)

	require.NoError(t, gotErr)
	require.Equal(t, len(payload), n)
	require.Equal(t, payload, buf[:n])

	require.NotNil(t, addr)
	_, isUDP := addr.(*net.UDPAddr)
	require.False(t, isUDP, "expected a non-UDP addr from fakenet.PacketConn")
}

func TestUniversalUDPMux_handleXORMappedResponse_NoMapping(t *testing.T) {
	mux := &UniversalUDPMuxDefault{
		UDPMuxDefault: &UDPMuxDefault{},
		xorMappedMap:  make(map[string]*xorMapped),
	}

	stunSrv := &net.UDPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 3478}
	msg := stun.New()

	err := mux.handleXORMappedResponse(stunSrv, msg)
	require.ErrorIs(t, err, errNoXorAddrMapping)
}

func newFakePC(t *testing.T) (*fakenet.PacketConn, net.Conn, net.Conn) {
	t.Helper()
	c1, c2 := net.Pipe()
	pc := &fakenet.PacketConn{}
	pc.Conn = c1

	return pc, c1, c2
}

func TestUniversalUDPMux_GetXORMappedAddr_Pending_WriteError(t *testing.T) {
	serverAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 3478}

	pc, c1, c2 := newFakePC(t)
	_ = c2.Close() // other end unused
	_ = c1.Close() // force future WriteTo to error

	mux := &UniversalUDPMuxDefault{
		UDPMuxDefault: &UDPMuxDefault{},
		params: UniversalUDPMuxParams{
			UDPConn: pc, // writeSTUN will call WriteTo on this fakenet PacketConn
		},
		xorMappedMap: map[string]*xorMapped{
			serverAddr.String(): {
				waitAddrReceived: make(chan struct{}),
				expiresAt:        time.Now().Add(time.Minute),
			},
		},
	}

	addr, err := mux.GetXORMappedAddr(serverAddr, time.Second)
	require.Nil(t, addr)
	require.ErrorIs(t, err, errWriteSTUNMessage)
}

func TestUniversalUDPMux_GetXORMappedAddr_WaitClosed_NoAddr(t *testing.T) {
	serverAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 3478}

	pc, c1, c2 := newFakePC(t)
	drainDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, c2)
		close(drainDone)
	}()
	t.Cleanup(func() {
		_ = c1.Close()
		_ = c2.Close()
		<-drainDone
	})

	waitCh := make(chan struct{})
	close(waitCh)

	mux := &UniversalUDPMuxDefault{
		UDPMuxDefault: &UDPMuxDefault{},
		params: UniversalUDPMuxParams{
			UDPConn: pc,
		},
		xorMappedMap: map[string]*xorMapped{
			serverAddr.String(): {
				addr:             nil,
				waitAddrReceived: waitCh,
				expiresAt:        time.Now().Add(time.Minute),
			},
		},
	}

	addr, err := mux.GetXORMappedAddr(serverAddr, time.Second)
	require.Nil(t, addr)
	require.ErrorIs(t, err, errNoXorAddrMapping)
}
