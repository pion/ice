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
