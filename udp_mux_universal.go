// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"time"

	stunx "github.com/pion/ice/v4/internal/stun"
	"github.com/pion/logging"
	"github.com/pion/stun/v3"
	"github.com/pion/transport/v4"
)

// UniversalUDPMux allows multiple connections to go over a single UDP port for
// host, server reflexive and relayed candidates.
// Actual connection muxing is happening in the UDPMux.
type UniversalUDPMux interface {
	UDPMux
	GetXORMappedAddr(stunAddr net.Addr, deadline time.Duration) (*stun.XORMappedAddress, error)
	GetRelayedAddr(turnAddr net.Addr, deadline time.Duration) (*net.Addr, error)
	GetConnForURL(ufrag string, url string, addr net.Addr) (net.PacketConn, error)
}

// UniversalUDPMuxDefault handles STUN and TURN servers packets by wrapping the original UDPConn overriding ReadFrom.
// It the passes packets to the UDPMux that does the actual connection muxing.
type UniversalUDPMuxDefault struct {
	*UDPMuxDefault
	params UniversalUDPMuxParams

	// Since we have a shared socket, for srflx candidates it makes sense
	// to have a shared mapped address across all the agents
	// stun.XORMappedAddress indexed by the STUN server addr
	xorMappedMap          map[netip.AddrPort]*stunx.XORMappedAddrTransaction
	xorMappedTransactions map[[stun.TransactionIDSize]byte]xorMappedTransaction
}

// UniversalUDPMuxParams are parameters for UniversalUDPMux server reflexive.
type UniversalUDPMuxParams struct {
	Logger logging.LeveledLogger
	// UDPConn may implement AddrPortReaderWriter to opt in to allocation-free
	// address handling. *net.UDPConn will automatically be adapted to
	// implement AddrPortReaderWriter.
	UDPConn               net.PacketConn
	XORMappedAddrCacheTTL time.Duration
	Net                   transport.Net
}

// NewUniversalUDPMuxDefault creates an implementation of UniversalUDPMux embedding UDPMux.
func NewUniversalUDPMuxDefault(params UniversalUDPMuxParams) *UniversalUDPMuxDefault {
	if params.Logger == nil {
		params.Logger = logging.NewDefaultLoggerFactory().NewLogger("ice")
	}
	if params.XORMappedAddrCacheTTL == 0 {
		params.XORMappedAddrCacheTTL = time.Second * 25
	}

	mux := &UniversalUDPMuxDefault{
		params:                params,
		xorMappedMap:          make(map[netip.AddrPort]*stunx.XORMappedAddrTransaction),
		xorMappedTransactions: make(map[[stun.TransactionIDSize]byte]xorMappedTransaction),
	}

	// Wrap UDP connection, process server reflexive messages
	// before they are passed to the UDPMux connection handler (connWorker)
	baseConn := &udpConn{
		PacketConn: params.UDPConn,
		mux:        mux,
		logger:     params.Logger,
	}
	var wrappedConn net.PacketConn = baseConn
	if addrPortConn := asAddrPortReaderWriter(params.UDPConn); addrPortConn != nil {
		wrappedConn = &udpAddrPortConn{
			udpConn:      baseConn,
			addrPortConn: addrPortConn,
		}
	}
	mux.params.UDPConn = wrappedConn

	// Embed UDPMux
	udpMuxParams := UDPMuxParams{
		Logger:  params.Logger,
		UDPConn: mux.params.UDPConn,
		Net:     mux.params.Net,
	}
	mux.UDPMuxDefault = NewUDPMuxDefault(udpMuxParams)

	return mux
}

// udpConn is a wrapper around UDPMux conn that overrides ReadFrom and handles STUN/TURN packets.
type udpConn struct {
	net.PacketConn
	mux    *UniversalUDPMuxDefault
	logger logging.LeveledLogger
}

// GetRelayedAddr creates relayed connection to the given TURN service and returns the relayed addr.
// Not implemented yet.
func (m *UniversalUDPMuxDefault) GetRelayedAddr(net.Addr, time.Duration) (*net.Addr, error) {
	return nil, errNotImplemented
}

// GetConnForURL add uniques to the muxed connection by concatenating ufrag and URL
// (e.g. STUN URL) to be able to support multiple STUN/TURN servers
// and return a unique connection per server.
func (m *UniversalUDPMuxDefault) GetConnForURL(ufrag string, url string, addr net.Addr) (net.PacketConn, error) {
	return m.UDPMuxDefault.GetConn(fmt.Sprintf("%s%s", ufrag, url), addr)
}

// ReadFrom is called by UDPMux connWorker and handles packets coming from the STUN server discovering a mapped address.
// It passes processed packets further to the UDPMux (maybe this is not really necessary).
func (c *udpConn) ReadFrom(buf []byte) (n int, addr net.Addr, err error) {
	n, addr, err = c.PacketConn.ReadFrom(buf)
	if err != nil {
		return n, addr, err
	}

	if stun.IsMessage(buf[:n]) {
		addrPort := netAddrToAddrPort(addr)
		if !addrPort.IsValid() {
			// Let UDPMuxDefault report the unexpected source address type.
			return n, addr, nil
		}

		c.handleSTUNMessage(buf[:n], addrPort)
	}

	return n, addr, nil
}

