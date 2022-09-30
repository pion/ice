package ice

import (
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/pion/logging"
	"github.com/pion/stun"
)

// UDPMux allows multiple connections to go over a single UDP port
type UDPMux interface {
	io.Closer
	GetConn(ufrag string, isIPv6 bool, local net.IP) (net.PacketConn, error)
	RemoveConnByUfrag(ufrag string)
}

// UDPMuxDefault is an implementation of the interface
type UDPMuxDefault struct {
	params UDPMuxParams

	closedChan chan struct{}
	closeOnce  sync.Once

	// connsIPv4 and connsIPv6 are maps of all udpMuxedConn indexed by ufrag|network|candidateType
	connsIPv4, connsIPv6 map[string]map[ipAddr]*udpMuxedConn

	addressMapMu sync.RWMutex

	// remote address (ip:port) -> (localip -> udpMuxedConn)
	addressMap map[string]map[ipAddr]*udpMuxedConn

	// buffer pool to recycle buffers for net.UDPAddr encodes/decodes
	pool *sync.Pool

	mu sync.Mutex
}

const maxAddrSize = 512

// UDPMuxConn is a udp PacketConn with ReadMsgUDP and File method
// to retrieve the destination local address of the received packet
type UDPMuxConn interface {
	net.PacketConn

	// ReadMsgUdp used to get destination address when received a udp packet
	ReadMsgUDP(b, oob []byte) (n, oobn, flags int, addr *net.UDPAddr, err error)

	// File returns a copy of the underlying os.File.
	// It is the caller's responsibility to close f when finished.
	// Closing c does not affect f, and closing f does not affect c.
	File() (f *os.File, err error)
}

// UDPMuxParams are parameters for UDPMux.
type UDPMuxParams struct {
	Logger  logging.LeveledLogger
	UDPConn UDPMuxConn
}

