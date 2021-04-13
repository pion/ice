package ice

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pion/logging"
)

// UDPMux allows multiple connections to go over a single UDP port
type UDPMux interface {
	io.Closer
	GetConn(ufrag, network string) (net.PacketConn, error)
	RemoveConnByUfrag(ufrag string)
	Start(port int) error
}

// UDPMuxDefault is an implementation of the interface
type UDPMuxDefault struct {
	params     UDPMuxParams
	listenAddr *net.UDPAddr
	udpConn    *net.UDPConn

	mappingChan chan connMap
	closedChan  chan struct{}
	closeOnce   sync.Once

	// conns is a map of all udpMuxedConn indexed by ufrag|network|candidateType
	conns map[string]*udpMuxedConn

	// buffer pool to recycle buffers for incoming packets
	pool *sync.Pool

	mu sync.Mutex
}

type connMap struct {
	address string
	conn    *udpMuxedConn
}

// UDPMuxParams are parameters for UDPMux.
type UDPMuxParams struct {
	Logger         logging.LeveledLogger
	ReadBufferSize int
}

// NewUDPMuxDefault creates an implementation of UDPMux
func NewUDPMuxDefault(params UDPMuxParams) *UDPMuxDefault {
	return &UDPMuxDefault{
		params:      params,
		conns:       make(map[string]*udpMuxedConn),
		mappingChan: make(chan connMap, 10),
		closedChan:  make(chan struct{}, 1),
		pool: &sync.Pool{
			New: func() interface{} {
				return newBufferHolder(receiveMTU)
			},
		},
	}
}

// Start starts the mux. Before the UDPMux is usable, it must be started
func (m *UDPMuxDefault) Start(port int) error {
	if m.udpConn != nil {
		return ErrMultipleStart
	}
	m.listenAddr = &net.UDPAddr{
		Port: port,
	}
	uc, err := net.ListenUDP(udp, m.listenAddr)
	if err != nil {
		return err
	}
	m.udpConn = uc

	go m.connWorker()
	return nil
}

// LocalAddr returns the listening address of this UDPMuxDefault
func (m *UDPMuxDefault) LocalAddr() net.Addr {
	return m.listenAddr
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

	c := m.createMuxedConn()
	go func() {
		<-c.CloseChannel()
		print("muxed connection closed, removing key ", key, "\n")
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
			m.mappingChan <- connMap{
				address: addr,
				conn:    nil,
			}
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
		m.mappingChan <- connMap{
			address: addr,
			conn:    nil,
		}
	}
}

func (m *UDPMuxDefault) writeTo(buf []byte, raddr net.Addr) (n int, err error) {
	return m.udpConn.WriteTo(buf, raddr)
}

func (m *UDPMuxDefault) doneWithBuffer(buf *bufferHolder) {
	m.pool.Put(buf)
}

func (m *UDPMuxDefault) registerConnForAddress(conn *udpMuxedConn, addr string) {
	if m.IsClosed() {
		return
	}
	m.mappingChan <- connMap{
		address: addr,
		conn:    conn,
	}
}

func (m *UDPMuxDefault) createMuxedConn() *udpMuxedConn {
	c := newUDPMuxedConn(&udpMuxedConnParams{
		Mux:        m,
		ReadBuffer: m.params.ReadBufferSize,
		LocalAddr:  m.LocalAddr(),
		Logger:     m.params.Logger,
	})
	return c
}

func (m *UDPMuxDefault) connWorker() {
	// map of remote addresses -> udpMuxedConn
	// used to look up incoming packets
	remoteMap := make(map[string]*udpMuxedConn)
	logger := m.params.Logger

	defer func() {
		_ = m.Close()
	}()
	for {
		buffer := m.pool.Get().(*bufferHolder)
		_ = m.udpConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, addr, err := m.udpConn.ReadFrom(buffer.buffer)
		// process any mapping changes, this is done as early as possible to prevent channel clogging up
		m.applyMappingChanges(remoteMap)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				m.doneWithBuffer(buffer)
				continue
			} else if err != io.EOF {
				logger.Errorf("could not read udp packet: %v", err)
			}
			return
		}

		// look up forward destination
		addrStr := addr.String()
		c := remoteMap[addrStr]

		if c == nil {
			m.doneWithBuffer(buffer)
			// ignore packets that we don't know where to route to
			continue
		}

		err = c.writePacket(muxedPacket{
			Buffer: buffer,
			Size:   n,
			RAddr:  addr,
		})
		if err != nil {
			logger.Errorf("could not write packet: %v", err)
		}
	}
}

func (m *UDPMuxDefault) applyMappingChanges(remoteMap map[string]*udpMuxedConn) {
	for {
		select {
		case cm := <-m.mappingChan:
			// deregister previous addresses
			existingConn := remoteMap[cm.address]
			if existingConn != nil {
				existingConn.removeAddress(cm.address)
			}
			remoteMap[cm.address] = cm.conn
		default:
			return
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
