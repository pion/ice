package ice

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pion/logging"
	"github.com/pion/stun"
)

// UDPMux allows multiple connections to go over a single UDP port
type UDPMux interface {
	io.Closer
	GetConn(ufrag string) (net.PacketConn, error)
	RemoveConnByUfrag(ufrag string)
	GetXORMappedAddr(serverAddr net.Addr, deadline time.Duration) (*stun.XORMappedAddress, error)
}

// UDPMuxDefault is an implementation of the interface
type UDPMuxDefault struct {
	params UDPMuxParams

	closedChan chan struct{}
	closeOnce  sync.Once

	// conns is a map of all udpMuxedConn indexed by ufrag|network|candidateType
	conns map[string]*udpMuxedConn

	addressMapMu sync.RWMutex
	addressMap   map[string]*udpMuxedConn

	// buffer pool to recycle buffers for net.UDPAddr encodes/decodes
	pool *sync.Pool

	mu sync.Mutex

	// since we have a shared socket, for srflx candidates it makes sense to have a shared mapped address across all the agents
	// stun.XORMappedAddress indexed by the STUN server addr
	xorMappedAddr map[string]*xorAddrMap
}

const maxAddrSize = 512

// UDPMuxParams are parameters for UDPMux.
type UDPMuxParams struct {
	Logger                logging.LeveledLogger
	UDPConn               net.PacketConn
	XORMappedAddrCacheTTL time.Duration
}

// NewUDPMuxDefault creates an implementation of UDPMux
func NewUDPMuxDefault(params UDPMuxParams) *UDPMuxDefault {
	if params.Logger == nil {
		params.Logger = logging.NewDefaultLoggerFactory().NewLogger("ice")
	}

	if params.XORMappedAddrCacheTTL == 0 {
		params.XORMappedAddrCacheTTL = time.Second * 25
	}

	m := &UDPMuxDefault{
		addressMap: map[string]*udpMuxedConn{},
		params:     params,
		conns:      make(map[string]*udpMuxedConn),
		closedChan: make(chan struct{}, 1),
		pool: &sync.Pool{
			New: func() interface{} {
				// big enough buffer to fit both packet and address
				return newBufferHolder(receiveMTU + maxAddrSize)
			},
		},
		xorMappedAddr: make(map[string]*xorAddrMap),
	}

	go m.connWorker()

	return m
}

// LocalAddr returns the listening address of this UDPMuxDefault
func (m *UDPMuxDefault) LocalAddr() net.Addr {
	return m.params.UDPConn.LocalAddr()
}

// GetConn returns a PacketConn given the connection's ufrag and network
// creates the connection if an existing one can't be found
func (m *UDPMuxDefault) GetConn(ufrag string) (net.PacketConn, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.IsClosed() {
		return nil, io.ErrClosedPipe
	}

	if c, ok := m.conns[ufrag]; ok {
		return c, nil
	}

	c := m.createMuxedConn(ufrag)
	go func() {
		<-c.CloseChannel()
		m.removeConn(ufrag)
	}()
	m.conns[ufrag] = c
	return c, nil
}

// RemoveConnByUfrag stops and removes the muxed packet connection
func (m *UDPMuxDefault) RemoveConnByUfrag(ufrag string) {
	m.mu.Lock()
	removedConns := make([]*udpMuxedConn, 0)
	for key := range m.conns {
		if key != ufrag {
			continue
		}

		c := m.conns[key]
		delete(m.conns, key)
		if c != nil {
			removedConns = append(removedConns, c)
		}
	}
	// keep lock section small to avoid deadlock with conn lock
	m.mu.Unlock()

	m.addressMapMu.Lock()
	defer m.addressMapMu.Unlock()

	for _, c := range removedConns {
		addresses := c.getAddresses()
		for _, addr := range addresses {
			delete(m.addressMap, addr)
		}
	}
}

// IsClosed returns true if the mux had been closed
func (m *UDPMuxDefault) IsClosed() bool {
	select {
	case <-m.closedChan:
		return true
	default:
		return false
	}
}

