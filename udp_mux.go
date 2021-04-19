package ice

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/pion/logging"
)

// UDPMux allows multiple connections to go over a single UDP port
type UDPMux interface {
	io.Closer
	GetConn(ufrag, network string) (net.PacketConn, error)
	RemoveConnByUfrag(ufrag string)
	Start(conn net.PacketConn) error
}

// UDPMuxDefault is an implementation of the interface
type UDPMuxDefault struct {
	params  UDPMuxParams
	udpConn net.PacketConn

	closedChan chan struct{}
	closeOnce  sync.Once

	// conns is a map of all udpMuxedConn indexed by ufrag|network|candidateType
	conns map[string]*udpMuxedConn

	// map of udpAddr -> udpMuxedConn
	addressMap sync.Map

	// buffer pool to recycle buffers for net.UDPAddr encodes/decodes
	pool *sync.Pool

	mu sync.Mutex
}

const maxAddrSize = 512

// UDPMuxParams are parameters for UDPMux.
type UDPMuxParams struct {
	Logger logging.LeveledLogger
}

// NewUDPMuxDefault creates an implementation of UDPMux
func NewUDPMuxDefault(params UDPMuxParams) *UDPMuxDefault {
	return &UDPMuxDefault{
		params:     params,
		conns:      make(map[string]*udpMuxedConn),
		closedChan: make(chan struct{}, 1),
		pool: &sync.Pool{
			New: func() interface{} {
				// big enough buffer to fit both packet and address
				return newBufferHolder(receiveMTU + maxAddrSize)
			},
		},
	}
}

// Start starts the mux. Before the UDPMux is usable, it must be started
// the mux will read/write data on the underlying net.PacketConn. It must
// conn.ReadFrom *MUST* return a *net.UDPAddr
func (m *UDPMuxDefault) Start(conn net.PacketConn) error {
	if m.udpConn != nil {
		return ErrMultipleStart
	}
	m.udpConn = conn

	go m.connWorker()
	return nil
}

// LocalAddr returns the listening address of this UDPMuxDefault
func (m *UDPMuxDefault) LocalAddr() net.Addr {
	return m.udpConn.LocalAddr()
}

// GetConn returns a PacketConn given the connection's ufrag and network
// creates the connection if an existing one can't be found
func (m *UDPMuxDefault) GetConn(ufrag, network string) (net.PacketConn, error) {
	if m.udpConn == nil {
		return nil, ErrMuxNotStarted
	}

	key := fmt.Sprintf("%s|%s", ufrag, network)

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.IsClosed() {
		return nil, io.ErrClosedPipe
	}

	if c, ok := m.conns[key]; ok {
		return c, nil
	}

	c := m.createMuxedConn(key)
	go func() {
		<-c.CloseChannel()
		m.removeConn(key)
	}()
	m.conns[key] = c
	return c, nil
}

// RemoveConnByUfrag stops and removes the muxed packet connection
func (m *UDPMuxDefault) RemoveConnByUfrag(ufrag string) {
	m.mu.Lock()
	removedConns := make([]*udpMuxedConn, 0)
	for key := range m.conns {
		if !strings.HasPrefix(key, ufrag) {
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

	for _, c := range removedConns {
		addresses := c.getAddresses()
		for _, addr := range addresses {
			m.addressMap.Delete(addr)
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

		// close udp conn and prevent packets coming in
		err = m.udpConn.Close()

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

	addresses := c.getAddresses()
	for _, addr := range addresses {
		m.addressMap.Delete(addr)
	}
}

func (m *UDPMuxDefault) writeTo(buf []byte, raddr net.Addr) (n int, err error) {
	return m.udpConn.WriteTo(buf, raddr)
}

func (m *UDPMuxDefault) registerConnForAddress(conn *udpMuxedConn, addr string) {
	if m.IsClosed() {
		return
	}
	existing, ok := m.addressMap.Load(addr)
	if ok {
		existing.(*udpMuxedConn).removeAddress(addr)
	}
	m.addressMap.Store(addr, conn)
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

func (m *UDPMuxDefault) connWorker() {
	logger := m.params.Logger

	defer func() {
		_ = m.Close()
	}()
	buf := make([]byte, receiveMTU)
	for {
		n, addr, err := m.udpConn.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				continue
			} else if err != io.EOF {
				logger.Errorf("could not read udp packet: %v", err)
			}
			return
		}

		// look up forward destination
		v, ok := m.addressMap.Load(addr.String())
		if !ok {
			// ignore packets that we don't know where to route to
			continue
		}

		udpAddr, ok := addr.(*net.UDPAddr)
		if !ok {
			logger.Errorf("underlying PacketConn did not return a UDPAddr")
			return
		}
		c := v.(*udpMuxedConn)
		err = c.writePacket(buf[:n], udpAddr)
		if err != nil {
			logger.Errorf("could not write packet: %v", err)
		}
	}
}

type bufferHolder struct {
	buffer []byte
}

func newBufferHolder(size int) *bufferHolder {
	return &bufferHolder{
		buffer: make([]byte, size),
	}
}
