// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"time"

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
	xorMappedMap map[netip.AddrPort]*xorMapped
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
		params:       params,
		xorMappedMap: make(map[netip.AddrPort]*xorMapped),
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
	msg := &stun.Message{
		Raw: append([]byte{}, buf...),
	}

	if err := msg.Decode(); err != nil {
		c.logger.Warnf("Failed to handle decode ICE from %s: %v", stunAddr, err)

		return
	}

	if c.mux.isXORMappedResponse(msg, stunAddr) {
		if err := c.mux.handleXORMappedResponse(stunAddr, msg); err != nil {
			c.logger.Debugf("%w: %v", errGetXorMappedAddrResponse, err)
		}
	}
}

// isXORMappedResponse indicates whether the message is a XORMappedAddress and is coming from the known STUN server.
func (m *UniversalUDPMuxDefault) isXORMappedResponse(msg *stun.Message, stunAddr netip.AddrPort) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Check first if it is a STUN server address,
	// because remote peer can also send similar messages but as a BindingSuccess.
	_, ok := m.xorMappedMap[stunAddr]
	_, err := msg.Get(stun.AttrXORMappedAddress)

	return err == nil && ok
}

// handleXORMappedResponse parses response from the STUN server, extracts XORMappedAddress attribute.
// and set the mapped address for the server.
func (m *UniversalUDPMuxDefault) handleXORMappedResponse(stunAddr netip.AddrPort, msg *stun.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	mappedAddr, ok := m.xorMappedMap[stunAddr]
	if !ok {
		return errNoXorAddrMapping
	}

	var addr stun.XORMappedAddress
	if err := addr.GetFrom(msg); err != nil {
		return err
	}

	mappedAddr.SetAddr(&addr)

	return nil
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

	if mappedAddr, ok := m.cachedXORMappedAddr(serverAddrPort); ok {
		return mappedAddr, nil
	}

	// Otherwise, make a STUN request to discover the address
	// or wait for already sent request to complete
	waitAddrReceived, err := m.writeSTUN(ctx, serverAddr, serverAddrPort)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}

		return nil, fmt.Errorf("%w: %s", errWriteSTUNMessage, err) //nolint:errorlint
	}

	timer := time.NewTimer(deadline)
	defer timer.Stop()

	// Block until response was handled by the connWorker routine and XORMappedAddress was updated
	select {
	case <-waitAddrReceived:
		// When channel closed, addr was obtained
		m.mu.Lock()
		mappedAddr, ok := m.xorMappedMap[serverAddrPort]
		if !ok || mappedAddr.addr == nil {
			m.mu.Unlock()

			return nil, errNoXorAddrMapping
		}
		addr := mappedAddr.addr
		m.mu.Unlock()

		return addr, nil
	case <-timer.C:
		return nil, errXORMappedAddrTimeout
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *UniversalUDPMuxDefault) cachedXORMappedAddr(serverAddr netip.AddrPort) (*stun.XORMappedAddress, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	mappedAddr, ok := m.xorMappedMap[serverAddr]
	// If we already have a mapping for this STUN server (address already received)
	// and if it is not too old we return it without making a new request to STUN server
	if !ok {
		return nil, false
	}
	if mappedAddr.expired() {
		mappedAddr.closeWaiters()
		delete(m.xorMappedMap, serverAddr)

		return nil, false
	}
	if mappedAddr.pending() {
		return nil, false
	}

	return mappedAddr.addr, true
}

// writeSTUN sends a STUN request via UDP conn.
//
// The returned channel is closed when the STUN response has been received.
// Method is safe for concurrent use.
func (m *UniversalUDPMuxDefault) writeSTUN(
	ctx context.Context,
	serverAddr net.Addr,
	serverAddrPort netip.AddrPort,
) (chan struct{}, error) {
	m.mu.Lock()

	// If record present in the map, we already sent a STUN request,
	// just wait when waitAddrReceived will be closed
	addrMap, ok := m.xorMappedMap[serverAddrPort]
	if !ok {
		addrMap = &xorMapped{
			expiresAt:        time.Now().Add(m.params.XORMappedAddrCacheTTL),
			waitAddrReceived: make(chan struct{}),
		}
		m.xorMappedMap[serverAddrPort] = addrMap
	}
	waitAddrReceived := addrMap.waitAddrReceived
	m.mu.Unlock()

	req, err := stun.Build(stun.BindingRequest, stun.TransactionID)
	if err != nil {
		return nil, err
	}

	if _, err = m.writePacket(ctx, req.Raw, serverAddr); err != nil {
		return nil, err
	}

	return waitAddrReceived, nil
}

func (m *UniversalUDPMuxDefault) writePacket(ctx context.Context, packet []byte, addr net.Addr) (int, error) {
	if m.UDPMuxDefault != nil && m.UDPMuxDefault.params.UDPConn != nil {
		return m.UDPMuxDefault.writeToContext(ctx, packet, addr)
	}

	return m.params.UDPConn.WriteTo(packet, addr)
}

type xorMapped struct {
	addr             *stun.XORMappedAddress
	waitAddrReceived chan struct{}
	expiresAt        time.Time
}

func (a *xorMapped) closeWaiters() {
	select {
	case <-a.waitAddrReceived:
		// Notify was close, ok, that means we received duplicate response just exit
		break
	default:
		// Notify tha twe have a new addr
		close(a.waitAddrReceived)
	}
}

func (a *xorMapped) pending() bool {
	return a.addr == nil
}

func (a *xorMapped) expired() bool {
	return a.expiresAt.Before(time.Now())
}

func (a *xorMapped) SetAddr(addr *stun.XORMappedAddress) {
	a.addr = addr
	a.closeWaiters()
}
