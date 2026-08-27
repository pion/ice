// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package stun

import (
	"context"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/stun/v3"
	"github.com/pion/transport/v4/vnet"
	"github.com/stretchr/testify/require"
)

type stunTestNetwork struct {
	client   net.PacketConn
	server   net.PacketConn
	attacker net.PacketConn
	requests atomic.Int32
}

type blockingPacketConn struct {
	net.PacketConn
	started    chan struct{}
	unblock    chan struct{}
	blockWrite bool
	once       sync.Once
}

func (c *blockingPacketConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	close(c.started)
	if c.blockWrite {
		<-c.unblock

		return 0, os.ErrDeadlineExceeded
	}

	return len(p), nil
}

func (c *blockingPacketConn) ReadFrom([]byte) (int, net.Addr, error) {
	<-c.unblock

	return 0, nil, os.ErrDeadlineExceeded
}

func (c *blockingPacketConn) SetDeadline(deadline time.Time) error {
	if !deadline.IsZero() && !deadline.After(time.Now()) {
		c.once.Do(func() { close(c.unblock) })
	}

	return nil
}

func newSTUNTestNetwork(
	t *testing.T,
	dropRequests int32,
	mappedAddr stun.Setter,
	sendInvalidPackets bool,
) *stunTestNetwork {
	t.Helper()

	const (
		clientIP   = "192.0.2.1"
		serverIP   = "192.0.2.2"
		serverPort = 3478
	)

	router, err := vnet.NewRouter(&vnet.RouterConfig{
		CIDR:          "192.0.2.0/24",
		LoggerFactory: logging.NewDefaultLoggerFactory(),
	})
	require.NoError(t, err)

	clientNet, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{clientIP}})
	require.NoError(t, err)
	require.NoError(t, router.AddNet(clientNet))
	serverNet, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{serverIP}})
	require.NoError(t, err)
	require.NoError(t, router.AddNet(serverNet))

	network := &stunTestNetwork{}
	router.AddChunkFilter(func(chunk vnet.Chunk) bool {
		destination, ok := chunk.DestinationAddr().(*net.UDPAddr)
		if !ok || !destination.IP.Equal(net.ParseIP(serverIP)) || destination.Port != serverPort {
			return true
		}

		seen := network.requests.Add(1)

		return dropRequests >= 0 && seen > dropRequests
	})
	require.NoError(t, router.Start())

	network.server, err = serverNet.ListenPacket("udp4", net.JoinHostPort(serverIP, "3478"))
	require.NoError(t, err)
	network.attacker, err = serverNet.ListenPacket("udp4", net.JoinHostPort(serverIP, "0"))
	require.NoError(t, err)
	network.client, err = clientNet.ListenPacket("udp4", net.JoinHostPort(clientIP, "0"))
	require.NoError(t, err)

	t.Cleanup(func() {
		require.NoError(t, network.client.Close())
		require.NoError(t, network.server.Close())
		require.NoError(t, network.attacker.Close())
		require.NoError(t, router.Stop())
	})

	go func() {
		buf := make([]byte, 1280)
		for {
			n, clientAddr, readErr := network.server.ReadFrom(buf)
			if readErr != nil {
				return
			}

			req := &stun.Message{Raw: buf[:n]}
			if req.Decode() != nil {
				continue
			}

			setters := []stun.Setter{
				stun.NewTransactionIDSetter(req.TransactionID),
				stun.BindingSuccess,
			}
			if mappedAddr != nil {
				setters = append(setters, mappedAddr)
			}
			res := stun.MustBuild(setters...)
			if sendInvalidPackets {
				_, _ = network.server.WriteTo([]byte("invalid"), clientAddr)
				_, _ = network.attacker.WriteTo(res.Raw, clientAddr)
			}

			_, _ = network.server.WriteTo(res.Raw, clientAddr)
		}
	}()

	return network
}

