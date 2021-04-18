package ice

import (
	"encoding/binary"
	"io"
	"net"
	"sync"
	"time"

	"github.com/pion/logging"
	"github.com/pion/transport/packetio"
)

type udpMuxedConnParams struct {
	Mux       *UDPMuxDefault
	AddrPool  *sync.Pool
	Key       string
	LocalAddr net.Addr
	Logger    logging.LeveledLogger
}

// udpMuxedConn represents a logical packet conn for a single remote as identified by ufrag
type udpMuxedConn struct {
	params *udpMuxedConnParams
	// remote addresses that we have sent to on this conn
	addresses []string

	// channel holding incoming packets
	buffer     *packetio.Buffer
	closedChan chan struct{}
	closeOnce  sync.Once
	mu         sync.Mutex
}

func newUDPMuxedConn(params *udpMuxedConnParams) *udpMuxedConn {
	p := &udpMuxedConn{
		params:     params,
		buffer:     packetio.NewBuffer(),
		closedChan: make(chan struct{}),
	}

	return p
}

func (c *udpMuxedConn) ReadFrom(b []byte) (n int, raddr net.Addr, err error) {
	buf := c.params.AddrPool.Get().(*bufferHolder)
	defer c.params.AddrPool.Put(buf)

	// read address
	addrN, err := c.buffer.Read(buf.buffer)
	if err != nil {
		return 0, nil, err
	}

	if raddr, err = decodeUDPAddr(buf.buffer[:addrN]); err != nil {
		return 0, nil, err
	}

	// read data
	n, err = c.buffer.Read(b)
	if err != nil {
		return 0, nil, err
	}

	return n, raddr, err
}

func (c *udpMuxedConn) WriteTo(buf []byte, raddr net.Addr) (n int, err error) {
	if c.isClosed() {
		return 0, io.ErrClosedPipe
	}
	// each time we write to a new address, we'll register it with the mux
	addr := raddr.String()
	if !c.containsAddress(addr) {
		c.addAddress(addr)
	}

	return c.params.Mux.writeTo(buf, raddr)
}

func (c *udpMuxedConn) LocalAddr() net.Addr {
	return c.params.LocalAddr
}

func (c *udpMuxedConn) SetDeadline(tm time.Time) error {
	return nil
}

func (c *udpMuxedConn) SetReadDeadline(tm time.Time) error {
	return nil
}

func (c *udpMuxedConn) SetWriteDeadline(tm time.Time) error {
	return nil
}

func (c *udpMuxedConn) CloseChannel() <-chan struct{} {
	return c.closedChan
}

func (c *udpMuxedConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		err = c.buffer.Close()
		close(c.closedChan)
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	c.addresses = nil
	return err
}

func (c *udpMuxedConn) isClosed() bool {
	select {
	case <-c.closedChan:
		return true
	default:
		return false
	}
}

func (c *udpMuxedConn) getAddresses() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	addresses := make([]string, len(c.addresses))
	copy(addresses, c.addresses)
	return addresses
}

func (c *udpMuxedConn) addAddress(addr string) {
	c.mu.Lock()
	c.addresses = append(c.addresses, addr)
	c.mu.Unlock()

	// map it on mux
	c.params.Mux.registerConnForAddress(c, addr)
}

func (c *udpMuxedConn) removeAddress(addr string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	newAddresses := make([]string, 0, len(c.addresses))
	for _, a := range c.addresses {
		if a != addr {
			newAddresses = append(newAddresses, a)
		}
	}

	c.addresses = newAddresses
}

func (c *udpMuxedConn) containsAddress(addr string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, a := range c.addresses {
		if addr == a {
			return true
		}
	}
	return false
}

func (c *udpMuxedConn) writePacket(data []byte, addr *net.UDPAddr) error {
	// write two packets, address and data
	buf := c.params.AddrPool.Get().(*bufferHolder)
	defer c.params.AddrPool.Put(buf)
	n, err := encodeUDPAddr(addr, buf.buffer)
	if err != nil {
		return nil
	}
	if _, err := c.buffer.Write(buf.buffer[:n]); err != nil {
		return err
	}
	if _, err := c.buffer.Write(data); err != nil {
		return err
	}
	return nil
}

func encodeUDPAddr(addr *net.UDPAddr, buf []byte) (int, error) {
	ipdata, err := addr.IP.MarshalText()
	if err != nil {
		return 0, err
	}
	total := 2 + len(ipdata) + 2 + len(addr.Zone)
	if total > len(buf) {
		return 0, io.ErrShortBuffer
	}

	binary.LittleEndian.PutUint16(buf, uint16(len(ipdata)))
	offset := 2
	n := copy(buf[offset:], ipdata)
	offset += n
	binary.LittleEndian.PutUint16(buf[offset:], uint16(addr.Port))
	offset += 2
	copy(buf[offset:], addr.Zone)
	return total, nil
}

func decodeUDPAddr(buf []byte) (*net.UDPAddr, error) {
	addr := net.UDPAddr{}

	offset := 0
	ipLen := int(binary.LittleEndian.Uint16(buf[:2]))
	offset += 2
	// basic bounds checking
	if ipLen+offset > len(buf) {
		return nil, io.ErrShortBuffer
	}
	if err := addr.IP.UnmarshalText(buf[offset : offset+ipLen]); err != nil {
		return nil, err
	}
	offset += ipLen
	addr.Port = int(binary.LittleEndian.Uint16(buf[offset : offset+2]))
	offset += 2
	zone := make([]byte, len(buf[offset:]))
	copy(zone, buf[offset:])
	addr.Zone = string(zone)

	return &addr, nil
}
