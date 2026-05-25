// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type muxedPacketConn interface {
	net.PacketConn
	readFromContext(ctx context.Context, b []byte) (int, net.Addr, error)
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

func (s *sharedPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	ctx := s.ctx
	if ctx.Err() != nil {
		return 0, nil, io.ErrClosedPipe
	}

	if p := s.readDeadline.Load(); p != nil && !p.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(s.ctx, *p)
		defer cancel()
	}

	n, addr, err := s.underlying.readFromContext(ctx, b)
	if errors.Is(err, context.DeadlineExceeded) {
		return n, addr, os.ErrDeadlineExceeded
	}
	if errors.Is(err, context.Canceled) {
		return n, addr, io.ErrClosedPipe
	}

	return n, addr, err
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
