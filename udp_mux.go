// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/logging"
	"github.com/pion/stun/v3"
	"github.com/pion/transport/v4"
	"github.com/pion/transport/v4/stdnet"
)

// UDPMux allows multiple connections to go over a single UDP port.
type UDPMux interface {
	io.Closer
	GetConn(ufrag string, addr net.Addr) (net.PacketConn, error)
	RemoveConnByUfrag(ufrag string)
	GetListenAddresses() []net.Addr
}

// UDPMuxDefault is an implementation of the interface.
type UDPMuxDefault struct {
	params UDPMuxParams

	closedChan chan struct{}
	closeOnce  sync.Once

	// connsIPv4 and connsIPv6 are maps of all udpMuxedConn indexed by ufrag|network|candidateType
	connsIPv4, connsIPv6 map[string]*udpMuxedConn

	addressMapMu sync.RWMutex
	addressMap   map[ipPort]*udpMuxedConn

	// Buffer pool to recycle buffers for net.UDPAddr encodes/decodes
	pool *sync.Pool

	mu sync.Mutex

	// whether the UDP connection listens on an unspecified address
	isUnspecified bool

	// writeState coordinates context cancellation for WriteTo calls.
	// Low bits count writes currently inside UDPConn.WriteTo. blocked means an
	// abort arming the shared write deadline, so new writes wait.
	// deadline means SetWriteDeadline(time.Now()) succeeded and the last
	// in-flight writer must clear it before new writes can enter.
	writeState atomic.Uint64
}

const (
	udpMuxWriteBlockedBit  = uint64(1) << 63
	udpMuxWriteDeadlineBit = uint64(1) << 62
	udpMuxWriteCountMask   = udpMuxWriteDeadlineBit - 1
)

// UDPMuxParams are parameters for UDPMux.
type UDPMuxParams struct {
	Logger        logging.LeveledLogger
	UDPConn       net.PacketConn
	UDPConnString string

	// Required for gathering local addresses
	// in case a un UDPConn is passed which does not
	// bind to a specific local address.
	Net transport.Net
}

// NewUDPMuxDefault creates an implementation of UDPMux.
func NewUDPMuxDefault(params UDPMuxParams) *UDPMuxDefault {
	if params.Logger == nil {
		params.Logger = logging.NewDefaultLoggerFactory().NewLogger("ice")
	}

	var isUnspecified bool
	if udpAddr, ok := params.UDPConn.LocalAddr().(*net.UDPAddr); !ok {
		params.Logger.Errorf("LocalAddr is not a net.UDPAddr, got %T", params.UDPConn.LocalAddr())
	} else if ok && udpAddr.IP.IsUnspecified() {
		// For unspecified addresses, the correct behavior is to return errListenUnspecified, but
		// it will break the applications that are already using unspecified UDP connection
		// with UDPMuxDefault, so print a warn log.
		params.Logger.Warn("UDPMuxDefault should not listening on unspecified address, use NewMultiUDPMuxFromPort instead")
		isUnspecified = true
		if params.Net == nil {
			var err error
			if params.Net, err = stdnet.NewNet(); err != nil {
				params.Logger.Errorf("Failed to create network: %v", err)
			}
		}
	}
	params.UDPConnString = params.UDPConn.LocalAddr().String()

	mux := &UDPMuxDefault{
		addressMap: map[ipPort]*udpMuxedConn{},
		params:     params,
		connsIPv4:  make(map[string]*udpMuxedConn),
		connsIPv6:  make(map[string]*udpMuxedConn),
		closedChan: make(chan struct{}, 1),
		pool: &sync.Pool{
			New: func() any {
				// Big enough buffer to fit both packet and address
				return newBufferHolder(receiveMTU)
			},
		},
		isUnspecified: isUnspecified,
	}
	go mux.connWorker()

	return mux
}

// LocalAddr returns the listening address of this UDPMuxDefault.
func (m *UDPMuxDefault) LocalAddr() net.Addr {
	return m.params.UDPConn.LocalAddr()
}