// Close the mux, no further connections could be created
func (m *UDPMuxDefault) Close() error {
	var err error
	m.closeOnce.Do(func() {
		m.mu.Lock()
		defer m.mu.Unlock()

		for _, c := range m.conns {
			_ = c.Close()
		}
		m.conns = make(map[string]*udpMuxedConn)
		close(m.closedChan)
	})
	return err
}

func (m *UDPMuxDefault) removeConn(key string) {
	m.mu.Lock()
	c := m.conns[key]
	delete(m.conns, key)
	// keep lock section small to avoid deadlock with conn lock
	m.mu.Unlock()

	if c == nil {
		return
	}

	m.addressMapMu.Lock()
	defer m.addressMapMu.Unlock()

	addresses := c.getAddresses()
	for _, addr := range addresses {
		delete(m.addressMap, addr)
	}
}

func (m *UDPMuxDefault) writeTo(buf []byte, raddr net.Addr) (n int, err error) {
	return m.params.UDPConn.WriteTo(buf, raddr)
}

func (m *UDPMuxDefault) registerConnForAddress(conn *udpMuxedConn, addr string) {
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

	m.params.Logger.Debugf("Registered %s for %s", addr, conn.params.Key)
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

func (m *UDPMuxDefault) handleRemotePing(msg *stun.Message) (*udpMuxedConn, error) {
	attr, err := msg.Get(stun.AttrUsername)
	if err != nil {
		return nil, err
	}

	ufrag := strings.Split(string(attr), ":")[0]
	m.mu.Lock()
	destinationConn := m.conns[ufrag]
	m.mu.Unlock()
	return destinationConn, nil
}

func (m *UDPMuxDefault) handleXORMappedResponse(stunAddr *net.UDPAddr, msg *stun.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	mappedAddr, ok := m.xorMappedAddr[stunAddr.String()]
	if !ok {
		return fmt.Errorf("no address map for %s", stunAddr.String())
	}

	var addr stun.XORMappedAddress
	if err := addr.GetFrom(msg); err != nil {
		return err
	}

	m.xorMappedAddr[stunAddr.String()] = mappedAddr
	mappedAddr.SetAddr(&addr)

	return nil
}

func (m *UDPMuxDefault) connWorker() {
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
			} else if err != io.EOF {
				logger.Errorf("could not read udp packet: %v", err)
			}

			return
		}

		udpAddr, ok := addr.(*net.UDPAddr)
		if !ok {
			logger.Errorf("underlying PacketConn did not return a UDPAddr")
			return
		}

		// If we have already seen this address dispatch to the appropriate destination
		m.addressMapMu.Lock()
		destinationConn := m.addressMap[addr.String()]
		m.addressMapMu.Unlock()

		// If we haven't seen this address before but is a STUN packet lookup by ufrag
		if destinationConn == nil && stun.IsMessage(buf[:n]) {
			msg := &stun.Message{
				Raw: append([]byte{}, buf[:n]...),
			}

			if err = msg.Decode(); err != nil {
				m.params.Logger.Warnf("Failed to handle decode ICE from %s: %v\n", addr.String(), err)
				continue
			}

			if isRemotePing(msg) {
				destinationConn, err = m.handleRemotePing(msg)
				if err != nil {
					m.params.Logger.Warnf("No Username attribute in STUN message from %s\n", addr.String())
					continue
				}
			}

			if isXORMappedResponse(msg) {
				err = m.handleXORMappedResponse(udpAddr, msg)
				if err != nil {
					m.params.Logger.Errorf("%w: %v", errGetXorMappedAddrResponse, err)
				}
				continue
			}
		}

		if destinationConn == nil {
			m.params.Logger.Tracef("dropping packet from %s, addr: %s", udpAddr.String(), addr.String())
			continue
		}

		if err = destinationConn.writePacket(buf[:n], udpAddr); err != nil {
			m.params.Logger.Errorf("could not write packet: %v", err)
		}
	}
}

// isXORMappedResponse indicates whether the message is a XORMappedAddress response from the STUN server
func isXORMappedResponse(msg *stun.Message) bool {
	_, err := msg.Get(stun.AttrXORMappedAddress)
	return err == nil
}

