// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package taskloop

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRunReturnsErrClosedWhenLoopClosing(t *testing.T) {
	loop := New(func() {})

	blockStarted := make(chan struct{})
	releaseBlock := make(chan struct{})
	go func() {
		_ = loop.Run(context.Background(), func(context.Context) {
			close(blockStarted)
			<-releaseBlock
		})
	}()
	<-blockStarted

	var secondRan atomic.Bool
	errCh := make(chan error, 1)
	go func() {
		errCh <- loop.Run(context.Background(), func(context.Context) {
			secondRan.Store(true)
		})
	}()

	time.Sleep(10 * time.Millisecond)

	closeDone := make(chan struct{})
	go func() {
		loop.Close()
		close(closeDone)
	}()

	select {
	case err := <-errCh:
		assert.ErrorIs(t, err, ErrClosed)
	case <-time.After(time.Second):
		assert.Fail(t, "Run did not return after loop close")
	}

	close(releaseBlock)

	select {
	case <-closeDone:
	case <-time.After(time.Second):
		assert.Fail(t, "Close did not return")
	}

	assert.False(t, secondRan.Load(), "second task should not excute after loop is closed")
}

func TestCloseWithPreStopConcurrentWaits(t *testing.T) {
	loop := New(func() {})

	blockStarted := make(chan struct{})
	releaseBlock := make(chan struct{})
	go func() {
		_ = loop.Run(context.Background(), func(context.Context) {
			close(blockStarted)
			<-releaseBlock
		})
	}()
	<-blockStarted

	const closers = 8
	var preStopCalls atomic.Int32
	closeReturned := make(chan struct{}, closers)
	var wg sync.WaitGroup
	wg.Add(closers)
	for range closers {
		go func() {
			defer wg.Done()
			loop.CloseWithPreStop(func() {
				preStopCalls.Add(1)
			})
			closeReturned <- struct{}{}
		}()
	}

	assert.Eventually(t, func() bool {
		return preStopCalls.Load() == 1
	}, time.Second, time.Millisecond)

	select {
	case <-closeReturned:
		assert.Fail(t, "CloseWithPreStop returned before the active task finished")
	case <-time.After(10 * time.Millisecond):
	}

	close(releaseBlock)
	wg.Wait()

	assert.Equal(t, int32(1), preStopCalls.Load())
}

func TestClosed(t *testing.T) {
	loop := New(func() {})

	assert.False(t, loop.Closed(), "Closed on a running loop")
	assert.NoError(t, loop.Err(), "Err on a running loop")

	loop.Close()

	assert.True(t, loop.Closed(), "Closed on a closed loop")
	assert.ErrorIs(t, loop.Err(), ErrClosed, "Err on a closed loop")

	// Callers gate on Closed and then report Err, so Closed must never be true
	// while Done is still open and Err would return nil.
	select {
	case <-loop.Done():
	default:
		assert.Fail(t, "Done is not closed after Close")
	}

	assert.ErrorIs(t, loop.Run(context.Background(), func(context.Context) {}), ErrClosed)
}

// The closed check runs once per Conn.Read, Conn.Write and Loop.Run, so it
// shows up in CPU profiles of packet-heavy workloads. Closed is an atomic load
// that inlines into the caller, where Err pays a runtime call for the
// non-blocking receive.
func BenchmarkClosed(b *testing.B) {
	loop := New(func() {})
	defer loop.Close()

	var closed bool
	for b.Loop() {
		closed = loop.Closed()
	}

	assert.False(b, closed)
}

func BenchmarkErr(b *testing.B) {
	loop := New(func() {})
	defer loop.Close()

	var err error
	for b.Loop() {
		err = loop.Err()
	}

	assert.NoError(b, err)
}

func BenchmarkClosedParallel(b *testing.B) {
	loop := New(func() {})
	defer loop.Close()

	b.RunParallel(func(pb *testing.PB) {
		var closed bool
		for pb.Next() {
			closed = loop.Closed()
		}

		assert.False(b, closed)
	})
}