// GetListenAddresses returns the list of addresses that this mux is listening on.
func (m *UDPMuxDefault) GetListenAddresses() []net.Addr {
	if m.isUnspecified {
		udpAddr, ok := m.params.UDPConn.LocalAddr().(*net.UDPAddr)
		if !ok {
			m.params.Logger.Errorf("Failed to get local UDP address")

			return []net.Addr{m.LocalAddr()}
		}

		var networks []NetworkType
		switch {
		case udpAddr.IP.To4() != nil:
			networks = []NetworkType{NetworkTypeUDP4}
		case udpAddr.IP.To16() != nil:
			networks = []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6}
		default:
			return []net.Addr{m.LocalAddr()}
		}

		_, addrs, err := localInterfaces(m.params.Net, nil, nil, networks, true)
		if err != nil {
			m.params.Logger.Errorf("Failed to get local interfaces: %v", err)

			return []net.Addr{m.LocalAddr()}
		}

		result := make([]net.Addr, len(addrs))
		for i, addr := range addrs {
			result[i] = &net.UDPAddr{
				IP:   addr.addr.AsSlice(),
				Port: udpAddr.Port,
				Zone: addr.addr.Zone(),
			}
		}

		return result
	}

	return []net.Addr{m.LocalAddr()}
}

// GetConn returns a PacketConn given the connection's ufrag and network address.
// creates the connection if an existing one can't be found. The returned conn
// is a refcounted — repeat calls return connection with increased refcount, so
// the connection's Close method should be called for each GetConn call to avoid leaks.
func (m *UDPMuxDefault) GetConn(ufrag string, addr net.Addr) (net.PacketConn, error) {
	// don't check addr for mux using unspecified address
	if !m.isUnspecified && m.params.UDPConnString != addr.String() {
		return nil, errInvalidAddress
	}

	var isIPv6 bool
	if udpAddr, _ := addr.(*net.UDPAddr); udpAddr != nil && udpAddr.IP.To4() == nil {
		isIPv6 = true
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.IsClosed() {
		return nil, io.ErrClosedPipe
	}

	muxedConn, ok := m.getConn(ufrag, isIPv6)
	if !ok {
		muxedConn = m.createMuxedConn(ufrag)
		go func() {
			<-muxedConn.CloseChannel()
			m.RemoveConnByUfrag(ufrag)
		}()

		if isIPv6 {
			m.connsIPv6[ufrag] = muxedConn
		} else {
			m.connsIPv4[ufrag] = muxedConn
		}
	}

	return newSharedPacketConn(muxedConn, &muxedConn.refs), nil
}

// RemoveConnByUfrag stops and removes the muxed packet connection.
func (m *UDPMuxDefault) RemoveConnByUfrag(ufrag string) {
	removedConns := make([]*udpMuxedConn, 0, 2)

	// Keep lock section small to avoid deadlock with conn lock.
	m.mu.Lock()
	if c, ok := m.connsIPv4[ufrag]; ok {
		delete(m.connsIPv4, ufrag)
		removedConns = append(removedConns, c)
	}
	if c, ok := m.connsIPv6[ufrag]; ok {
		delete(m.connsIPv6, ufrag)
		removedConns = append(removedConns, c)
	}
	m.mu.Unlock()

	if len(removedConns) == 0 {
		// No need to lock if no connection was found.
		return
	}

	m.addressMapMu.Lock()
	defer m.addressMapMu.Unlock()

	for _, c := range removedConns {
		addresses := c.getAddresses()
		for _, addr := range addresses {
			delete(m.addressMap, addr)
		}
	}
}

// IsClosed returns true if the mux had been closed.
func (m *UDPMuxDefault) IsClosed() bool {
	select {
	case <-m.closedChan:
		return true
	default:
		return false
	}
}

// Close the mux, no further connections could be created.
func (m *UDPMuxDefault) Close() error {
	var err error
	m.closeOnce.Do(func() {
		m.mu.Lock()
		defer m.mu.Unlock()

		for _, c := range m.connsIPv4 {
			_ = c.Close()
		}
		for _, c := range m.connsIPv6 {
			_ = c.Close()
		}

		m.connsIPv4 = make(map[string]*udpMuxedConn)
		m.connsIPv6 = make(map[string]*udpMuxedConn)

		close(m.closedChan)

		_ = m.params.UDPConn.Close()
	})

	return err
}

func (m *UDPMuxDefault) writeTo(buf []byte, rAddr net.Addr) (n int, err error) {
	return m.writeToContext(context.Background(), buf, rAddr)
}

func (m *UDPMuxDefault) writeToContext(ctx context.Context, buf []byte, rAddr net.Addr) (n int, err error) {
	if err = m.startWriteContext(ctx); err != nil {
		return 0, err
	}

	defer func() {
		err = m.finishWrite(err)
	}()

	if err = ctx.Err(); err != nil {
		return 0, err
	}

	if done := ctx.Done(); done != nil {
		// net.PacketConn writes cannot be canceled directly. If ctx is
		// canceled while WriteTo is blocked, abortWrite interrupts it by
		// temporarily setting the shared socket write deadline to now.
		stopAbort := make(chan struct{})
		var stopped atomic.Bool
		defer func() {
			stopped.Store(true)
			close(stopAbort)
		}()
		go func() {
			select {
			case <-done:
				if !stopped.Load() {
					if abortErr := m.abortWrite(); abortErr != nil {
						m.params.Logger.Warnf("Failed to abort UDP write: %v", abortErr)
					}
				}
			case <-stopAbort:
			}
		}()
	}

	n, err = m.params.UDPConn.WriteTo(buf, rAddr)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return n, ctxErr
		}
	}

	return n, err
}

