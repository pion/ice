// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package ice

import (
	"context"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// countingPacketConn records whether Close was invoked and how many times,
// and exposes the latest write deadline applied to it so tests can confirm
// SetWriteDeadline delegation. All other net.PacketConn methods return zero
// values. It satisfies the internal muxedPacketConn interface so
// sharedPacketConn can wrap it.
type countingPacketConn struct {
	closeCount    atomic.Int32
	writeDeadline atomic.Pointer[time.Time]
}

func (c *countingPacketConn) ReadFrom([]byte) (int, net.Addr, error) { return 0, nil, nil }
func (c *countingPacketConn) readFromContext(ctx context.Context, _ []byte) (int, net.Addr, error) {
	<-ctx.Done()

	return 0, nil, ctx.Err()
}
func (c *countingPacketConn) WriteTo([]byte, net.Addr) (int, error) { return 0, nil }
func (c *countingPacketConn) LocalAddr() net.Addr                   { return nil }
func (c *countingPacketConn) SetDeadline(time.Time) error           { return nil }
func (c *countingPacketConn) SetReadDeadline(time.Time) error       { return nil }

func (c *countingPacketConn) SetWriteDeadline(t time.Time) error {
	c.writeDeadline.Store(&t)

	return nil
}

func (c *countingPacketConn) Close() error {
	c.closeCount.Add(1)

	return nil
}

func TestSharedPacketConn_RefcountClose(t *testing.T) {
	underlying := &countingPacketConn{}
	var refs atomic.Int32

	a := newSharedPacketConn(underlying, &refs)
	b := newSharedPacketConn(underlying, &refs)
	c := newSharedPacketConn(underlying, &refs)
	require.Equal(t, int32(3), refs.Load())

	require.NoError(t, a.Close())
	require.Equal(t, int32(2), refs.Load())
	require.Zero(t, underlying.closeCount.Load(), "underlying must stay open while refs > 0")

	require.NoError(t, b.Close())
	require.Equal(t, int32(1), refs.Load())
	require.Zero(t, underlying.closeCount.Load(), "underlying must stay open while refs > 0")

	require.NoError(t, c.Close())
	require.Equal(t, int32(0), refs.Load())
	require.Equal(t, int32(1), underlying.closeCount.Load(), "underlying must close exactly once at refs==0")
}

func TestSharedPacketConn_DoubleCloseIdempotent(t *testing.T) {
	underlying := &countingPacketConn{}
	var refs atomic.Int32

	a := newSharedPacketConn(underlying, &refs)
	b := newSharedPacketConn(underlying, &refs)

	require.NoError(t, a.Close())
	require.NoError(t, a.Close(), "second Close on same wrapper must be a no-op")
	require.NoError(t, a.Close())

	// b is still alive — its ref must not have been consumed by a's repeat Close calls.
	require.Equal(t, int32(1), refs.Load())
	require.Zero(t, underlying.closeCount.Load())

	require.NoError(t, b.Close())
	require.Equal(t, int32(1), underlying.closeCount.Load())
}

// blockingReadPacketConn parks readFromContext until release is closed or ctx
// is done, simulating a network conn that has no incoming packets. Close/
// WriteTo are normal.
type blockingReadPacketConn struct {
	countingPacketConn
	release chan struct{}
}

func newBlockingReadPacketConn() *blockingReadPacketConn {
	return &blockingReadPacketConn{release: make(chan struct{})}
}

func (b *blockingReadPacketConn) ReadFrom(_ []byte) (int, net.Addr, error) {
	<-b.release

	return 0, nil, io.EOF
}

func (b *blockingReadPacketConn) readFromContext(ctx context.Context, _ []byte) (int, net.Addr, error) {
	select {
	case <-b.release:
		return 0, nil, io.EOF
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
}

// A wrapper's Close must unblock its own in-flight ReadFrom without closing
// the underlying conn or affecting sibling wrappers — this is the property
// that lets candidate.close work for a single alias candidate while
// siblings still hold references.
func TestSharedPacketConn_CloseUnblocksOwnReadWithoutTouchingSiblings(t *testing.T) {
	underlying := newBlockingReadPacketConn()
	var refs atomic.Int32

	wrapperA := newSharedPacketConn(underlying, &refs)
	wrapperB := newSharedPacketConn(underlying, &refs)
	require.Equal(t, int32(2), refs.Load())
	t.Cleanup(func() {
		close(underlying.release) // let any leftover ReadFrom helpers exit
	})

	// Start a ReadFrom on wrapperA; it will park inside the underlying.
	readDone := make(chan struct{})
	var readErr error
	go func() {
		buf := make([]byte, 1500)
		_, _, readErr = wrapperA.ReadFrom(buf)
		close(readDone)
	}()

	// Give the read time to enter the wait.
	time.Sleep(50 * time.Millisecond)
	select {
	case <-readDone:
		require.FailNow(t, "ReadFrom returned before Close fired")
	default:
	}

	// Closing wrapperA must unblock its own ReadFrom even though wrapperB
	// still holds the underlying alive.
	require.NoError(t, wrapperA.Close())
	select {
	case <-readDone:
	case <-time.After(time.Second):
		require.FailNow(t, "ReadFrom did not return after wrapper Close")
	}
	require.ErrorIs(t, readErr, io.ErrClosedPipe)
	require.Equal(t, int32(1), refs.Load(), "sibling wrapperB must still hold a reference")
	require.Zero(t, underlying.closeCount.Load(), "underlying must stay open while sibling holds a ref")

	// wrapperB is unaffected; closing it releases the underlying.
	require.NoError(t, wrapperB.Close())
	require.Equal(t, int32(0), refs.Load())
	require.Equal(t, int32(1), underlying.closeCount.Load())
}

func TestSharedPacketConn_ConcurrentLastCloserOnlyClosesUnderlying(t *testing.T) {
	// Closing many wrappers concurrently must call underlying.Close exactly
	// once — for the last release. This is the property Agent.deleteAllCandidates
	// relies on to break the alias-candidate close deadlock.
	const n = 64
	underlying := &countingPacketConn{}
	var refs atomic.Int32

	wrappers := make([]*sharedPacketConn, n)
	for i := range wrappers {
		wrappers[i] = newSharedPacketConn(underlying, &refs)
	}
	require.Equal(t, int32(n), refs.Load())

	var wg sync.WaitGroup
	for _, w := range wrappers {
		wg.Add(1)
		go func(wrapper *sharedPacketConn) {
			defer wg.Done()
			require.NoError(t, wrapper.Close())
		}(w)
	}
	wg.Wait()

	require.Equal(t, int32(0), refs.Load())
	require.Equal(t, int32(1), underlying.closeCount.Load(),
		"underlying must close exactly once even under concurrent wrapper close")
}

// SetReadDeadline on one wrapper must not affect concurrent reads on
// sibling wrappers sharing the same underlying conn.
func TestSharedPacketConn_SetReadDeadlineIsolated(t *testing.T) {
	underlying := newBlockingReadPacketConn()
	var refs atomic.Int32

	wrapperA := newSharedPacketConn(underlying, &refs)
	wrapperB := newSharedPacketConn(underlying, &refs)
	t.Cleanup(func() {
		close(underlying.release)
		_ = wrapperA.Close()
		_ = wrapperB.Close()
	})

	// Apply a short read deadline only on wrapperA. wrapperB stays deadline-free.
	require.NoError(t, wrapperA.SetReadDeadline(time.Now().Add(50*time.Millisecond)))

	doneA := make(chan error, 1)
	doneB := make(chan error, 1)
	go func() {
		buf := make([]byte, 1500)
		_, _, err := wrapperA.ReadFrom(buf)
		doneA <- err
	}()
	go func() {
		buf := make([]byte, 1500)
		_, _, err := wrapperB.ReadFrom(buf)
		doneB <- err
	}()

	select {
	case err := <-doneA:
		require.ErrorIs(t, err, os.ErrDeadlineExceeded, "wrapperA must hit its per-wrapper read deadline")
	case <-time.After(time.Second):
		require.FailNow(t, "wrapperA's ReadFrom did not respect its SetReadDeadline")
	}

	// wrapperB must still be parked — no deadline, no release yet.
	select {
	case err := <-doneB:
		require.FailNowf(t, "unexpected return", "wrapperB's ReadFrom returned: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
}

// ReadFrom and WriteTo on a closed wrapper must return io.ErrClosedPipe,
// even if siblings keep the underlying alive and packets are buffered.
func TestSharedPacketConn_ReadWriteAfterCloseReturnsClosed(t *testing.T) {
	underlying := newBlockingReadPacketConn()
	var refs atomic.Int32

	wrapperA := newSharedPacketConn(underlying, &refs)
	wrapperB := newSharedPacketConn(underlying, &refs)
	t.Cleanup(func() {
		close(underlying.release)
		_ = wrapperB.Close()
	})

	require.NoError(t, wrapperA.Close())

	buf := make([]byte, 1500)
	_, _, rerr := wrapperA.ReadFrom(buf)
	require.ErrorIs(t, rerr, io.ErrClosedPipe, "ReadFrom after Close must return io.ErrClosedPipe")

	_, werr := wrapperA.WriteTo([]byte("x"), &net.UDPAddr{})
	require.ErrorIs(t, werr, io.ErrClosedPipe, "WriteTo after Close must return io.ErrClosedPipe")

	// wrapperB is untouched.
	require.Equal(t, int32(1), refs.Load())
	require.Zero(t, underlying.closeCount.Load())
}