// isRemotePing indicates whether the message is a ping from a remote candidate
func isRemotePing(msg *stun.Message) bool {
	_, err := msg.Get(stun.AttrUsername)
	return err == nil
}

// GetXORMappedAddr returns *stun.XORMappedAddress if already present for a given STUN server.
//
// Makes a STUN binding request to discover mapped address otherwise.
// Blocks until the response is received. The response will be handled by UDPMuxDefault.connWorker
// Method is safe for concurrent use.
func (m *UDPMuxDefault) GetXORMappedAddr(serverAddr net.Addr, deadline time.Duration) (*stun.XORMappedAddress, error) {
	m.mu.Lock()
	mappedAddr, ok := m.xorMappedAddr[serverAddr.String()]
	// if we already have a mapping for this STUN server (address already recieved)
	// and if it is not too old we return it without making a new request to STUN server
	if ok {
		if mappedAddr.expired() {
			mappedAddr.closeWaiters()
			delete(m.xorMappedAddr, serverAddr.String())
			ok = false
		} else if mappedAddr.pending() {
			ok = false
		}
	}
	m.mu.Unlock()
	if ok {
		return mappedAddr.addr, nil
	}

	// otherwise, make a STUN request to discover the address
	// or wait for already sent request to complete
	waitAddrReceived, err := m.sendStun(serverAddr)
	if err != nil {
		return nil, fmt.Errorf("could not send STUN request: %v", err)
	}

	// block until response was handled by the connWorker routine and XORMappedAddress was updated
	select {
	case <-waitAddrReceived:
		// when channel closed, addr was obtained
		m.mu.Lock()
		mappedAddr := *m.xorMappedAddr[serverAddr.String()]
		m.mu.Unlock()
		if mappedAddr.addr == nil {
			return nil, fmt.Errorf("no XORMappedAddress for %s", serverAddr.String())
		}
		return mappedAddr.addr, nil
	case <-time.After(deadline):
		return nil, fmt.Errorf("timeout while waiting for XORMappedAddr")
	}
}

// sendStun sends a STUN request via UDP conn.
//
// The returned channel is closed when the STUN response has been received.
// Method is safe for concurrent use.
func (m *UDPMuxDefault) sendStun(serverAddr net.Addr) (chan struct{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// if record present in the map, we already sent a STUN request,
	// just wait when waitAddrRecieved will be closed
	addrMap, ok := m.xorMappedAddr[serverAddr.String()]
	if !ok {
		addrMap = &xorAddrMap{
			expiresAt:        time.Now().Add(m.params.XORMappedAddrCacheTTL),
			waitAddrReceived: make(chan struct{}),
		}
		m.xorMappedAddr[serverAddr.String()] = addrMap
	}

	req, err := stun.Build(stun.BindingRequest, stun.TransactionID)
	if err != nil {
		return nil, err
	}

	if _, err = m.params.UDPConn.WriteTo(req.Raw, serverAddr); err != nil {
		return nil, err
	}

	return addrMap.waitAddrReceived, nil
}

type bufferHolder struct {
	buffer []byte
}

func newBufferHolder(size int) *bufferHolder {
	return &bufferHolder{
		buffer: make([]byte, size),
	}
}

type xorAddrMap struct {
	ctx              context.Context
	addr             *stun.XORMappedAddress
	waitAddrReceived chan struct{}
	expiresAt        time.Time
}

func (a *xorAddrMap) closeWaiters() {
	select {
	case <-a.waitAddrReceived:
		// notify was close, ok, that means we received duplicate response
		// just exit
		break
	default:
		// notify tha twe have a new addr
		close(a.waitAddrReceived)
	}
}

func (a *xorAddrMap) pending() bool {
	return a.addr == nil
}

func (a *xorAddrMap) expired() bool {
	return a.expiresAt.Before(time.Now())
}

func (a *xorAddrMap) SetAddr(addr *stun.XORMappedAddress) {
	a.addr = addr
	a.closeWaiters()
}
