package ice

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

const queueLen = 15

//ConnValidator interface, that used for check accepted connection validity
type ConnValidator interface {
	IsValid(interface{}) bool
}

type incomingPacket struct {
	srcAddr *net.Addr
	buffer  []byte
}

//packetTCP paket over tcp conn
type packetTCP struct {
	upstream   chan *[]byte
	downstream chan *incomingPacket
	linked     bool
	validator  ConnValidator
	sync.Mutex
	local net.Addr
}

//newPacketTCP new connection
func newPacketTCP(local net.Addr) *packetTCP {
	return &packetTCP{
		upstream:   make(chan *[]byte, queueLen),
		downstream: make(chan *incomingPacket, queueLen),
		linked:     false,
		local:      local,
	}
}

// ReadFrom reads from...
func (t *packetTCP) ReadFrom(b []byte) (n int, src net.Addr, err error) {
	pkt, ok := <-t.downstream
	if ok {
		err = nil
		n = len(pkt.buffer)
		copy(b, pkt.buffer)
		return n, *pkt.srcAddr, err
	}
	err = io.ErrClosedPipe
	return n, nil, err
}

// WriteTo write bytes. Chan is for not opened tcp conn
func (t *packetTCP) WriteTo(b []byte, dst net.Addr) (n int, err error) {
	if dst.(*net.UDPAddr).Port == 9 {
		return
	}
	bufferCopy := make([]byte, len(b)+2)
	binary.BigEndian.PutUint16(bufferCopy, uint16(len(b)))
	copy(bufferCopy[2:], b)
	select {
	case t.upstream <- &bufferCopy:
	default:
		fmt.Println("out queue full")
	}
	return len(b), err
}

// Close ...
func (t *packetTCP) Close() error {
	if t.linked {
		t.upstream <- nil
	}
	t.linked = false
	return nil
}

//LocalAddr returns address listens to
func (t *packetTCP) LocalAddr() net.Addr {
	return t.local
}

//SetValidator return parent value
func (t *packetTCP) SetValidator(i ConnValidator) {
	t.validator = i
}

//GetParent returns address listens to
func (t *packetTCP) GetParent() ConnValidator {
	return t.validator
}

//SetDeadline ...
func (t *packetTCP) SetDeadline(tm time.Time) error {
	return nil
}

// SetReadDeadline ...
func (t *packetTCP) SetReadDeadline(tm time.Time) error {
	return nil
}

// SetWriteDeadline ...
func (t *packetTCP) SetWriteDeadline(tm time.Time) error {
	return nil
}