func TestXORMappedAddrTransactionRunPacketConn(t *testing.T) {
	reflexiveIP := net.IPv4(203, 0, 113, 7)
	const reflexivePort = 51234
	xorMappedAddr := &stun.XORMappedAddress{IP: reflexiveIP, Port: reflexivePort}

	for _, testCase := range []struct {
		name           string
		dropped        int32
		requests       int32
		mappedAddr     stun.Setter
		invalidPackets bool
	}{
		{name: "NoLoss", requests: 1, mappedAddr: xorMappedAddr},
		{name: "RecoversFromConsecutiveLosses", dropped: 2, requests: 3, mappedAddr: xorMappedAddr},
		{
			name:       "LegacyMappedAddress",
			requests:   1,
			mappedAddr: &stun.MappedAddress{IP: reflexiveIP, Port: reflexivePort},
		},
		{name: "DiscardsInvalidPackets", requests: 1, mappedAddr: xorMappedAddr, invalidPackets: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			network := newSTUNTestNetwork(t, testCase.dropped, testCase.mappedAddr, testCase.invalidPackets)
			transaction, err := NewXORMappedAddrTransaction()
			require.NoError(t, err)

			addr, err := transaction.RunPacketConn(
				context.Background(),
				network.client,
				network.server.LocalAddr(),
				5*time.Second,
			)
			require.NoError(t, err)
			require.True(t, addr.IP.Equal(reflexiveIP))
			require.Equal(t, reflexivePort, addr.Port)
			require.Equal(t, testCase.requests, network.requests.Load())
		})
	}

	t.Run("TimeoutBoundsTransaction", func(t *testing.T) {
		network := newSTUNTestNetwork(t, -1, xorMappedAddr, false)
		transaction, err := NewXORMappedAddrTransaction()
		require.NoError(t, err)

		const timeout = 750 * time.Millisecond
		start := time.Now()
		addr, err := transaction.RunPacketConn(context.Background(), network.client, network.server.LocalAddr(), timeout)
		elapsed := time.Since(start)

		require.Nil(t, addr)
		require.True(t, os.IsTimeout(err))
		require.Less(t, elapsed, timeout+250*time.Millisecond)
		require.Greater(t, network.requests.Load(), int32(1))
	})

	t.Run("MissingMappedAddressIsTerminal", func(t *testing.T) {
		network := newSTUNTestNetwork(t, 0, nil, false)
		transaction, err := NewXORMappedAddrTransaction()
		require.NoError(t, err)

		addr, err := transaction.RunPacketConn(
			context.Background(),
			network.client,
			network.server.LocalAddr(),
			5*time.Second,
		)
		require.ErrorIs(t, err, errGetXorMappedAddrResponse)
		require.Nil(t, addr)
		require.EqualValues(t, 1, network.requests.Load())
	})
}

func TestXORMappedAddrTransactionRunPacketConnCancelsBlockedWrite(t *testing.T) {
	transaction, err := NewXORMappedAddrTransaction()
	require.NoError(t, err)
	conn := &blockingPacketConn{started: make(chan struct{}), unblock: make(chan struct{}), blockWrite: true}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-conn.started
		cancel()
	}()

	addr, err := transaction.RunPacketConn(ctx, conn, &net.UDPAddr{}, time.Second)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, addr)
}

func TestXORMappedAddrTransactionRunPacketConnCancelsBlockedRead(t *testing.T) {
	transaction, err := NewXORMappedAddrTransaction()
	require.NoError(t, err)
	conn := &blockingPacketConn{started: make(chan struct{}), unblock: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-conn.started
		cancel()
	}()

	addr, err := transaction.RunPacketConn(ctx, conn, &net.UDPAddr{}, time.Second)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, addr)
}

func TestXORMappedAddrTransactionDiscardsUnrelatedResponses(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		method     stun.Method
		class      stun.MessageClass
		mismatchID bool
	}{
		{name: "TransactionID", method: stun.MethodBinding, class: stun.ClassSuccessResponse, mismatchID: true},
		{name: "Method", method: stun.MethodAllocate, class: stun.ClassSuccessResponse},
		{name: "Class", method: stun.MethodBinding, class: stun.ClassRequest},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			transaction, err := NewXORMappedAddrTransaction()
			require.NoError(t, err)
			responseID := transaction.ID()
			if testCase.mismatchID {
				responseID[0] ^= 0xff
			}
			response, err := stun.Build(
				stun.NewTransactionIDSetter(responseID),
				stun.NewType(testCase.method, testCase.class),
				&stun.XORMappedAddress{IP: net.IPv4(203, 0, 113, 7), Port: 51234},
			)
			require.NoError(t, err)

			require.False(t, transaction.HandleResponse(response))
		})
	}
}

func TestXORMappedAddrTransactionReportsErrorResponse(t *testing.T) {
	transaction, err := NewXORMappedAddrTransaction()
	require.NoError(t, err)
	response, err := stun.Build(
		stun.NewTransactionIDSetter(transaction.ID()),
		stun.BindingError,
		stun.ErrorCodeAttribute{Code: stun.CodeBadRequest, Reason: []byte("Bad Request")},
	)
	require.NoError(t, err)

	handled := false
	_, err = transaction.Get(context.Background(), time.Second, func(context.Context, []byte) error {
		handled = transaction.HandleResponse(response)

		return nil
	})
	var stunErr stun.TurnError
	require.ErrorAs(t, err, &stunErr)
	require.Equal(t, stun.CodeBadRequest, stunErr.ErrorCodeAttr.Code)
	require.True(t, handled)
}

func TestXORMappedAddrTransactionPrefersCanceledContextToSendError(t *testing.T) {
	transaction, err := NewXORMappedAddrTransaction()
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())

	_, err = transaction.Get(ctx, time.Second, func(context.Context, []byte) error {
		cancel()

		return net.ErrClosed
	})
	require.ErrorIs(t, err, context.Canceled)
}

func TestXORMappedAddrTransactionTimeoutIncludesSend(t *testing.T) {
	transaction, err := NewXORMappedAddrTransaction()
	require.NoError(t, err)
	start := time.Now()

	_, err = transaction.Get(context.Background(), 50*time.Millisecond, func(ctx context.Context, _ []byte) error {
		<-ctx.Done()

		return ctx.Err()
	})
	require.True(t, os.IsTimeout(err))
	require.Less(t, time.Since(start), 250*time.Millisecond)
}
