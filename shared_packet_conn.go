// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type muxedPacketConn interface {
	net.PacketConn
	readFromContext(ctx context.Context, b []byte) (int, net.Addr, error)
}

// muxedAddrPortConn adds netip.AddrPort I/O to muxedPacketConn.
type muxedAddrPortConn interface {
	muxedPacketConn
	AddrPortReaderWriter
	readFromAddrPortContext(ctx context.Context, b []byte) (int, netip.AddrPort, error)
}

// sharedPacketConn is a reference-counted wrapper around an underlying
// muxedPacketConn owned by a mux. The mux can hand out several wrappers that
// share one underlying connection (for example, alias host candidates
// produced by an AddressRewrite append rule).
//
// Each wrapper owns its own context. Close() cancels that context, which
// unblocks any in-flight ReadFrom on this wrapper — without affecting
// siblings that still hold their own references. The underlying connection
// itself is closed only when the last wrapper is released.
type sharedPacketConn struct {
	underlying muxedPacketConn
	refs       *atomic.Int32

	ctx       context.Context //nolint:containedctx
	cancel    context.CancelFunc
	closeOnce sync.Once

	readDeadline atomic.Pointer[time.Time]
}

// newSharedPacketConn increments the shared refcount and returns a wrapper.
// Each returned wrapper must have Close called exactly once.
func newSharedPacketConn(u muxedPacketConn, refs *atomic.Int32) *sharedPacketConn {
	refs.Add(1)
	ctx, cancel := context.WithCancel(context.Background())

	return &sharedPacketConn{
		underlying: u,
		refs:       refs,
		ctx:        ctx,
		cancel:     cancel,
	}
}

// readContext returns the context to use for a single read, arming the
// configured read deadline if one is set. cancel is non-nil when the caller
// must cancel after the read completes.
func (s *sharedPacketConn) readContext() (ctx context.Context, cancel context.CancelFunc, err error) {
	ctx = s.ctx
	if ctx.Err() != nil {
		return nil, nil, io.ErrClosedPipe
	}

	if p := s.readDeadline.Load(); p != nil && !p.IsZero() {
		ctx, cancel = context.WithDeadline(s.ctx, *p)
	}

	return ctx, cancel, nil
}

// mapContextError converts context termination errors to the net.Conn
// equivalents callers expect.
func mapContextError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return os.ErrDeadlineExceeded
	}
	if errors.Is(err, context.Canceled) {
		return io.ErrClosedPipe
	}

	return err
}

func (s *sharedPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	ctx, cancel, err := s.readContext()
	if err != nil {
		return 0, nil, err
	}
	if cancel != nil {
		defer cancel()
	}

	n, addr, err := s.underlying.readFromContext(ctx, b)

	return n, addr, mapContextError(err)
}

func (s *sharedPacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	if s.ctx.Err() != nil {
		return 0, io.ErrClosedPipe
	}

	return s.underlying.WriteTo(b, addr)
}

func (s *sharedPacketConn) LocalAddr() net.Addr {
	return s.underlying.LocalAddr()
}

func (s *sharedPacketConn) SetReadDeadline(t time.Time) error {
	if s.ctx.Err() != nil {
		return io.ErrClosedPipe
	}

	s.readDeadline.Store(&t)

	return nil
}

func (s *sharedPacketConn) SetWriteDeadline(t time.Time) error {
	if s.ctx.Err() != nil {
		return io.ErrClosedPipe
	}

	return s.underlying.SetWriteDeadline(t)
}

func (s *sharedPacketConn) SetDeadline(t time.Time) error {
	if err := s.SetReadDeadline(t); err != nil {
		return err
	}

	return s.SetWriteDeadline(t)
}

func (s *sharedPacketConn) abortWrite() error {
	aborter, ok := s.underlying.(writeAborter)
	if !ok {
		return nil
	}

	return aborter.abortWrite()
}

func (s *sharedPacketConn) Close() error {
	var err error
	fired := false
	s.closeOnce.Do(func() {
		fired = true
		s.cancel()
		if s.refs.Add(-1) <= 0 {
			err = s.underlying.Close()
		}
	})
	if !fired {
		return nil
	}

	return err
}

// sharedAddrPortConn adds netip.AddrPort I/O to sharedPacketConn.
type sharedAddrPortConn struct {
	*sharedPacketConn
	underlyingAddrPort muxedAddrPortConn
}

func newSharedAddrPortConn(u muxedAddrPortConn, refs *atomic.Int32) *sharedAddrPortConn {
	return &sharedAddrPortConn{
		sharedPacketConn:   newSharedPacketConn(u, refs),
		underlyingAddrPort: u,
	}
}

func (s *sharedAddrPortConn) ReadFromAddrPort(b []byte) (int, netip.AddrPort, error) {
	ctx, cancel, err := s.readContext()
	if err != nil {
		return 0, netip.AddrPort{}, err
	}
	if cancel != nil {
		defer cancel()
	}

	n, addr, err := s.underlyingAddrPort.readFromAddrPortContext(ctx, b)

	return n, addr, mapContextError(err)
}

func (s *sharedAddrPortConn) WriteToAddrPort(b []byte, addr netip.AddrPort) (int, error) {
	if s.ctx.Err() != nil {
		return 0, io.ErrClosedPipe
	}

	return s.underlyingAddrPort.WriteToAddrPort(b, addr)
}
