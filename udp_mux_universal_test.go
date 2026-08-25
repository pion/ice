// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package ice

import (
	"encoding/binary"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/ice/v4/internal/fakenet"
	stunx "github.com/pion/ice/v4/internal/stun"
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

	testMuxSrflxConnection(t, udpMux, "ufrag4", udp)
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
	remoteAddr, ok := remoteConn.LocalAddr().(*net.UDPAddr)
	require.True(t, ok)
	// Use small value for TTL to check expiration of the address
	udpMux.params.XORMappedAddrCacheTTL = time.Millisecond * 20
	testXORIP := net.ParseIP("213.141.156.236")
	testXORPort := 21254

	type result struct {
		addr *stun.XORMappedAddress
		err  error
	}
	resultCh := make(chan result, 1)
	go func() {
		address, resultErr := udpMux.GetXORMappedAddr(remoteAddr, time.Second)
		resultCh <- result{address, resultErr}
	}()

	// Read the binding request.
	buf := make([]byte, receiveMTU)
	n, err := remoteConn.Read(buf)
	require.NoError(t, err)
	req := &stun.Message{Raw: append([]byte{}, buf[:n]...)}
	require.NoError(t, req.Decode())

	// Write back to udpMux XOR message with address
	addr := &stun.XORMappedAddress{
		IP:   testXORIP,
		Port: testXORPort,
	}
	msg, err := stun.Build(
		stun.NewTransactionIDSetter(req.TransactionID),
		stun.BindingSuccess,
		addr,
	)
	require.NoError(t, err)
	_, err = remoteConn.Write(msg.Raw)
	require.NoError(t, err)

	got := <-resultCh
	require.NoError(t, got.err)
	require.NotNil(t, got.addr)
	require.True(t, got.addr.IP.Equal(testXORIP))
	require.Equal(t, got.addr.Port, testXORPort)

	// We should get address immediately from the cached map
	address, err := udpMux.GetXORMappedAddr(remoteConn.LocalAddr(), time.Second)
	require.NoError(t, err)
	require.NotNil(t, address)

	// Check expiration by TTL
	time.Sleep(time.Millisecond * 21)

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

	// Unwrap the per-(ufrag,url) connections to compare identity.
	w1, ok := pc1.(*sharedAddrPortConn)
	require.True(t, ok, "pc1 is not *sharedAddrPortConn")
	w2, ok := pc2.(*sharedAddrPortConn)
	require.True(t, ok, "pc2 is not *sharedAddrPortConn")
	c1, ok := w1.underlying.(*udpMuxedConn)
	require.True(t, ok, "pc1 underlying is not *udpMuxedConn")
	c2, ok := w2.underlying.(*udpMuxedConn)
	require.True(t, ok, "pc2 underlying is not *udpMuxedConn")
	require.NotSame(t, c1, c2, "expected distinct muxed conns for different URLs with same ufrag")

	pc1b, err := udpMux.GetConnForURL("ufragX", "stun:serverA", lf)
	require.NoError(t, err)
	defer func() {
		_ = pc1b.Close()
	}()

	w1b, ok := pc1b.(*sharedAddrPortConn)
	require.True(t, ok, "pc1b is not *sharedAddrPortConn")
	c1b, ok := w1b.underlying.(*udpMuxedConn)
	require.True(t, ok, "pc1b underlying is not *udpMuxedConn")

	require.NotSame(t, w1, w1b, "GetConnForURL must return a fresh wrapper each call")
	require.Same(t, c1, c1b, "expected same underlying muxed conn when requesting the same (ufrag,url)")
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

func TestUniversalUDPMux_handleXORMappedResponse_RoutesByTransactionAndServer(t *testing.T) {
	serverAddr := canonicalAddrPort(netip.MustParseAddrPort("192.0.2.1:3478"))
	otherServerAddr := canonicalAddrPort(netip.MustParseAddrPort("192.0.2.2:3478"))
	transaction, err := stunx.NewXORMappedAddrTransaction()
	require.NoError(t, err)
	mux := &UniversalUDPMuxDefault{
		UDPMuxDefault: &UDPMuxDefault{},
		xorMappedTransactions: map[[stun.TransactionIDSize]byte]xorMappedTransaction{
			transaction.ID(): {transaction, serverAddr},
		},
	}

	response, err := stun.Build(
		stun.NewTransactionIDSetter(transaction.ID()),
		stun.BindingSuccess,
		&stun.XORMappedAddress{IP: net.IPv4(203, 0, 113, 8), Port: 51235},
	)
	require.NoError(t, err)
	mux.handleXORMappedResponse(otherServerAddr, response)
	_, cached := transaction.Cached(time.Minute)
	require.False(t, cached)
	mux.handleXORMappedResponse(serverAddr, response)
	_, cached = transaction.Cached(time.Minute)
	require.True(t, cached)
}

type wrappedUDPAddr struct {
	*net.UDPAddr
}

func TestUniversalUDPMux_GetXORMappedAddr_CustomAddrCacheKey(t *testing.T) {
	serverAddr := &wrappedUDPAddr{
		UDPAddr: &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 3478},
	}
	serverAddrPort := canonicalAddrPort(serverAddr.UDPAddr.AddrPort())
	mappedAddr := &stun.XORMappedAddress{IP: net.IPv4(203, 0, 113, 1), Port: 5000}
	transaction, err := stunx.NewXORMappedAddrTransaction()
	require.NoError(t, err)
	response, err := stun.Build(
		stun.NewTransactionIDSetter(transaction.ID()),
		stun.BindingSuccess,
		mappedAddr,
	)
	require.NoError(t, err)
	require.True(t, transaction.HandleResponse(response))
	mux := &UniversalUDPMuxDefault{
		UDPMuxDefault: &UDPMuxDefault{},
		params:        UniversalUDPMuxParams{XORMappedAddrCacheTTL: time.Minute},
		xorMappedMap: map[netip.AddrPort]*stunx.XORMappedAddrTransaction{
			serverAddrPort: transaction,
		},
	}

	got, err := mux.GetXORMappedAddr(serverAddr, 0)
	require.NoError(t, err)
	require.True(t, mappedAddr.IP.Equal(got.IP))
	require.Equal(t, mappedAddr.Port, got.Port)
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
		xorMappedMap:          make(map[netip.AddrPort]*stunx.XORMappedAddrTransaction),
		xorMappedTransactions: make(map[[stun.TransactionIDSize]byte]xorMappedTransaction),
	}

	addr, err := mux.GetXORMappedAddr(serverAddr, time.Second)
	require.Nil(t, addr)
	require.ErrorIs(t, err, errWriteSTUNMessage)
}

