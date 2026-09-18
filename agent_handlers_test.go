// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"testing"
	"time"

	"github.com/pion/transport/v5/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConnectionStateNotifier(t *testing.T) {
	t.Run("TestManyUpdates", func(t *testing.T) {
		defer test.CheckRoutines(t)()

		updates := make(chan struct{}, 1)
		notifier := &handlerNotifier[ConnectionState]{
			handler: func(_ ConnectionState) {
				updates <- struct{}{}
			},
			done: make(chan struct{}),
		}
		// Enqueue all updates upfront to ensure that it
		// doesn't block
		for range 10000 {
			notifier.Enqueue(ConnectionStateNew)
		}
		done := make(chan struct{})
		go func() {
			for range 10000 {
				<-updates
			}
			select {
			case <-updates:
				t.Errorf("received more updates than expected") // nolint
			case <-time.After(1 * time.Second):
			}
			close(done)
		}()
		<-done
		notifier.Close(true)
	})
	t.Run("TestUpdateOrdering", func(t *testing.T) {
		defer test.CheckRoutines(t)()
		updates := make(chan ConnectionState)
		notifer := &handlerNotifier[ConnectionState]{
			handler: func(cs ConnectionState) {
				updates <- cs
			},
			done: make(chan struct{}),
		}
		done := make(chan struct{})
		go func() {
			for i := range 10000 {
				assert.Equal(t, ConnectionState(i), <-updates)
			}
			select {
			case <-updates:
				t.Errorf("received more updates than expected") // nolint
			case <-time.After(1 * time.Second):
			}
			close(done)
		}()
		for i := range 10000 {
			notifer.Enqueue(ConnectionState(i))
		}
		<-done
		notifer.Close(true)
	})
}

func TestHandlerNotifier_Close_AlreadyClosed(t *testing.T) {
	defer test.CheckRoutines(t)()

	notifier := &handlerNotifier[ConnectionState]{
		handler: func(ConnectionState) {},
		done:    make(chan struct{}),
	}

	// first close
	notifier.Close(false)

	isClosed := func(ch <-chan struct{}) bool {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}
	assert.True(t, isClosed(notifier.done), "expected h.done to be closed after first Close")

	// second close should hit `case <-h.done` and return immediately
	// without blocking on the WaitGroup.
	finished := make(chan struct{}, 1)
	go func() {
		notifier.Close(true)
		close(finished)
	}()

	assert.Eventually(t, func() bool {
		select {
		case <-finished:
			return true
		default:
			return false
		}
	}, 250*time.Millisecond, 10*time.Millisecond, "second Close(true) did not return promptly")

	// ensure still closed afterwards
	assert.True(t, isClosed(notifier.done), "expected h.done to remain closed after second Close")

	// sanity: no enqueues should start after close.
	require.False(t, notifier.running)
	require.Zero(t, len(notifier.queue))
}

func TestHandlerNotifier_EnqueueConnectionState_AfterClose(t *testing.T) {
	defer test.CheckRoutines(t)()

	connCh := make(chan struct{}, 1)
	notifier := &handlerNotifier[ConnectionState]{
		handler: func(ConnectionState) { connCh <- struct{}{} },
		done:    make(chan struct{}),
	}

	notifier.Close(false)
	notifier.Enqueue(ConnectionStateConnected)

	assert.Never(t, func() bool {
		select {
		case <-connCh:
			return true
		default:
			return false
		}
	}, 250*time.Millisecond, 10*time.Millisecond, "connectionStateFunc should not be called after close")
}

func TestHandlerNotifier_EnqueueCandidate_AfterClose(t *testing.T) {
	defer test.CheckRoutines(t)()

	candidateCh := make(chan struct{}, 1)
	h := &handlerNotifier[Candidate]{
		handler: func(Candidate) { candidateCh <- struct{}{} },
		done:    make(chan struct{}),
	}

	h.Close(false)
	h.Enqueue(nil)

	assert.Never(t, func() bool {
		select {
		case <-candidateCh:
			return true
		default:
			return false
		}
	}, 250*time.Millisecond, 10*time.Millisecond, "candidateFunc should not be called after close")
}

func TestHandlerNotifier_EnqueueSelectedCandidatePair_AfterClose(t *testing.T) {
	defer test.CheckRoutines(t)()

	pairCh := make(chan struct{}, 1)
	h := &handlerNotifier[*CandidatePair]{
		handler: func(*CandidatePair) { pairCh <- struct{}{} },
		done:    make(chan struct{}),
	}

	h.Close(false)
	h.Enqueue(nil)

	assert.Never(t, func() bool {
		select {
		case <-pairCh:
			return true
		default:
			return false
		}
	}, 250*time.Millisecond, 10*time.Millisecond, "candidatePairFunc should not be called after close")
}
