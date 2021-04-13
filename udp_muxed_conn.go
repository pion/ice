package ice

import (
	"io"
	"net"
	"sync"
	"time"

	"github.com/pion/logging"
)

type udpMuxedConnParams struct {
	Mux        *UDPMuxDefault
	ReadBuffer int
	LocalAddr  net.Addr
	Logger     logging.LeveledLogger
}

type muxedPacket struct {
	Buffer *bufferHolder
	RAddr  net.Addr
	Size   int
}

// udpMuxedConn represents a logical packet conn for a single remote as identified by ufrag
type udpMuxedConn struct {
	params *udpMuxedConnParams
	// remote addresses that we have sent to on this conn
	addresses []string

	// channel holding incoming packets
	recvChan   chan muxedPacket
	closedChan chan struct{}
	closeOnce  sync.Once
	mu         sync.Mutex
}

func newUDPMuxedConn(params *udpMuxedConnParams) *udpMuxedConn {
	p := &udpMuxedConn{
		params:     params,
		recvChan:   make(chan muxedPacket, params.ReadBuffer),
		closedChan: make(chan struct{}),
	}

	return p
}

func (c *udpMuxedConn) ReadFrom(b []byte) (n int, raddr net.Addr, err error) {
	pkt, ok := <-c.recvChan

	if !ok {
		return 0, nil, io.ErrClosedPipe
	}

	if cap(b) < pkt.Size {
		return 0, pkt.RAddr, io.ErrShortBuffer
	}

	copy(b, pkt.Buffer.buffer[:pkt.Size])
	c.params.Mux.doneWithBuffer(pkt.Buffer)
	return pkt.Size, pkt.RAddr, err
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
	c.closeOnce.Do(func() {
		close(c.closedChan)
		close(c.recvChan)
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	c.addresses = nil
	return nil
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
	newAddresses := make([]string, 0, len(c.addresses))
	for _, a := range c.addresses {
		if a != addr {
			newAddresses = append(newAddresses, a)
		}
	}

	c.mu.Lock()
	c.addresses = newAddresses
	c.mu.Unlock()
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

func (c *udpMuxedConn) writePacket(pkt muxedPacket) error {
	select {
	case c.recvChan <- pkt:
		return nil
	case <-c.closedChan:
		return io.ErrClosedPipe
	}
}