func (m *UDPMuxDefault) abortWrite() error {
	for {
		state := m.writeState.Load()
		if state&udpMuxWriteBlockedBit != 0 || state&udpMuxWriteCountMask == 0 {
			return nil
		}

		if !m.writeState.CompareAndSwap(state, state|udpMuxWriteBlockedBit) {
			continue
		}

		// The deadline applies to the shared UDPConn, so blocked stays set
		// until the final in-flight writer clears the deadline in finishWrite.
		if err := m.params.UDPConn.SetWriteDeadline(time.Now()); err != nil {
			m.clearWriteAbortState()

			return err
		}

		m.setWriteDeadlineArmed()

		return nil
	}
}

func (m *UDPMuxDefault) startWriteContext(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		state := m.writeState.Load()
		if state&udpMuxWriteBlockedBit != 0 {
			runtime.Gosched()

			continue
		}

		if m.writeState.CompareAndSwap(state, state+1) {
			return nil
		}
	}
}

func (m *UDPMuxDefault) finishWrite(writeErr error) error {
	for {
		state := m.writeState.Load()
		count := state & udpMuxWriteCountMask
		if count == 0 {
			return writeErr
		}

		if state&udpMuxWriteBlockedBit != 0 && count == 1 {
			if !m.writeState.CompareAndSwap(state, state-1) {
				continue
			}

			return m.clearWriteDeadlineAfterAbort(writeErr)
		}

		if m.writeState.CompareAndSwap(state, state-1) {
			return writeErr
		}
	}
}

func (m *UDPMuxDefault) setWriteDeadlineArmed() {
	for {
		state := m.writeState.Load()
		if state&udpMuxWriteBlockedBit == 0 || state&udpMuxWriteDeadlineBit != 0 {
			return
		}
		if m.writeState.CompareAndSwap(state, state|udpMuxWriteDeadlineBit) {
			return
		}
	}
}

func (m *UDPMuxDefault) clearWriteDeadlineAfterAbort(writeErr error) error {
	for {
		state := m.writeState.Load()
		if state&udpMuxWriteBlockedBit == 0 {
			return writeErr
		}
		if state&udpMuxWriteDeadlineBit == 0 {
			// The last writer can race with abortWrite after blocked is set but
			// before SetWriteDeadline returns.
			runtime.Gosched()

			continue
		}

		clearErr := m.params.UDPConn.SetWriteDeadline(time.Time{})
		m.writeState.Store(0)
		if writeErr == nil {
			return clearErr
		}

		return writeErr
	}
}

func (m *UDPMuxDefault) clearWriteAbortState() {
	for {
		state := m.writeState.Load()
		newState := state &^ (udpMuxWriteBlockedBit | udpMuxWriteDeadlineBit)
		if state == newState {
			return
		}
		if m.writeState.CompareAndSwap(state, newState) {
			return
		}
	}
}

