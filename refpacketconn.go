package ice

import (
	"io"
	"net"
	"sync"
	"time"
)

type sharedPacketConn struct {
	pc     net.PacketConn
	mu     sync.Mutex
	subs   map[*subPacketConn]struct{}
	closed bool
	stopCh chan struct{}
	wg     sync.WaitGroup

	writeMu sync.Mutex
	// hold prevents closing the parent when there are temporarily no subscribers
	hold bool
}

type packet struct {
	b    []byte
	addr net.Addr
}

type subPacketConn struct {
	parent   *sharedPacketConn
	recvCh   chan packet
	closedCh chan struct{}
	once     sync.Once

	// deadlines are per-subscriber, not applied to parent except during WriteTo
	mu          sync.Mutex
	readDL      time.Time
	writeDL     time.Time
	readDLTimer *time.Timer
}

// newSharedPacketConn wraps pc to allow creating multiple logical PacketConn instances.
func newSharedPacketConn(pc net.PacketConn) *sharedPacketConn {
	spc := &sharedPacketConn{
		pc:     pc,
		subs:   make(map[*subPacketConn]struct{}),
		stopCh: make(chan struct{}),
		hold:   true,
	}

	return spc
}

// Release drops the creation hold. If there are no subscribers, it closes the parent.
func (s *sharedPacketConn) Release() {
	s.mu.Lock()
	s.hold = false
	doClose := !s.closed && len(s.subs) == 0
	var parent net.PacketConn
	if doClose {
		s.closed = true
		parent = s.pc
	}
	s.mu.Unlock()
	if parent != nil {
		_ = parent.Close()
	}
}

// Ref returns a new logical PacketConn sharing the same underlying socket.
func (s *sharedPacketConn) Ref() net.PacketConn {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		// Underlying already closed; return a closed subconn
		sc := &subPacketConn{parent: s, recvCh: make(chan packet, 1), closedCh: make(chan struct{})}
		sc.once.Do(func() { close(sc.closedCh) })

		return sc
	}

	sc := &subPacketConn{
		parent:   s,
		recvCh:   make(chan packet, 64),
		closedCh: make(chan struct{}),
	}
	s.subs[sc] = struct{}{}

	// Start reader if first subscriber
	if len(s.subs) == 1 {
		s.startReader()
	}

	return sc
}

func (s *sharedPacketConn) startReader() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		buf := make([]byte, receiveMTU)
		for {
			// allow shutdown
			select {
			case <-s.stopCh:
				return
			default:
			}

			n, addr, err := s.pc.ReadFrom(buf)
			if err != nil {
				// propagate close to subs and exit
				s.mu.Lock()
				for sc := range s.subs {
					close(sc.closedCh)
				}
				s.closed = true
				s.mu.Unlock()
				_ = s.pc.Close()

				return
			}

			pkt := packet{b: append(make([]byte, 0, n), buf[:n]...), addr: addr}

			s.mu.Lock()
			for sc := range s.subs {
				select {
				case sc.recvCh <- pkt:
				default:
					// drop if subscriber is slow
				}
			}
			s.mu.Unlock()
		}
	}()
}

// Close parent when called with no subscribers; not part of PacketConn.
func (s *sharedPacketConn) closeParent() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()

		return
	}
	if len(s.subs) == 0 && !s.hold {
		close(s.stopCh)
		s.closed = true
		_ = s.pc.Close()
	}
	s.mu.Unlock()
	// wait for reader to exit if we closed it
	if s.closed {
		s.wg.Wait()
	}
}

// subPacketConn implements net.PacketConn, backed by sharedPacketConn.

func (s *subPacketConn) ReadFrom(buf []byte) (int, net.Addr, error) {
	for {
		// compute wait channel based on deadline
		var timerCh <-chan time.Time
		s.mu.Lock()
		if !s.readDL.IsZero() {
			now := time.Now()
			if !s.readDL.After(now) {
				s.mu.Unlock()

				return 0, nil, &netTimeoutError{}
			}
			t := time.NewTimer(time.Until(s.readDL))
			s.readDLTimer = t
			timerCh = t.C
		}
		s.mu.Unlock()

		select {
		case pkt, ok := <-s.recvCh:
			if s.readDLTimer != nil {
				s.readDLTimer.Stop()
				s.mu.Lock()
				s.readDLTimer = nil
				s.mu.Unlock()
			}
			if !ok {
				return 0, nil, io.ErrClosedPipe
			}
			n := copy(buf, pkt.b)

			return n, pkt.addr, nil
		case <-s.closedCh:
			if s.readDLTimer != nil {
				s.readDLTimer.Stop()
				s.mu.Lock()
				s.readDLTimer = nil
				s.mu.Unlock()
			}

			return 0, nil, io.ErrClosedPipe
		case <-timerCh:
			return 0, nil, &netTimeoutError{}
		}
	}
}

func (s *subPacketConn) WriteTo(buf []byte, addr net.Addr) (int, error) {
	// Enforce per-subscriber write deadline by temporarily setting parent deadline.
	// Serialize writes to avoid interfering deadlines across subscribers.
	s.parent.writeMu.Lock()
	defer s.parent.writeMu.Unlock()

	// Check deadline before touching the parent.
	s.mu.Lock()
	wdl := s.writeDL
	s.mu.Unlock()
	if !wdl.IsZero() {
		if time.Now().After(wdl) {
			return 0, &netTimeoutError{}
		}
		_ = s.parent.pc.SetWriteDeadline(wdl)
		defer s.parent.pc.SetWriteDeadline(time.Time{})
	}

	return s.parent.pc.WriteTo(buf, addr)
}

func (s *subPacketConn) Close() error {
	var doParentClose bool
	s.once.Do(func() {
		close(s.closedCh)
		s.parent.mu.Lock()
		delete(s.parent.subs, s)
		doParentClose = len(s.parent.subs) == 0 && !s.parent.hold
		s.parent.mu.Unlock()
	})
	if doParentClose {
		s.parent.closeParent()
	}

	return nil
}

func (s *subPacketConn) LocalAddr() net.Addr {
	return s.parent.pc.LocalAddr()
}

func (s *subPacketConn) SetDeadline(t time.Time) error {
	if err := s.SetReadDeadline(t); err != nil {
		return err
	}

	return s.SetWriteDeadline(t)
}

func (s *subPacketConn) SetReadDeadline(t time.Time) error {
	s.mu.Lock()
	s.readDL = t
	if s.readDLTimer != nil {
		s.readDLTimer.Stop()
		s.readDLTimer = nil
	}
	s.mu.Unlock()

	return nil
}

func (s *subPacketConn) SetWriteDeadline(t time.Time) error {
	s.mu.Lock()
	s.writeDL = t
	s.mu.Unlock()

	return nil
}

type netTimeoutError struct{}

func (e *netTimeoutError) Error() string   { return "i/o timeout" }
func (e *netTimeoutError) Timeout() bool   { return true }
func (e *netTimeoutError) Temporary() bool { return true }

var _ net.PacketConn = (*subPacketConn)(nil)
