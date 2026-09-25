// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package taskloop

import (
	"context"
	"errors"
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
func TestLoopErr(t *testing.T) {
	loop := New(func() {})

	if err := loop.Err(); err != nil {
		t.Fatalf("Err() on a running loop: got %v, want nil", err)
	}

	loop.Close()

	if err := loop.Err(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Err() on a closed loop: got %v, want %v", err, ErrClosed)
	}

	// Err() must agree with the Done channel, which callers select on.
	select {
	case <-loop.Done():
	default:
		t.Fatal("Done() not closed after Close()")
	}

	if err := loop.Run(context.Background(), func(context.Context) {}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Run() on a closed loop: got %v, want %v", err, ErrClosed)
	}
}

func TestLoopConcurrentClose(t *testing.T) {
	loop := New(func() {})

	var wait sync.WaitGroup
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			loop.Close()
		}()
	}
	wait.Wait()

	if err := loop.Err(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Err() after concurrent Close(): got %v, want %v", err, ErrClosed)
	}
}

// Err() is called once per Conn.Read and Conn.Write, so it shows up in CPU
// profiles of write-heavy workloads. Keep it cheap enough to inline.
//
// BenchmarkLoopErrSelect is the previous implementation, kept as a baseline:
//
//	BenchmarkLoopErr-10          1000000000    0.64 ns/op    0 B/op
//	BenchmarkLoopErrSelect-10     557208962    4.24 ns/op    0 B/op
func BenchmarkLoopErr(b *testing.B) {
	loop := New(func() {})
	defer loop.Close()

	for i := 0; i < b.N; i++ {
		if err := loop.Err(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLoopErrSelect(b *testing.B) {
	loop := New(func() {})
	defer loop.Close()

	errSelect := func() error {
		select {
		case <-loop.done:
			return ErrClosed
		default:
			return nil
		}
	}

	for i := 0; i < b.N; i++ {
		if err := errSelect(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLoopErrParallel(b *testing.B) {
	loop := New(func() {})
	defer loop.Close()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := loop.Err(); err != nil {
				b.Fatal(err)
			}
		}
	})
}