func TestUniversalUDPMux_GetXORMappedAddr_ConcurrentTransactions(t *testing.T) {
	serverAddr := &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 3478}
	serverAddrPort := canonicalAddrPort(serverAddr.AddrPort())
	wantAddr := &stun.XORMappedAddress{IP: net.IPv4(203, 0, 113, 9), Port: 51236}
	writes := make(chan struct{}, 2)
	releaseWrites := make(chan struct{})

	var (
		mux          *UniversalUDPMuxDefault
		requestCount atomic.Int32
	)
	pc := &writeHookPacketConn{
		onWrite: func(raw []byte) error {
			req := &stun.Message{Raw: append([]byte{}, raw...)}
			if err := req.Decode(); err != nil {
				return err
			}
			requestCount.Add(1)
			writes <- struct{}{}
			<-releaseWrites
			res, err := stun.Build(
				stun.NewTransactionIDSetter(req.TransactionID),
				stun.BindingSuccess,
				wantAddr,
			)
			if err != nil {
				return err
			}
			mux.handleXORMappedResponse(serverAddrPort, res)

			return nil
		},
	}
	mux = &UniversalUDPMuxDefault{
		UDPMuxDefault: &UDPMuxDefault{},
		params: UniversalUDPMuxParams{
			UDPConn:               pc,
			XORMappedAddrCacheTTL: time.Minute,
		},
		xorMappedMap:          make(map[netip.AddrPort]*stunx.XORMappedAddrTransaction),
		xorMappedTransactions: make(map[[stun.TransactionIDSize]byte]xorMappedTransaction),
	}

	results := make(chan *stun.XORMappedAddress, 2)
	errs := make(chan error, 2)
	getAddr := func() {
		addr, err := mux.GetXORMappedAddr(serverAddr, 2*time.Second)
		results <- addr
		errs <- err
	}
	go getAddr()
	go getAddr()
	<-writes
	<-writes
	close(releaseWrites)

	for range 2 {
		require.NoError(t, <-errs)
		addr := <-results
		require.True(t, wantAddr.IP.Equal(addr.IP))
		require.Equal(t, wantAddr.Port, addr.Port)
	}
	require.Equal(t, int32(2), requestCount.Load())
}

type writeHookPacketConn struct {
	net.PacketConn
	onWrite func([]byte) error
}

func (w *writeHookPacketConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	if w.onWrite != nil {
		if err := w.onWrite(p); err != nil {
			return 0, err
		}
	}

	return len(p), nil
}
