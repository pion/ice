// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"encoding/binary"
	"io"
	"net"
	"sync"
	"time"

	"github.com/pion/logging"
	"github.com/pion/transport/v3/packetio"
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
	// Remote addresses that we have sent to on this conn
	addresses []ipPort

	// Channel holding incoming packets
	buf        *packetio.Buffer
	closedChan chan struct{}
	closeOnce  sync.Once
	mu         sync.Mutex
}

func newUDPMuxedConn(params *udpMuxedConnParams) *udpMuxedConn {
	p := &udpMuxedConn{
		params:     params,
		buf:        packetio.NewBuffer(),
		closedChan: make(chan struct{}),
	}

	return p
}

func (c *udpMuxedConn) ReadFrom(b []byte) (n int, rAddr net.Addr, err error) {
	buf := c.params.AddrPool.Get().(*bufferHolder) //nolint:forcetypeassert
	defer c.params.AddrPool.Put(buf)

	// Read address
	total, err := c.buf.Read(buf.buf)
	if err != nil {
		return 0, nil, err
	}

	dataLen := int(binary.LittleEndian.Uint16(buf.buf[:2]))
	if dataLen > total || dataLen > len(b) {
		return 0, nil, io.ErrShortBuffer
	}

	// Read data and then address
	offset := 2
	copy(b, buf.buf[offset:offset+dataLen])
	offset += dataLen

	// Read address len & decode address
	addrLen := int(binary.LittleEndian.Uint16(buf.buf[offset : offset+2]))
	offset += 2

	if rAddr, err = decodeUDPAddr(buf.buf[offset : offset+addrLen]); err != nil {
		return 0, nil, err
	}

	return dataLen, rAddr, nil
}

func (c *udpMuxedConn) WriteTo(buf []byte, rAddr net.Addr) (n int, err error) {
	if c.isClosed() {
		return 0, io.ErrClosedPipe
	}
	// Each time we write to a new address, we'll register it with the mux
	netUDPAddr, ok := rAddr.(*net.UDPAddr)
	if !ok {
		return 0, errFailedToCastUDPAddr
	}

	ipAndPort, err := newIPPort(netUDPAddr.IP, netUDPAddr.Zone, uint16(netUDPAddr.Port))
	if err != nil {
		return 0, err
	}
	if !c.containsAddress(ipAndPort) {
		c.addAddress(ipAndPort)
	}

	return c.params.Mux.writeTo(buf, rAddr)
}

func (c *udpMuxedConn) LocalAddr() net.Addr {
	return c.params.LocalAddr
}

func (c *udpMuxedConn) SetDeadline(time.Time) error {
	return nil
}

func (c *udpMuxedConn) SetReadDeadline(time.Time) error {
	return nil
}

func (c *udpMuxedConn) SetWriteDeadline(time.Time) error {
	return nil
}

func (c *udpMuxedConn) CloseChannel() <-chan struct{} {
	return c.closedChan
}

func (c *udpMuxedConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		err = c.buf.Close()
		close(c.closedChan)
	})
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

func (c *udpMuxedConn) getAddresses() []ipPort {
	c.mu.Lock()
	defer c.mu.Unlock()
	addresses := make([]ipPort, len(c.addresses))
	copy(addresses, c.addresses)
	return addresses
}

func (c *udpMuxedConn) addAddress(addr ipPort) {
	c.mu.Lock()
	c.addresses = append(c.addresses, addr)
	c.mu.Unlock()

	// Map it on mux
	c.params.Mux.registerConnForAddress(c, addr)
}

func (c *udpMuxedConn) removeAddress(addr ipPort) {
	c.mu.Lock()
	defer c.mu.Unlock()

	newAddresses := make([]ipPort, 0, len(c.addresses))
	for _, a := range c.addresses {
		if a != addr {
			newAddresses = append(newAddresses, a)
		}
	}

	c.addresses = newAddresses
}

func (c *udpMuxedConn) containsAddress(addr ipPort) bool {
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
	// Write two packets, address and data
	buf := c.params.AddrPool.Get().(*bufferHolder) //nolint:forcetypeassert
	defer c.params.AddrPool.Put(buf)

	// Format of buffer | data len | data bytes | addr len | addr bytes |
	if len(buf.buf) < len(data)+maxAddrSize {
		return io.ErrShortBuffer
	}
	// Data length
	binary.LittleEndian.PutUint16(buf.buf, uint16(len(data)))
	offset := 2

	// Data
	copy(buf.buf[offset:], data)
	offset += len(data)

	// Write address first, leaving room for its length
	n, err := encodeUDPAddr(addr, buf.buf[offset+2:])
	if err != nil {
		return err
	}
	total := offset + n + 2

	// Address len
	binary.LittleEndian.PutUint16(buf.buf[offset:], uint16(n))

	if _, err := c.buf.Write(buf.buf[:total]); err != nil {
		return err
	}
	return nil
}

func encodeUDPAddr(addr *net.UDPAddr, buf []byte) (int, error) {
	total := 1 + len(addr.IP) + 2 + len(addr.Zone)
	if len(buf) < total {
		return 0, io.ErrShortBuffer
	}

	buf[0] = uint8(len(addr.IP))
	offset := 1

	copy(buf[offset:], addr.IP)
	offset += len(addr.IP)

	binary.LittleEndian.PutUint16(buf[offset:], uint16(addr.Port))
	offset += 2

	copy(buf[offset:], addr.Zone)
	return total, nil
}

func decodeUDPAddr(buf []byte) (*net.UDPAddr, error) {
	addr := &net.UDPAddr{}

	// Basic bounds checking
	if len(buf) == 0 || len(buf) < int(buf[0])+3 {
		return nil, io.ErrShortBuffer
	}

	ipLen := int(buf[0])
	offset := 1

	if ipLen == 0 {
		addr.IP = nil
	} else {
		addr.IP = append(addr.IP[:0], buf[offset:offset+ipLen]...)
		offset += ipLen
	}

	addr.Port = int(binary.LittleEndian.Uint16(buf[offset:]))
	offset += 2

	addr.Zone = string(buf[offset:])

	return addr, nil
}
