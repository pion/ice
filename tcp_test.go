package ice

import (
	"log"
	"net"
	"sync"
	"testing"

	"github.com/pions/transport/test"
)

var x sync.WaitGroup

type rw struct {
	*packetTCP
	dest net.Addr
}

func (r *rw) Read(b []byte) (int, error) {
	n, _, err := r.ReadFrom(b)
	log.Print("read", n)
	return n, err
}

func (r *rw) Write(b []byte) (int, error) {
	log.Print("write", len(b))
	n, err := r.WriteTo(b, r.dest)
	return n, err
}

func TestPacketTCP(t *testing.T) {
	a, b := pipeTCP("127.0.0.1:10000", "127.0.0.1:20000")
	testPacket := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 0}
	a.Write(testPacket)
	readBuff := make([]byte, 1000)
	n, err := b.Read(readBuff)
	log.Print(n, err, readBuff[:n])

	opt := test.Options{
		MsgSize:  100,
		MsgCount: 10, // Order not reliable due to UDP & potentially multiple candidate pairs.
	}
	err = test.StressDuplex(a, b, opt)
	if err != nil {
		t.Fatal(err)
	}

	a.Close()
	b.Close()
	x.Wait()
	log.Print("done")
}

func mkTCPPeer(addr string) *packetTCP {
	a, _ := net.ResolveUDPAddr("udp", addr)
	p := newPacketTCP(a)
	p.linked = true
	return p
}

func pipeTCP(aAddr, bAddr string) (arw, brw *rw) {
	a := mkTCPPeer(aAddr)
	b := mkTCPPeer(bAddr)
	aConn, bConn := net.Pipe()

	go sender(aConn, a)
	go receiver(aConn, a)
	go sender(bConn, b)
	go receiver(bConn, b)

	return &rw{a, b.local}, &rw{b, a.local}
}

func sender(c net.Conn, pconn *packetTCP) {
	x.Add(1)
loop:
	for {
		select {
		case p := <-pconn.upstream:
			if p == nil {
				log.Print(pconn.LocalAddr().String(), "eof chan")
				break loop
			}
			if _, err := c.Write(*p); err != nil {
				log.Print(err)
				break loop
			}
		}
	}
	c.Close()
	x.Done()
}

func receiver(c net.Conn, pconn *packetTCP) {
	x.Add(1)
	b := make([]byte, receiveMTU)
	addr := c.LocalAddr()
	for {
		n, err := ConnReadPacket(c, b)
		if n == 0 {
			log.Print(pconn.LocalAddr().String(), "eof")
			break
		}
		if err != nil {
			log.Print(err)
			break
		}
		p := &incomingPacket{
			srcAddr: &addr,
			buffer:  make([]byte, n),
		}
		copy(p.buffer, b[:n])
		pconn.downstream <- p
	}
	log.Print(pconn.LocalAddr().String(), "closed")
	x.Done()
}