func (m *UDPMuxDefault) registerConnForAddress(conn *udpMuxedConn, addr ipPort) {
	if m.IsClosed() {
		return
	}

	m.addressMapMu.Lock()
	defer m.addressMapMu.Unlock()

	existing, ok := m.addressMap[addr]
	if ok {
		existing.removeAddress(addr)
	}
	m.addressMap[addr] = conn

	m.params.Logger.Debugf("Registered %s for %s", addr.addr.String(), conn.params.Key)
}

func (m *UDPMuxDefault) createMuxedConn(key string) *udpMuxedConn {
	c := newUDPMuxedConn(&udpMuxedConnParams{
		Mux:       m,
		Key:       key,
		AddrPool:  m.pool,
		LocalAddr: m.LocalAddr(),
		Logger:    m.params.Logger,
	})

	return c
}

func (m *UDPMuxDefault) connWorker() { //nolint:cyclop
	logger := m.params.Logger

	defer func() {
		_ = m.Close()
	}()

	buf := make([]byte, receiveMTU)
	for {
		n, addr, err := m.params.UDPConn.ReadFrom(buf)
		if m.IsClosed() {
			return
		} else if err != nil {
			if os.IsTimeout(err) {
				continue
			} else if !errors.Is(err, io.EOF) {
				logger.Errorf("Failed to read UDP packet: %v", err)
			}

			return
		}

		netUDPAddr, ok := addr.(*net.UDPAddr)
		if !ok {
			logger.Errorf("Underlying PacketConn did not return a UDPAddr")

			return
		}
		udpAddr, err := newIPPort(netUDPAddr.IP, netUDPAddr.Zone, uint16(netUDPAddr.Port)) //nolint:gosec
		if err != nil {
			logger.Errorf("Failed to create a new IP/Port host pair")

			return
		}

		// If we have already seen this address dispatch to the appropriate destination
		m.addressMapMu.Lock()
		destinationConn := m.addressMap[udpAddr]
		m.addressMapMu.Unlock()

		// If we haven't seen this address before but is a STUN packet lookup by ufrag
		if destinationConn == nil && stun.IsMessage(buf[:n]) {
			msg := &stun.Message{
				Raw: append([]byte{}, buf[:n]...),
			}

			if err = msg.Decode(); err != nil {
				m.params.Logger.Warnf("Failed to handle decode ICE from %s: %v", addr.String(), err)

				continue
			}

			attr, stunAttrErr := msg.Get(stun.AttrUsername)
			if stunAttrErr != nil {
				m.params.Logger.Warnf("No Username attribute in STUN message from %s", addr.String())

				continue
			}

			ufrag := strings.Split(string(attr), ":")[0]
			isIPv6 := netUDPAddr.IP.To4() == nil

			m.mu.Lock()
			destinationConn, _ = m.getConn(ufrag, isIPv6)
			m.mu.Unlock()
		}

		if destinationConn == nil {
			m.params.Logger.Tracef("Dropping packet from %s, addr: %s", udpAddr.addr, addr)

			continue
		}

		if err = destinationConn.writePacket(buf[:n], netUDPAddr); err != nil {
			m.params.Logger.Errorf("Failed to write packet: %v", err)
		}
	}
}

func (m *UDPMuxDefault) getConn(ufrag string, isIPv6 bool) (val *udpMuxedConn, ok bool) {
	if isIPv6 {
		val, ok = m.connsIPv6[ufrag]
	} else {
		val, ok = m.connsIPv4[ufrag]
	}

	return
}

type bufferHolder struct {
	next *bufferHolder
	buf  []byte
	addr *net.UDPAddr
}

func newBufferHolder(size int) *bufferHolder {
	return &bufferHolder{
		buf: make([]byte, size),
	}
}

func (b *bufferHolder) reset() {
	b.next = nil
	b.addr = nil
}

type ipPort struct {
	addr netip.Addr
	port uint16
}

// newIPPort create a custom type of address based on netip.Addr and
// port. The underlying ip address passed is converted to IPv6 format
// to simplify ip address handling.
func newIPPort(ip net.IP, zone string, port uint16) (ipPort, error) {
	n, ok := netip.AddrFromSlice(ip.To16())
	if !ok {
		return ipPort{}, errInvalidIPAddress
	}

	return ipPort{
		addr: n.WithZone(zone),
		port: port,
	}, nil
}