// NewUDPMuxDefault creates an implementation of UDPMux
func NewUDPMuxDefault(params UDPMuxParams) *UDPMuxDefault {
	if params.Logger == nil {
		params.Logger = logging.NewDefaultLoggerFactory().NewLogger("ice")
	}

	m := &UDPMuxDefault{
		addressMap: make(map[string]map[ipAddr]*udpMuxedConn),
		params:     params,
		connsIPv4:  make(map[string]map[ipAddr]*udpMuxedConn),
		connsIPv6:  make(map[string]map[ipAddr]*udpMuxedConn),
		closedChan: make(chan struct{}, 1),
		pool: &sync.Pool{
			New: func() interface{} {
				// big enough buffer to fit both packet and address
				return newBufferHolder(receiveMTU + maxAddrSize)
			},
		},
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
func (m *UDPMuxDefault) GetConn(ufrag string, isIPv6 bool, local net.IP) (net.PacketConn, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.IsClosed() {
		return nil, io.ErrClosedPipe
	}

	if conn, ok := m.getConn(ufrag, isIPv6, local); ok {
		return conn, nil
	}

	c, err := m.createMuxedConn(ufrag, local)
	if err != nil {
		return nil, err
	}
	go func() {
		<-c.CloseChannel()
		m.removeConnByUfragAndLocalHost(ufrag, local)
	}()

	var (
		conns map[ipAddr]*udpMuxedConn
		ok    bool
	)
	if isIPv6 {
		if conns, ok = m.connsIPv6[ufrag]; !ok {
			conns = make(map[ipAddr]*udpMuxedConn)
			m.connsIPv6[ufrag] = conns
		}
	} else {
		if conns, ok = m.connsIPv4[ufrag]; !ok {
			conns = make(map[ipAddr]*udpMuxedConn)
			m.connsIPv4[ufrag] = conns
		}
	}
	conns[ipAddr(local.String())] = c

	return c, nil
}

// RemoveConnByUfrag stops and removes the muxed packet connection
func (m *UDPMuxDefault) RemoveConnByUfrag(ufrag string) {
	removedConns := make([]*udpMuxedConn, 0, 4)

	// Keep lock section small to avoid deadlock with conn lock
	m.mu.Lock()
	if conns, ok := m.connsIPv4[ufrag]; ok {
		delete(m.connsIPv4, ufrag)
		for _, c := range conns {
			removedConns = append(removedConns, c)
		}
	}
	if conns, ok := m.connsIPv6[ufrag]; ok {
		delete(m.connsIPv6, ufrag)
		for _, c := range conns {
			removedConns = append(removedConns, c)
		}
	}
	m.mu.Unlock()

	if len(removedConns) == 0 {
		// No need to lock if no connection was found
		return
	}

	m.addressMapMu.Lock()
	defer m.addressMapMu.Unlock()

	for _, c := range removedConns {
		addresses := c.getAddresses()
		for _, addr := range addresses {
			if conns, ok := m.addressMap[addr]; ok {
				delete(conns, ipAddr(c.params.LocalIP.String()))
				if len(conns) == 0 {
					delete(m.addressMap, addr)
				}
			}
		}
	}
}

func (m *UDPMuxDefault) removeConnByUfragAndLocalHost(ufrag string, local net.IP) {
	removedConns := make([]*udpMuxedConn, 0, 4)

	localIP := ipAddr(local.String())
	// Keep lock section small to avoid deadlock with conn lock
	m.mu.Lock()
	if conns, ok := m.connsIPv4[ufrag]; ok {
		if conn, ok := conns[localIP]; ok {
			delete(conns, localIP)
			if len(conns) == 0 {
				delete(m.connsIPv4, ufrag)
			}
			removedConns = append(removedConns, conn)
		}
	}
	if conns, ok := m.connsIPv6[ufrag]; ok {
		if conn, ok := conns[localIP]; ok {
			delete(conns, localIP)
			if len(conns) == 0 {
				delete(m.connsIPv6, ufrag)
			}
			removedConns = append(removedConns, conn)
		}
	}
	m.mu.Unlock()

	if len(removedConns) == 0 {
		// No need to lock if no connection was found
		return
	}

	m.addressMapMu.Lock()
	defer m.addressMapMu.Unlock()

	for _, c := range removedConns {
		addresses := c.getAddresses()
		for _, addr := range addresses {
			if conns, ok := m.addressMap[addr]; ok {
				delete(conns, ipAddr(c.params.LocalIP.String()))
				if len(conns) == 0 {
					delete(m.addressMap, addr)
				}
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

		for _, conns := range m.connsIPv4 {
			for _, c := range conns {
				_ = c.Close()
			}
		}
		for _, conns := range m.connsIPv6 {
			for _, c := range conns {
				_ = c.Close()
			}
		}

		m.connsIPv4 = make(map[string]map[ipAddr]*udpMuxedConn)
		m.connsIPv6 = make(map[string]map[ipAddr]*udpMuxedConn)

		// ReadMsgUDP will block until something is received, otherwise it will block forever
		// and the Conn's Close method too. So send a packet to wake it for exit.
		close(m.closedChan)
		closeConn, errConn := net.DialUDP("udp", nil, m.params.UDPConn.LocalAddr().(*net.UDPAddr))
		// i386 doesn't support dial local ipv6 address
		if errConn != nil && strings.Contains(errConn.Error(), "dial udp [::]:") &&
			strings.Contains(errConn.Error(), "connect: cannot assign requested address") {
			closeConn, errConn = net.DialUDP("udp4", nil, &net.UDPAddr{Port: m.params.UDPConn.LocalAddr().(*net.UDPAddr).Port})
		}
		if errConn != nil {
			m.params.Logger.Errorf("Failed to open close notify socket, %v", errConn)
		} else {
			defer func() {
				_ = closeConn.Close()
			}()
			_, errConn = closeConn.Write([]byte("close"))
			if errConn != nil {
				m.params.Logger.Errorf("Failed to send close notify msg, %v", errConn)
			}
		}
	})
	return err
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

	conns, ok := m.addressMap[addr]
	if ok {
		existing, ok := conns[ipAddr(conn.params.LocalIP.String())]
		if ok {
			existing.removeAddress(addr)
		}
	} else {
		conns = make(map[ipAddr]*udpMuxedConn)
		m.addressMap[addr] = conns
	}
	conns[ipAddr(conn.params.LocalIP.String())] = conn

	m.params.Logger.Debugf("Registered %s for %s, local %s", addr, conn.params.Key, conn.params.LocalIP.String())
}

func (m *UDPMuxDefault) createMuxedConn(key string, local net.IP) (*udpMuxedConn, error) {
	m.params.Logger.Debugf("Creating new muxed connection, key:%s local:%s ", key, local.String())
	addr, ok := m.LocalAddr().(*net.UDPAddr)
	if !ok {
		return nil, ErrGetTransportAddress
	}
	localAddr := *addr
	localAddr.IP = local
	c := newUDPMuxedConn(&udpMuxedConnParams{
		Mux:       m,
		Key:       key,
		AddrPool:  m.pool,
		LocalAddr: &localAddr,
		LocalIP:   local,
		Logger:    m.params.Logger,
	})
	return c, nil
}

func (m *UDPMuxDefault) connWorker() { //nolint:gocognit
	logger := m.params.Logger

	defer func() {
		_ = m.Close()
	}()

	localUDPAddr, _ := m.LocalAddr().(*net.UDPAddr)

	buf := make([]byte, receiveMTU)
	file, _ := m.params.UDPConn.File()
	setUDPSocketOptionsForLocalAddr(file.Fd(), m.params.Logger)
	_ = file.Close()
	oob := make([]byte, receiveMTU)
	for {
		localHost := localUDPAddr.IP

		n, oobn, _, addr, err := m.params.UDPConn.ReadMsgUDP(buf, oob)
		if m.IsClosed() {
			return
		} else if err != nil {
			if os.IsTimeout(err) {
				continue
			} else if !errors.Is(err, io.EOF) {
				logger.Errorf("could not read udp packet: %v", err)
			}

			return
		}

		// get destination local addr from received packet
		if oobIP, addrErr := getLocalAddrFromOob(oob[:oobn]); addrErr == nil {
			localHost = oobIP
		} else {
			m.params.Logger.Warnf("could not get local addr from oob: %v, remote %s", addrErr, addr)
		}

		// If we have already seen this address dispatch to the appropriate destination
		var destinationConn *udpMuxedConn
		m.addressMapMu.Lock()
		if conns, ok := m.addressMap[addr.String()]; ok {
			destinationConn, ok = conns[ipAddr(localHost.String())]
			if !ok {
				for _, c := range conns {
					destinationConn = c
					break
				}
			}
		}
		m.addressMapMu.Unlock()

		// If we haven't seen this address before but is a STUN packet lookup by ufrag
		if destinationConn == nil && stun.IsMessage(buf[:n]) && !localHost.IsUnspecified() {
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
			isIPv6 := addr.IP.To4() == nil

			m.mu.Lock()
			destinationConn, _ = m.getConn(ufrag, isIPv6, localHost)
			m.mu.Unlock()
		}

		if destinationConn == nil {
			m.params.Logger.Tracef("dropping packet from %s", addr.String())
			continue
		}

		if err = destinationConn.writePacket(buf[:n], addr); err != nil {
			m.params.Logger.Errorf("could not write packet: %v", err)
		}
	}
}

func (m *UDPMuxDefault) getConn(ufrag string, isIPv6 bool, local net.IP) (val *udpMuxedConn, ok bool) {
	var conns map[ipAddr]*udpMuxedConn
	if isIPv6 {
		conns, ok = m.connsIPv6[ufrag]
	} else {
		conns, ok = m.connsIPv4[ufrag]
	}
	if conns != nil {
		val, ok = conns[ipAddr(local.String())]
	}
	return
}

type bufferHolder struct {
	buffer []byte
}

func newBufferHolder(size int) *bufferHolder {
	return &bufferHolder{
		buffer: make([]byte, size),
	}
}
