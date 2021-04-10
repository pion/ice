package ice

import (
	"io"
	"net"
	"sync"

	"github.com/pion/logging"
)

// UDPMux allows multiple connections to go over a single UDP port
type UDPMux interface {
	io.Closer
	GetConnByUfrag(ufrag string) (net.PacketConn, error)
	RemoveConnByUfrag(ufrag string)
}

// UDPMuxDefault is an implementation of the interface
type UDPMuxDefault struct {
	params     UDPMuxParams
	listenAddr *net.UDPAddr
	udpConn    *net.UDPConn

	mappingChan chan connMap
	closedChan  chan struct{}
	closeOnce   sync.Once

	// conns is a map of all udpMuxedConn indexed by ufrag
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
				return make([]byte, receiveMTU)
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

// GetConnByUfrag returns a PacketConn given the connection's ufrag.
// creates the connection if an existing one can't be found
func (m *UDPMuxDefault) GetConnByUfrag(ufrag string) (net.PacketConn, error) {
	if m.udpConn == nil {
		return nil, ErrMuxNotStarted
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.IsClosed() {
		return nil, io.ErrClosedPipe
	}

	if c, ok := m.conns[ufrag]; ok {
		return c, nil
	}

	c := m.createMuxedConn()
	go func() {
		<-c.CloseChannel()
		m.RemoveConnByUfrag(ufrag)
	}()
	m.conns[ufrag] = c
	return c, nil
}

// RemoveConnByUfrag stops and removes the muxed packet connection
func (m *UDPMuxDefault) RemoveConnByUfrag(ufrag string) {
	// get addresses to remove
	m.mu.Lock()
	c := m.conns[ufrag]
	delete(m.conns, ufrag)
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

func (m *UDPMuxDefault) writeTo(buf []byte, raddr net.Addr) (n int, err error) {
	return m.udpConn.WriteTo(buf, raddr)
}

func (m *UDPMuxDefault) doneWithBuffer(buf []byte) {
	//nolint
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
		buffer := m.pool.Get().([]byte)
		n, addr, err := m.udpConn.ReadFrom(buffer)
		if err == io.EOF {
			return
		} else if err != nil {
			logger.Errorf("could not read udp packet: %v", err)
			return
		}

		// process any mapping changes
		m.applyMappingChanges(remoteMap)

		// look up forward destination
		addrStr := addr.String()
		c := remoteMap[addrStr]

		if c == nil {
			//nolint
			m.pool.Put(buffer)
			// ignore packets that we don't know where to route to
			continue
		}

		err = c.writePacket(muxedPacket{
			Data:  buffer,
			Size:  n,
			RAddr: addr,
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