// udpAddrPortConn preserves netip.AddrPort I/O while udpConn intercepts STUN packets.
type udpAddrPortConn struct {
	*udpConn
	addrPortConn AddrPortReaderWriter
}

func (c *udpAddrPortConn) ReadFromAddrPort(buf []byte) (n int, addrPort netip.AddrPort, err error) {
	n, addrPort, err = c.addrPortConn.ReadFromAddrPort(buf)
	if err != nil {
		return n, addrPort, err
	}

	if stun.IsMessage(buf[:n]) {
		c.handleSTUNMessage(buf[:n], addrPort)
	}

	return n, addrPort, nil
}

func (c *udpAddrPortConn) WriteToAddrPort(buf []byte, addr netip.AddrPort) (int, error) {
	return c.addrPortConn.WriteToAddrPort(buf, addr)
}

// handleSTUNMessage intercepts XOR-mapped-address responses coming from known
// STUN servers before the packet is passed on to the UDPMux connWorker.
func (c *udpConn) handleSTUNMessage(buf []byte, stunAddr netip.AddrPort) {
	stunAddr = canonicalAddrPort(stunAddr)
	msg := &stun.Message{Raw: buf}

	if err := msg.Decode(); err != nil {
		c.logger.Warnf("Failed to handle decode ICE from %s: %v", stunAddr, err)

		return
	}

	c.mux.handleXORMappedResponse(stunAddr, msg)
}

// handleXORMappedResponse routes a decoded message to the transaction that owns
// its transaction ID. The transaction validates and parses the response.
func (m *UniversalUDPMuxDefault) handleXORMappedResponse(stunAddr netip.AddrPort, msg *stun.Message) {
	m.mu.Lock()
	transaction, ok := m.xorMappedTransactions[msg.TransactionID]
	m.mu.Unlock()

	if ok && transaction.serverAddr == stunAddr {
		transaction.HandleResponse(msg)
	}
}

// GetXORMappedAddr returns *stun.XORMappedAddress if already present for a given STUN server.
// Makes a STUN binding request to discover mapped address otherwise.
// Blocks until the stun.XORMappedAddress has been discovered or deadline.
// Method is safe for concurrent use.
func (m *UniversalUDPMuxDefault) GetXORMappedAddr(
	serverAddr net.Addr,
	deadline time.Duration,
) (*stun.XORMappedAddress, error) {
	return m.GetXORMappedAddrContext(context.Background(), serverAddr, deadline)
}

func (m *UniversalUDPMuxDefault) GetXORMappedAddrContext(
	ctx context.Context,
	serverAddr net.Addr,
	deadline time.Duration,
) (*stun.XORMappedAddress, error) {
	serverAddrPort := netAddrToAddrPort(serverAddr)
	if !serverAddrPort.IsValid() {
		return nil, errInvalidAddress
	}
	serverAddrPort = canonicalAddrPort(serverAddrPort)

	m.mu.Lock()
	if transaction := m.xorMappedMap[serverAddrPort]; transaction != nil {
		if addr, cached := transaction.Cached(m.params.XORMappedAddrCacheTTL); cached {
			m.mu.Unlock()

			return addr, nil
		}
	}
	transaction, err := stunx.NewXORMappedAddrTransaction()
	if err != nil {
		m.mu.Unlock()

		return nil, err
	}
	m.xorMappedTransactions[transaction.ID()] = xorMappedTransaction{transaction, serverAddrPort}
	m.mu.Unlock()

	addr, err := transaction.Get(ctx, deadline, func(ctx context.Context, request []byte) error {
		if _, writeErr := m.writePacket(ctx, request, serverAddr); writeErr != nil {
			return fmt.Errorf("%w: %s", errWriteSTUNMessage, writeErr) //nolint:errorlint
		}

		return nil
	})
	m.mu.Lock()
	delete(m.xorMappedTransactions, transaction.ID())
	if err == nil && addr != nil {
		m.xorMappedMap[serverAddrPort] = transaction
	}
	m.mu.Unlock()
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return nil, errXORMappedAddrTimeout
	}

	return addr, err
}

func (m *UniversalUDPMuxDefault) writePacket(ctx context.Context, packet []byte, addr net.Addr) (int, error) {
	if m.UDPMuxDefault != nil && m.UDPMuxDefault.params.UDPConn != nil {
		return m.UDPMuxDefault.writeToContext(ctx, packet, addr)
	}

	return m.params.UDPConn.WriteTo(packet, addr)
}

type xorMappedTransaction struct {
	*stunx.XORMappedAddrTransaction
	serverAddr netip.AddrPort
}
