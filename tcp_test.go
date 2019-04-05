package ice

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pions/transport/test"
)

var x sync.WaitGroup

type rw struct {
	*packetTCP
	dest net.Addr
}

func (r *rw) Read(b []byte) (int, error) {
	n, _, err := r.ReadFrom(b)
	return n, err
}

func (r *rw) Write(b []byte) (int, error) {
	n, err := r.WriteTo(b, r.dest)
	return n, err
}

var errChan = make(chan error)

func TestPacketTCP(t *testing.T) {
	a, b := pipeTCP("127.0.0.1:10000", "127.0.0.1:20000")

	opt := test.Options{
		MsgSize:  100,
		MsgCount: 10,
	}
	err := test.StressDuplex(a, b, opt)
	if err != nil {
		t.Fatal(err)
	}

	a.Close()
	b.Close()
	aUpDone, bUpDone := false, false
	aDnDone, bDnDone := false, false
	timer := time.NewTimer(5 * time.Second)
	for {
		select {
		case <-timer.C:
			t.Fatal("timeout: subchannel not closed")
		case aUpDone = <-a.upstreamDone:
		case aDnDone = <-a.downstreamDone:
		case bUpDone = <-b.upstreamDone:
		case bDnDone = <-b.downstreamDone:
		case err = <-errChan:
			t.Fatal(err)
		}
		if aUpDone && aDnDone && bUpDone && bDnDone {
			break
		}
	}
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
	go func() {
		if err := a.attachConn(aConn); err != nil {
			errChan <- err
		}
	}()
	go func() {
		if err := b.attachConn(bConn); err != nil {
			errChan <- err
		}
	}()
	return &rw{a, b.local}, &rw{b, a.local}
}
